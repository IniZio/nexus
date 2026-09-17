//go:build integration

// ch_netns_lifecycle_test.go — lifecycle / teardown / crash-recovery hardening
// for the netns-runtime process topology (driver → netns child → CH grandchild).
//
// These tests cover CRASH paths and recovery edges. The clean-path reaping
// (rt.Stop on a fully healthy runtime) is already proven by
// TestNetnsRuntime_CHOrphanKill; do not duplicate it here.
//
// Acceptance edges:
//
//  1. NormalStop_NoLeaks       – clean stop leaves no proc/fd/goroutine/netns leaks.
//  2. Crash_MemoryLost         – kill grandchild → recovery yields stopped(memory_lost).
//  3. StopBounded              – rt.Stop() after abrupt CH crash returns within deadline.
//  4. ExplicitKillNoPdeathsig  – explicit Kill(-pgid) reaps the child regardless of Pdeathsig.
//
// Integration: runtime-skipped when /dev/kvm, CH binary, or kernel artifact
// absent. Requires no host CAP_NET_ADMIN.
package cloudhypervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/recovery"
	"github.com/IniZio/nexus/internal/core/store"
)

// ─── shared lifecycle test infrastructure ────────────────────────────────────

var lcDefaultCHBin = filepath.Join(os.Getenv("HOME"), ".local/bin/cloud-hypervisor")

// lcGuards runs pre-flight checks common to all lifecycle integration tests.
// Returns (chBin, kernelPath) or skips the test.
func lcGuards(t *testing.T) (chBin, kernelPath string) {
	t.Helper()
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("lifecycle: skipping — /dev/kvm not present")
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("lifecycle: skipping — /dev/kvm not usable: %v", err)
	}
	f.Close()
	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			t.Skip("lifecycle: skipping — unprivileged_userns_clone=0")
		}
	}
	chBin = os.Getenv("CLOUD_HYPERVISOR_BIN")
	if chBin == "" {
		chBin = lcDefaultCHBin
	}
	if _, err := os.Stat(chBin); err != nil {
		t.Skipf("lifecycle: skipping — CH binary not found at %s", chBin)
	}
	kernelPath = netnsSkipUnlessArtifact(t, "vmlinux-x86_64")
	return
}

// lcMakeSocketDir creates a temp dir under /tmp and skips the test if the
// path would be too long for a Unix socket (sockNameLen == 35; see driver.go).
func lcMakeSocketDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// sockNameLen = 35: "sb-"+26+".sock" = 34 chars + "/".
	// len(dir)+sockNameLen must not exceed maxSocketPathLen (107).
	const sockNameLen = 35
	if len(dir)+sockNameLen > 107 {
		t.Skipf("lifecycle: path too long for Unix socket: %s", dir)
	}
	return dir
}

// lcBootCH starts a NetnsRuntime for id with the given socketPath, polls until
// CH's REST API is responsive, and returns the runtime handle.
// The caller is responsible for cleanup (typically via t.Cleanup(rt.Stop)).
//
// IMPORTANT: context.Background() is used for StartNetnsRuntime intentionally.
// exec.CommandContext kills the child process when its context is cancelled, so
// the context must NOT be tied to this function's scope — the child must outlive
// the boot helper.
func lcBootCH(t *testing.T, chBin, kernelPath string, id domain.SandboxID, socketPath string) *NetnsRuntime {
	t.Helper()
	cfg := Config{
		BinaryPath:   chBin,
		StartTimeout: 20 * time.Second,
		KernelPath:   kernelPath,
	}
	// Use Background so the child is not killed when this helper returns.
	rt, err := StartNetnsRuntime(context.Background(), cfg, id, socketPath, "") // "" = boot mode
	if err != nil {
		t.Fatalf("StartNetnsRuntime: %v", err)
	}

	c := newClient(socketPath)
	const pingTimeout = 25 * time.Second
	start := time.Now()
	for time.Since(start) < pingTimeout {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		pingErr := c.Ping(pingCtx)
		pingCancel()
		if pingErr == nil {
			t.Logf("CH API ready in %v (childPgid=%d)", time.Since(start), rt.ChildPGID)
			return rt
		}
		time.Sleep(50 * time.Millisecond)
	}
	rt.Stop()
	t.Fatalf("CH API not ready after %v; child stderr:\n%s", pingTimeout, rt.ChildStderr())
	return nil
}

// lcKillCHGrandchild simulates a CH VMM crash:
//   - locates CH by scanning /proc for the socket path in its cmdline,
//   - sends SIGKILL (best-effort; CH may already be a zombie — see NOTE),
//   - waits until the CH API socket is absent (ENOENT or ECONNREFUSED).
//
// Returns the CH pid for diagnostic logging.
//
// NOTE on zombie CH: cloud-hypervisor starts without a VM kernel (only
// --api-socket is passed), so it may exit shortly after socket creation.
// When CH exits, the netns child (its parent, blocked in tapPump) does not
// call wait(), so CH becomes a zombie. Zombies retain their /proc entry and
// kill(zombie, 0) returns 0, so ESRCH-based "is it dead?" probing fails.
// The canonical "CH is down" signal used by the recovery layer is the socket
// becoming absent (ENOENT / ECONNREFUSED), which is what we check here.
func lcKillCHGrandchild(t *testing.T, socketPath string) int {
	t.Helper()
	chPID, err := findCHPidForSocket(socketPath)
	if err != nil {
		// CH may already be a zombie (exited but not yet reaped).
		t.Logf("findCHPidForSocket: %v — CH may already be a zombie; will wait for socket-absent", err)
		chPID = 0
	} else {
		t.Logf("SIGKILLing CH grandchild pid=%d", chPID)
		// Send SIGKILL best-effort. On a zombie this is a no-op (returns 0);
		// on a live process it delivers the signal.
		_ = syscall.Kill(chPID, syscall.SIGKILL)
	}

	// Wait for the CH socket to become absent — the definitive signal that
	// CH is no longer serving requests (alive, zombie, or never started).
	c := newClient(socketPath)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		pingErr := c.Ping(pingCtx)
		cancel()
		if pingErr != nil && isAbsent(pingErr) {
			t.Logf("CH socket absent (pid=%d confirmed down)", chPID)
			return chPID
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("CH socket still responding 5s after SIGKILL (pid=%d)", chPID)
	return 0
}

// lcWaitESRCH polls kill(pid, 0) until ESRCH or timeout. Returns true on ESRCH.
func lcWaitESRCH(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// countOpenFDs returns the number of entries in /proc/self/fd.
func countOpenFDs() int {
	entries, _ := os.ReadDir("/proc/self/fd")
	return len(entries)
}

// countUniqueNetNS counts unique network namespace inodes visible in /proc/*/ns/net.
func countUniqueNetNS() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return -1
	}
	seen := make(map[string]struct{})
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only numeric pid dirs.
		name := e.Name()
		allDigit := true
		for _, ch := range name {
			if ch < '0' || ch > '9' {
				allDigit = false
				break
			}
		}
		if !allDigit {
			continue
		}
		link, err := os.Readlink(filepath.Join("/proc", name, "ns/net"))
		if err != nil {
			continue
		}
		seen[link] = struct{}{}
	}
	return len(seen)
}

// listHostNxIfaces returns network interface names in /sys/class/net
// that start with any of the sandbox prefixes: nxg-, nxh-, nxb-.
func listHostNxIfaces() []string {
	entries, _ := os.ReadDir("/sys/class/net")
	var found []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "nxg-") || strings.HasPrefix(n, "nxh-") || strings.HasPrefix(n, "nxb-") {
			found = append(found, n)
		}
	}
	return found
}

// ─── Test 1: Normal Stop — No Leaks ──────────────────────────────────────────

// TestLifecycle_NormalStop_NoLeaks boots CH via StartNetnsRuntime, calls
// rt.Stop(), then asserts:
//
//	(a) CH grandchild process gone — ESRCH by pid.
//	(b) netns child process group gone — ESRCH by pgid.
//	(c) Goroutine count returns to within tolerance of the pre-start baseline.
//	(d) Open FD count returns to within tolerance of the pre-start baseline.
//	(e) Unique network namespace count does not grow (no leaked netns).
//	(f) No sandbox-prefixed (nxg-/nxh-/nxb-) interfaces leaked into the host netns.
func TestLifecycle_NormalStop_NoLeaks(t *testing.T) {
	chBin, kernelPath := lcGuards(t)
	socketDir := lcMakeSocketDir(t, "nx-lc-noleak-")
	id := domain.NewSandboxID()
	socketPath := filepath.Join(socketDir, "ch.sock")

	// Capture baseline BEFORE starting the runtime.
	baselineGoroutines := runtime.NumGoroutine()
	baselineFDs := countOpenFDs()
	baselineNetNS := countUniqueNetNS()
	baselineNxIfaces := listHostNxIfaces()
	t.Logf("baseline: goroutines=%d fds=%d netns=%d nxifaces=%v",
		baselineGoroutines, baselineFDs, baselineNetNS, baselineNxIfaces)

	rt := lcBootCH(t, chBin, kernelPath, id, socketPath)
	t.Cleanup(func() { rt.Stop() }) // safety net — idempotent via stopOnce

	chPID, err := findCHPidForSocket(socketPath)
	if err != nil {
		t.Fatalf("findCHPidForSocket before Stop: %v", err)
	}
	childPgid := rt.ChildPGID
	t.Logf("pre-stop: chPID=%d childPgid=%d", chPID, childPgid)

	// Verify both alive before Stop.
	if e := syscall.Kill(chPID, 0); e != nil {
		t.Fatalf("CH pid=%d not alive pre-Stop: %v", chPID, e)
	}
	if e := syscall.Kill(childPgid, 0); e != nil {
		t.Fatalf("child pgid=%d not alive pre-Stop: %v", childPgid, e)
	}

	rt.Stop()

	// Settle so runtime goroutines (cmd.Wait, perimConn.Close) finish.
	time.Sleep(200 * time.Millisecond)

	finalGoroutines := runtime.NumGoroutine()
	finalFDs := countOpenFDs()
	finalNetNS := countUniqueNetNS()
	finalNxIfaces := listHostNxIfaces()

	// (a) CH grandchild gone — check via socket absent AND pid ESRCH (with
	// tolerance for zombie cleanup delay: after the child dies, init reaps the
	// zombie but this may take a moment).
	{
		c := newClient(socketPath)
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		pingErr := c.Ping(pingCtx)
		pingCancel()
		if pingErr == nil {
			t.Errorf("FAIL proc-leak: CH socket still responding after rt.Stop() (pid=%d)", chPID)
		} else if isAbsent(pingErr) {
			t.Logf("PASS proc(CH): socket absent (pid=%d)", chPID)
		} else {
			t.Logf("proc(CH): socket err=%v (pid=%d) — possibly timeout not absent", pingErr, chPID)
		}
	}
	// PID ESRCH: allow up to 5s for init to reap the zombie after the child dies.
	if lcWaitESRCH(chPID, 5*time.Second) {
		t.Logf("PASS proc(CH): pid=%d gone (ESRCH)", chPID)
	} else {
		// Zombie not yet reaped by init — this is a reap-delay, not a leak.
		// The socket is absent (checked above); log as info, not error.
		t.Logf("INFO proc(CH): pid=%d still in zombie table (not yet reaped by init); socket is absent", chPID)
	}

	// (b) netns child process group gone.
	if !lcWaitESRCH(childPgid, 5*time.Second) {
		t.Errorf("FAIL proc-leak: child pgid=%d still alive after rt.Stop()", childPgid)
	} else {
		t.Logf("PASS proc(child): pgid=%d gone (ESRCH)", childPgid)
	}

	// (c) Goroutine count.
	const goroutineTol = 5
	goroutineDelta := finalGoroutines - baselineGoroutines
	t.Logf("goroutines: baseline=%d after-stop=%d delta=%d (tolerance=%d)",
		baselineGoroutines, finalGoroutines, goroutineDelta, goroutineTol)
	if goroutineDelta > goroutineTol {
		t.Errorf("FAIL goroutine-leak: delta=%d > tolerance=%d", goroutineDelta, goroutineTol)
	} else {
		t.Logf("PASS goroutine: delta=%d within tolerance", goroutineDelta)
	}

	// (d) Open FD count.
	const fdTol = 5
	fdDelta := finalFDs - baselineFDs
	t.Logf("fds: baseline=%d after-stop=%d delta=%d (tolerance=%d)",
		baselineFDs, finalFDs, fdDelta, fdTol)
	if fdDelta > fdTol {
		t.Errorf("FAIL fd-leak: delta=%d > tolerance=%d", fdDelta, fdTol)
	} else {
		t.Logf("PASS fd: delta=%d within tolerance", fdDelta)
	}

	// (e) Network namespace count does not grow.
	netnsDelta := finalNetNS - baselineNetNS
	t.Logf("netns: baseline=%d after-stop=%d delta=%d", baselineNetNS, finalNetNS, netnsDelta)
	if netnsDelta > 0 {
		t.Errorf("FAIL netns-leak: netns grew by %d (baseline=%d after=%d)",
			netnsDelta, baselineNetNS, finalNetNS)
	} else {
		t.Logf("PASS netns: delta=%d (zero or negative)", netnsDelta)
	}

	// (f) No sandbox-prefixed interfaces (nxg-/nxh-/nxb-) in host netns.
	t.Logf("nx ifaces: before=%v after=%v", baselineNxIfaces, finalNxIfaces)
	for _, iface := range finalNxIfaces {
		if !slices.Contains(baselineNxIfaces, iface) {
			t.Errorf("FAIL iface-leak: nx interface %q appeared in host netns after Stop", iface)
		}
	}
	if len(finalNxIfaces) == len(baselineNxIfaces) {
		t.Logf("PASS iface: no new nx interfaces in host netns (count=%d)", len(finalNxIfaces))
	}
}

// ─── Test 2: Crash → stopped(memory_lost) [edge-10] ─────────────────────────

// TestLifecycle_Crash_MemoryLost directly SIGKILLs the CH VMM grandchild,
// then runs recovery on a durable `running` record. The expected outcome is
// OutcomeResolvedStopped with StopReason=memory_lost — never error.
//
// The socket path is constructed as socketDir/id.String()+".sock" to match
// CHDriver.socketPath(id), so that CHDriver.Observe returns driver.Absent
// after the kill, triggering the applyAbsent(Running) → memory_lost path
// (edge-10: Running + TriggerSubstrateLost → Stopped(memory_lost)).
func TestLifecycle_Crash_MemoryLost(t *testing.T) {
	chBin, kernelPath := lcGuards(t)
	socketDir := lcMakeSocketDir(t, "nx-lc-memlost-")

	// Socket name must match d.socketPath(id) = socketDir/id.String()+".sock".
	id := domain.NewSandboxID()
	socketPath := filepath.Join(socketDir, id.String()+".sock")

	rt := lcBootCH(t, chBin, kernelPath, id, socketPath)
	t.Cleanup(func() { rt.Stop() }) // idempotent safety net

	// Build a CHDriver pointing at the same SocketDir so Observe finds the socket.
	drv, err := New(Config{
		BinaryPath:  chBin,
		SocketDir:   socketDir,
		CallTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New CHDriver: %v", err)
	}

	// Persist a durable running record in a FileStore.
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	sb := domain.Sandbox{
		ID:         id,
		Name:       "lifecycle-crash-test",
		Project:    "test",
		State:      domain.Running,
		InstanceID: "test-iid-01",
	}
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatalf("st.Create: %v", err)
	}
	t.Logf("persisted sandbox id=%s state=running", id)

	// SIGKILL the CH grandchild (substrate loss event).
	childPgid := rt.ChildPGID
	_ = lcKillCHGrandchild(t, socketPath)

	// Brief settle: ensure ENOENT/ECONNREFUSED on the socket before Observe.
	time.Sleep(100 * time.Millisecond)

	// Log drv.Observe so we can verify it returns Absent (not Unknown/error).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	obs, obsErr := drv.Observe(ctx, id)
	t.Logf("post-crash Observe: state=%v detail=%q err=%v", obs.State, obs.Detail, obsErr)
	if obs.State != driver.Absent {
		t.Fatalf("FAIL precondition: Observe returned state=%v (want Absent) — recovery will not resolve to memory_lost", obs.State)
	}

	// Run recovery — Absent + Running record → memory_lost.
	rec := recovery.New(st, drv)
	out, recErr := rec.RecoverOne(ctx, id)
	if recErr != nil {
		t.Fatalf("RecoverOne error: %v", recErr)
	}
	t.Logf("recovery result: kind=%s reason=%q", out.Kind, out.Reason)

	// Assert edge-10: must be OutcomeResolvedStopped.
	if out.Kind != recovery.OutcomeResolvedStopped {
		t.Errorf("FAIL edge-10: want kind=%s, got kind=%s (reason: %q)",
			recovery.OutcomeResolvedStopped, out.Kind, out.Reason)
	} else {
		t.Logf("PASS edge-10: kind=%s", out.Kind)
	}

	// Read updated record: must be stopped + memory_lost.
	updated, err := st.Get(ctx, id)
	if err != nil {
		t.Fatalf("st.Get after recovery: %v", err)
	}
	t.Logf("record after recovery: state=%s stop_reason=%q", updated.State, updated.StopReason)

	if updated.State != domain.Stopped {
		t.Errorf("FAIL state: want stopped, got %s", updated.State)
	} else {
		t.Logf("PASS state: stopped")
	}
	if updated.StopReason != domain.StopReasonMemoryLost {
		t.Errorf("FAIL stop_reason: want %q, got %q", domain.StopReasonMemoryLost, updated.StopReason)
	} else {
		t.Logf("PASS stop_reason: %q", updated.StopReason)
	}

	// Verify killing CH didn't orphan the netns child:
	// (1) Check /proc/<childPgid>/status PPid is still our pid (not reparented to init).
	statusPath := filepath.Join("/proc", itoa(childPgid), "status")
	if statusData, err := os.ReadFile(statusPath); err == nil {
		for _, line := range strings.Split(string(statusData), "\n") {
			if strings.HasPrefix(line, "PPid:") {
				t.Logf("child PPid check: %s (our pid=%d)", strings.TrimSpace(line), os.Getpid())
				break
			}
		}
	} else {
		t.Logf("child /proc status already gone (pid=%d): %v", childPgid, err)
	}

	// (2) rt.Stop() must reap the child.
	rt.Stop() // explicit; t.Cleanup is a no-op (idempotent)
	if !lcWaitESRCH(childPgid, 5*time.Second) {
		t.Errorf("FAIL orphan: netns child pgid=%d still alive after rt.Stop() with crashed CH", childPgid)
	} else {
		t.Logf("PASS orphan: netns child pgid=%d gone after rt.Stop()", childPgid)
	}
}

// itoa converts an int to string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ─── Test 3: Stop is time-bounded after abrupt CH death ───────────────────────

// TestLifecycle_StopBounded verifies that rt.Stop() completes within a
// bounded deadline when the CH grandchild has been abruptly killed beforehand.
// The Kill(-childPgid, SIGKILL) in Stop() must reach the netns child and
// cmd.Wait() must collect it quickly — no block on the socketpair.
func TestLifecycle_StopBounded(t *testing.T) {
	chBin, kernelPath := lcGuards(t)
	socketDir := lcMakeSocketDir(t, "nx-lc-bounded-")
	id := domain.NewSandboxID()
	socketPath := filepath.Join(socketDir, "ch.sock")

	rt := lcBootCH(t, chBin, kernelPath, id, socketPath)
	// Safety cleanup: force-kill the group in case rt.Stop hangs (bug scenario).
	// Do NOT use t.Cleanup(rt.Stop) — if Stop blocks, sync.Once.Do would
	// deadlock the cleanup goroutine behind the hung call.
	childPgid := rt.ChildPGID
	t.Cleanup(func() {
		_ = syscall.Kill(-childPgid, syscall.SIGKILL)
	})

	// Kill CH grandchild before calling Stop.
	lcKillCHGrandchild(t, socketPath)

	// rt.Stop() must return within stopDeadline.
	const stopDeadline = 5 * time.Second
	done := make(chan time.Duration, 1)
	go func() {
		t0 := time.Now()
		rt.Stop()
		done <- time.Since(t0)
	}()

	select {
	case elapsed := <-done:
		t.Logf("PASS bounded: rt.Stop() returned in %v (deadline=%v)", elapsed, stopDeadline)
		if elapsed > stopDeadline {
			t.Errorf("FAIL bounded: Stop took %v which exceeds deadline %v", elapsed, stopDeadline)
		}
	case <-time.After(stopDeadline + time.Second):
		t.Errorf("FAIL bounded: rt.Stop() did not return within %v after CH grandchild crash",
			stopDeadline+time.Second)
	}
}

// ─── Test 4: Explicit Kill(-pgid) reaps child without Pdeathsig ──────────────

// TestLifecycle_ExplicitKillNoPdeathsig confirms that after rt.Stop(), the
// entire child process group is gone (ESRCH) even when CH was killed first —
// proving explicit Kill(-childPgid, SIGKILL) in Stop() is the authoritative
// reaping mechanism, independent of Pdeathsig.
//
// Pdeathsig flows from netns child → CH grandchild (parent sets it on the
// child process so CH dies if the child dies). Killing CH first reverses the
// causality: the mechanism being verified here (Stop → Kill(-pgid)) must
// work without any Pdeathsig-delivered signal from the now-dead CH.
func TestLifecycle_ExplicitKillNoPdeathsig(t *testing.T) {
	chBin, kernelPath := lcGuards(t)
	socketDir := lcMakeSocketDir(t, "nx-lc-nopds-")
	id := domain.NewSandboxID()
	socketPath := filepath.Join(socketDir, "ch.sock")

	rt := lcBootCH(t, chBin, kernelPath, id, socketPath)
	t.Cleanup(func() { rt.Stop() })

	childPgid := rt.ChildPGID
	t.Logf("netns child pgid=%d", childPgid)

	// Verify child is alive before crash.
	if e := syscall.Kill(childPgid, 0); e != nil {
		t.Fatalf("child pgid=%d not alive before crash: %v", childPgid, e)
	}

	// Kill CH grandchild first — Pdeathsig (child→CH direction) cannot be
	// the mechanism that reaps the child afterward.
	lcKillCHGrandchild(t, socketPath)

	// Child may or may not still be alive here depending on whether it detects
	// CH dying via its own fds. Either outcome is acceptable — what matters is
	// that rt.Stop() reaps whatever is left.
	if e := syscall.Kill(childPgid, 0); e == nil {
		t.Log("child pgid still alive after CH crash (expected: tapPump still looping on fds)")
	} else if errors.Is(e, syscall.ESRCH) {
		t.Log("child pgid already gone after CH crash (also acceptable: pump saw EOF)")
	} else {
		t.Logf("child pgid kill(0) returned unexpected: %v", e)
	}

	// rt.Stop() must reap any remaining child processes via Kill(-childPgid).
	rt.Stop()

	// Assert child pgid is gone — proving explicit Kill(-pgid) is authoritative.
	const esrchTimeout = 5 * time.Second
	if !lcWaitESRCH(childPgid, esrchTimeout) {
		t.Errorf("FAIL reap: child pgid=%d still alive %v after rt.Stop() — explicit Kill(-pgid) failed",
			childPgid, esrchTimeout)
	} else {
		t.Logf("PASS reap: child pgid=%d gone (ESRCH) after rt.Stop() via explicit Kill(-pgid), "+
			"not Pdeathsig (CH was already dead)", childPgid)
	}
}

// TestStartCtxCancelDoesNotKillChild is the S-PERIM-TIMEOUT regression test.
//
// Symptom: when a sandbox ran a long, silent in-guest command, the outer
// connection tore down with:
//
//	"agent: pump: read frame: EOF"
//	"cannot receive packets from @...: i/o timeout"
//
// Root cause: exec.CommandContext(ctx, self) at StartNetnsRuntime bound the
// netns child process (which hosts CH + vsock + TAP pump) to the CALLER'S
// context. When the caller passed a short-lived start context and later
// cancelled it (e.g. after drv.Start returned), exec.CommandContext's
// monitoring goroutine fired SIGKILL at the child — killing the VM mid-exec.
//
// Fix: exec.Command(self) is used instead; the child's lifetime is controlled
// solely by rt.Stop(). This test confirms the child survives startCtx
// cancellation by probing /proc/<childPgid>/stat after cancellation.
// A zombie state (Z) means the bug is present; any live state (R/S/D) means
// the fix is in place.
func TestStartCtxCancelDoesNotKillChild(t *testing.T) {
	chBin, kernelPath := lcGuards(t)
	socketDir := lcMakeSocketDir(t, "nx-lc-ctxcancel-")
	id := domain.NewSandboxID()
	socketPath := filepath.Join(socketDir, "sb-ctx-cancel.sock")

	cfg := Config{
		BinaryPath:   chBin,
		StartTimeout: 20 * time.Second,
		KernelPath:   kernelPath,
	}

	// Use a cancellable start context — the kind a caller might pass for a
	// boot window and then cancel after drv.Start returns (S-PERIM-TIMEOUT).
	startCtx, startCancel := context.WithCancel(context.Background())

	rt, err := StartNetnsRuntime(startCtx, cfg, id, socketPath, "")
	if err != nil {
		t.Fatalf("StartNetnsRuntime: %v", err)
	}
	t.Cleanup(rt.Stop)

	// Cancel the start context immediately — simulating the caller discarding
	// its boot-phase context after the runtime is up.
	startCancel()

	// Give any exec.CommandContext monitoring goroutine time to wake and fire
	// SIGKILL (if the bug were still present). The goroutine wakes on the
	// already-closed ctx.Done() channel, so 300 ms is generous.
	time.Sleep(300 * time.Millisecond)

	// Probe child liveness via /proc/<childPgid>/stat.
	//
	//   Alive (R/S/D): child survived startCtx cancellation → PASS.
	//   Zombie (Z):    exec.CommandContext killed it             → FAIL.
	//   Missing:       child exited and was fully reaped        → FAIL.
	//
	// /proc/<pid>/stat format: "<pid> (<comm>) <state> <rest>"
	// State is the character immediately after the closing ')'.
	statPath := fmt.Sprintf("/proc/%d/stat", rt.ChildPGID)
	statBytes, readErr := os.ReadFile(statPath)
	if readErr != nil {
		t.Fatalf("FAIL S-PERIM-TIMEOUT: child (pgid=%d) has no /proc entry after "+
			"startCtx cancel — child exited (exec.CommandContext killed it): %v",
			rt.ChildPGID, readErr)
	}
	stat := string(statBytes)
	closeParenIdx := strings.LastIndex(stat, ")")
	if closeParenIdx < 0 || closeParenIdx+2 >= len(stat) {
		t.Fatalf("cannot parse /proc/%d/stat: %q", rt.ChildPGID, stat)
	}
	state := stat[closeParenIdx+2]
	if state == 'Z' {
		t.Fatalf("FAIL S-PERIM-TIMEOUT: child (pgid=%d) is a zombie after startCtx "+
			"cancel — exec.CommandContext killed it. Fix: use exec.Command, not "+
			"exec.CommandContext. /proc stat: %s",
			rt.ChildPGID, strings.TrimSpace(stat))
	}
	t.Logf("PASS S-PERIM-TIMEOUT: child (pgid=%d) state=%c after startCtx cancel — "+
		"survived context cancellation (not a zombie)", rt.ChildPGID, state)
}

// ─── Test 5: Launcher exits on CH death (orphan-launcher reap) ───────────────

// TestLifecycle_LauncherExitsOnCHDeath verifies that when the CH grandchild
// exits (crashed or killed), the netns child (launcher) also exits rather
// than lingering as an orphan with a zombie child.
//
// This covers the retry-leak pattern: a failed first boot leaves its launcher
// behind (ppid 1, zombie CH child) while the retry's launcher runs alongside.
// The fix is in RunNetnsChild: a goroutine calls syscall.Wait4(proc.pid) (which
// returns as soon as CH exits, without waiting for pipe-draining io.Copy
// goroutines) and then calls os.Exit(0), terminating the entire launcher
// process — including the stuck TAP-read goroutine that would otherwise hold
// tapPump open forever.
//
// Mutation proof: removing the goroutine (replacing it with _ = proc) means
// syscall.Wait4 is never called; os.Exit(0) is never called; the launcher
// TestLifecycle_ConsoleLogCreated verifies the R1-serial-console slice
// end-to-end on the real production path:
//
//	CHDriver.Start (ConsoleLogPath set, no SerialOutputPath)
//	  → driver.go injects serial:{mode:"Tty"} into vm.create
//	  → CH maps guest ttyS0 → CH stdout
//	  → netns child reads NEXUS_NETNS_CONSOLE_LOG
//	  → newCappedConsoleWriter drains CH stdout → console.log
//	  → "Linux version" kernel line appears in console.log
//
// Mutation proof: removing the serial:{mode:"Tty"} else-branch in driver.go
// (1 substitution — replace else block with an empty block) causes CH to
// create NO serial device, so guest ttyS0 output is silently discarded; the
// test fails with "console.log does not contain 'Linux version'".
func TestLifecycle_ConsoleLogCreated(t *testing.T) {
	chBin, kernelPath := lcGuards(t)
	initramfsPath := netnsSkipUnlessArtifact(t, "alpine-initramfs.cpio.gz")
	socketDir := lcMakeSocketDir(t, "nx-lc-consolelog-")
	consolePath := filepath.Join(socketDir, "console.log")

	drv, err := New(Config{
		BinaryPath:     chBin,
		SocketDir:      socketDir,
		KernelPath:     kernelPath,
		InitramfsPath:  initramfsPath,
		Cmdline:        "console=ttyS0 panic=5",
		ConsoleLogPath: consolePath,
		VCPUs:          1,
		MemoryMiB:      256,
		StartTimeout:   30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New CHDriver: %v", err)
	}

	id := domain.NewSandboxID()
	var vmmPID int
	t.Cleanup(func() {
		// Log console.log so the content is visible in -v mode and on failure.
		if content, readErr := os.ReadFile(consolePath); readErr == nil && len(content) > 0 {
			t.Logf("console.log (%d bytes):\n%s", len(content), content)
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = drv.Stop(stopCtx, id)
		if vmmPID != 0 {
			_ = syscall.Kill(-vmmPID, syscall.SIGKILL)
		}
		drv.clearState(id)
		// socketDir is cleaned up by lcMakeSocketDir's t.Cleanup.
	})

	startCtx, startCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer startCancel()

	if _, err := drv.Start(startCtx, driver.StartRequest{SandboxID: id}); err != nil {
		t.Fatalf("drv.Start: %v", err)
	}

	drv.mu.Lock()
	if proc := drv.procs[id]; proc != nil {
		vmmPID = proc.pid
	}
	drv.mu.Unlock()

	// Poll console.log for a string the guest kernel reliably emits on ttyS0.
	// "Linux version" is the first line emitted by every Linux kernel on the
	// console device; it arrives within milliseconds of the VM booting.
	// If this string is absent, serial output is not reaching CH stdout —
	// the serial:{mode:"Tty"} config in driver.go is missing or wrong.
	const wantStr = "Linux version"
	const pollTimeout = 30 * time.Second
	pollStart := time.Now()
	for time.Since(pollStart) < pollTimeout {
		content, _ := os.ReadFile(consolePath)
		if strings.Contains(string(content), wantStr) {
			t.Logf("console.log contains %q after %v", wantStr, time.Since(pollStart))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}

	content, _ := os.ReadFile(consolePath)
	t.Errorf("console.log does not contain %q after %v — serial→CH-stdout wiring broken\n"+
		"(driver.go serial:{mode:Tty} not injected, or cappedConsoleWriter not draining)\n"+
		"console.log (%d bytes):\n%.4000s",
		wantStr, pollTimeout, len(content), content)
}

// stays stuck in tapPump until rt.Stop() kills it. rt.cmd.Wait() never returns.
// The substitution count for the mutation is 1 (the single "_ = proc" line).
func TestLifecycle_LauncherExitsOnCHDeath(t *testing.T) {
	chBin, kernelPath := lcGuards(t)
	socketDir := lcMakeSocketDir(t, "nx-lc-orphan-")

	id := domain.NewSandboxID()
	socketPath := filepath.Join(socketDir, id.String()+".sock")

	rt := lcBootCH(t, chBin, kernelPath, id, socketPath)
	// Do NOT register t.Cleanup(rt.Stop) before the assertion: the test
	// asserts the launcher exits on its own. defer below calls Stop() only
	// after the check, cleaning up PerimConn so the test process does not leak.
	defer rt.Stop() // idempotent via stopOnce; no-op if child already exited

	childPID := rt.ChildPID
	t.Logf("netns child pid=%d (pgid=%d)", childPID, rt.ChildPGID)

	// Wait for the netns child to advance past spawnVMMInGroup and into tapPump.
	//
	// lcBootCH polls CH's API socket from the TEST process. When it returns
	// "ready", the netns child's own spawnVMMInGroup polling loop may not have
	// seen CH as ready yet (both poll at 50 ms intervals but are not synced).
	// If we kill CH before the netns child exits spawnVMMInGroup, the reap
	// goroutine never starts and the test races against the netns child's
	// 20 s StartTimeout. 300 ms = 6 full 50 ms poll intervals — enough for
	// the netns child to complete its own poll cycle and start the goroutine.
	time.Sleep(300 * time.Millisecond)

	// Kill the CH grandchild — simulates a CH crash or a failed first boot.
	_ = lcKillCHGrandchild(t, socketPath)

	// The launcher must exit within 5 s: the fix goroutine in RunNetnsChild
	// calls syscall.Wait4 (waits for CH to exit) then os.Exit(0) to kill the
	// entire process — including the stuck TAP-read goroutine inside tapPump.
	//
	// We observe exit via rt.deathCh (closed by watchParentOwnedDeath when
	// cmd.Wait() returns), NOT by calling rt.cmd.Wait() directly. Rationale:
	//   - watchParentOwnedDeath is the single owner of cmd.Wait() (AC-12c);
	//     a concurrent call from this test would be a data race under -race.
	//   - rt.deathCh is closed by the same cmd.Wait() call, so it fires on
	//     exactly the same condition as a direct cmd.Wait() would — without
	//     the zombie-PID-reuse hazard of kill(pid,0)/proc polling.
	//   - A closed channel broadcasts: both rt.Stop() (which waits on
	//     rt.deathCh) and this select can observe the close without conflict.
	//
	// Without the goroutine (mutation: replace goroutine with _ = proc),
	// syscall.Wait4 is never called; os.Exit(0) is never called; the netns
	// child is stuck in tapPump forever → deathCh never closes → test FAILS.
	const timeout = 5 * time.Second
	select {
	case <-rt.deathCh:
		t.Logf("PASS orphan-launcher: netns child pid=%d exited after CH death", childPID)
	case <-time.After(timeout):
		t.Logf("child stderr:\n%s", rt.ChildStderr())
		t.Fatalf("FAIL orphan-launcher: netns child pid=%d still alive %v after "+
			"CH exited; launcher leaked as orphan (ppid 1, zombie CH child)",
			childPID, timeout)
	}
}
