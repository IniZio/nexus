package service_test

// Tests for the netns-process sweep added to Reap (ticket 10:
// "create-path orphan leak and blind reap").
//
// Ticket 10's core finding: Reap's main loop enumerates ResourceIndex.List()
// — a filesystem enumeration — and asks "is the owner of this FILE still
// alive?". That direction cannot see a process whose files were already
// removed by the create path's own (possibly incomplete) cleanup. These
// tests drive the REAL service.Reap function via its ProcDir injection seam
// (the same seam TestReap_ConcurrentCreateInFlight above uses for cmdline
// scanning) with a synthetic /proc tree standing in for a live netns-child
// process — never a reimplementation of the detection logic under test.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/service"
)

// spawnSleeperForTest starts a real, short-lived "sleep" child process as its
// own process-group leader (Setpgid:true — mirroring StartNetnsRuntime's
// netnsChildAttr), so killNetnsProcessFn's real Kill(-pgid, SIGKILL) can be
// exercised against a real process without any risk of reaching the test
// binary's own process group.
func spawnSleeperForTest(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleeper: %v", err)
	}
	// A real orphan (reparented to init) is auto-reaped by the kernel's
	// subreaper the instant it exits, so kill(pid, 0) reliably reports ESRCH
	// once dead. Here the test process itself is the parent, so nothing
	// reaps the zombie unless we Wait() on it — do that in the background so
	// killNetnsProcessFn's SIGKILL is promptly followed by a reap, matching
	// the real-world timing this test's assertions depend on.
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waited
	})
	return cmd
}

// writeSyntheticStat writes a synthetic /proc/<pid>/stat file whose pgid
// field (field 5) reports pgid, matching the format readProcPGID parses.
func writeSyntheticStat(t *testing.T, procDir string, pid, pgid int) {
	t.Helper()
	pidDir := filepath.Join(procDir, itoa(pid))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("%d (sleep) S 1 %d 0 0 -1\n", pid, pgid)
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitForProcessExit polls kill(pid, 0) until it reports ESRCH or a timeout
// elapses, returning the last error (nil means the process is still alive).
func waitForProcessExit(pid int) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process %d still alive after deadline", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// writeSyntheticNetnsProcess creates a synthetic /proc/<pid>/environ file
// carrying the netns-child sentinel env vars StartNetnsRuntime sets on every
// real netns child (ch_netns.go:227-233): NEXUS3_NETNS_RUN=1 and
// NEXUS3_NETNS_API_SOCKET=<apiSocket>.
func writeSyntheticNetnsProcess(t *testing.T, procDir string, pid int, apiSocket string) {
	t.Helper()
	pidDir := filepath.Join(procDir, itoa(pid))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	environ := "NEXUS3_NETNS_RUN=1\x00NEXUS3_NETNS_API_SOCKET=" + apiSocket + "\x00PATH=/usr/bin\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "environ"), []byte(environ), 0o600); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestReap_OrphanedNetnsChild_NoRecordNoIntent verifies the ticket's headline
// scenario: a live netns child + CH process pair that no store record and no
// in-flight create-intent names at all (its own resource files — disk,
// socket — were already removed by a prior create's cleanup). Reap must
// REPORT it as an orphan via the REAL service.Reap function; before this
// ticket's fix it was invisible ("No orphaned resources found" while the
// pair kept running — the exact defect this ticket investigates).
//
// @verifies ticket-10 defect 2 (reap blindness)
func TestReap_OrphanedNetnsChild_NoRecordNoIntent(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	// No sandbox record, no create-intent — this id is claimed by NOTHING.
	orphanID := domain.NewSandboxID()
	orphanSocket := filepath.Join(sockDir, orphanID.String()+".sock")

	const orphanPID = 424242
	writeSyntheticNetnsProcess(t, procDir, orphanPID, orphanSocket)

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, false /*dry-run*/, service.ReapOptions{ProcDir: procDir})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	var found *service.ReapEntry
	for i := range report.Entries {
		if report.Entries[i].Resource.Kind == service.KindNetnsProcess && report.Entries[i].ProcessPID == orphanPID {
			found = &report.Entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("orphaned netns-child process (pid %d) not found in report.Entries at all — reap is blind to it (the ticket-10 defect); entries: %+v", orphanPID, report.Entries)
	}
	if found.Status != service.ReapStatusOrphan {
		t.Errorf("orphaned netns-child status = %q, want %q", found.Status, service.ReapStatusOrphan)
	}
	if found.ProcessPID != orphanPID {
		t.Errorf("ProcessPID = %d, want %d", found.ProcessPID, orphanPID)
	}
}

// TestReap_LiveNetnsChild_MatchingRecord_NotReported verifies the sweep does
// not false-positive on a sandbox's own live netns child: when a record's
// CHAPISocket matches the process's own reported api socket, the process is
// expected and must not appear as an orphan.
func TestReap_LiveNetnsChild_MatchingRecord_NotReported(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	id := domain.NewSandboxID()
	apiSocket := filepath.Join(sockDir, id.String()+".sock")

	const pid = 500001
	writeSyntheticNetnsProcess(t, procDir, pid, apiSocket)

	st := newEmptyStore(t)
	ctx := context.Background()
	if err := st.Create(ctx, domain.Sandbox{
		ID:          id,
		Name:        "n",
		Project:     "p",
		State:       domain.Running,
		CHAPISocket: apiSocket,
	}); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	report, err := service.Reap(ctx, st, idx, false, service.ReapOptions{ProcDir: procDir})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for _, e := range report.Entries {
		if e.Resource.Kind == service.KindNetnsProcess && e.ProcessPID == pid {
			t.Fatalf("live netns child matching its own record's CHAPISocket was reported as %s — false positive: %+v", e.Status, e)
		}
	}
}

// TestReap_NetnsChild_RecordSocketMismatch_ReportsSuspect verifies the
// fail-closed rail: a live netns child whose api socket names a sandbox ID
// that DOES have a record, but that record's CHAPISocket does not match what
// the process is actually using (e.g. never backfilled), must be reported as
// SUSPECT — not silently folded into "clean" and not asserted as a confident
// orphan (reap cannot rule out that this is the sandbox's own legitimate
// process).
//
// @verifies ticket-10 fail-closed rail
func TestReap_NetnsChild_RecordSocketMismatch_ReportsSuspect(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	id := domain.NewSandboxID()
	liveSocket := filepath.Join(sockDir, id.String()+".sock")

	const pid = 500002
	writeSyntheticNetnsProcess(t, procDir, pid, liveSocket)

	st := newEmptyStore(t)
	ctx := context.Background()
	// Record exists but its CHAPISocket disagrees with what the live process
	// reports (e.g. a pre-slice-04 record whose identity was never backfilled).
	if err := st.Create(ctx, domain.Sandbox{
		ID:      id,
		Name:    "n",
		Project: "p",
		State:   domain.Running,
		// CHAPISocket intentionally left empty.
	}); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	report, err := service.Reap(ctx, st, idx, false, service.ReapOptions{ProcDir: procDir})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	var found *service.ReapEntry
	for i := range report.Entries {
		if report.Entries[i].Resource.Kind == service.KindNetnsProcess {
			found = &report.Entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("mismatched netns child not reported at all — fail-closed rail violated (must be SUSPECT, not silently clean)")
	}
	if found.Status != service.ReapStatusSuspect {
		t.Errorf("status = %q, want %q (fail-closed: cannot verify ownership)", found.Status, service.ReapStatusSuspect)
	}
}

// TestReap_ApplyKillsOrphanedNetnsChild verifies that --apply reclaims a
// netns-process orphan by killing its process group, using the
// killNetnsProcessFn seam's real production wiring path (not a stub) against
// a REAL spawned process, so the assertion is that the process is actually
// dead afterward — not merely that Reap reported it.
func TestReap_ApplyKillsOrphanedNetnsChild(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	cmd := spawnSleeperForTest(t)
	pid := cmd.Process.Pid

	orphanID := domain.NewSandboxID()
	orphanSocket := filepath.Join(sockDir, orphanID.String()+".sock")
	writeSyntheticNetnsProcess(t, procDir, pid, orphanSocket)
	// Real pgid: our spawned process is its own group leader.
	writeSyntheticStat(t, procDir, pid, pid)

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{
		ProcDir: procDir,
		// NetnsKillFn is required when ProcDir is synthetic (not "/proc"); we
		// pass a real kill wrapper here because this test's whole point is to
		// prove the process actually dies — not merely that Reap reports it.
		NetnsKillFn: func(pgid int) error {
			if pgid <= 0 {
				return fmt.Errorf("reap test: refusing to kill non-positive pgid %d", pgid)
			}
			if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
				return fmt.Errorf("reap test: kill process group %d: %w", pgid, err)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(report.KilledPIDs) == 0 {
		t.Fatalf("report.KilledPIDs is empty; expected pid %d to be killed", pid)
	}

	if err := waitForProcessExit(pid); err != nil {
		t.Errorf("process %d still alive after --apply: %v", pid, err)
	}
}

// TestReap_SyntheticProcDir_RequiresNetnsKillFn verifies the structural guard
// that prevents a test from accidentally reaching the real syscall.Kill via a
// pgid discovered from a synthetic /proc entry.
//
// Hazard shape: a test sets ProcDir to a temp dir, plants a directory named
// after a live host pid, and calls Reap with apply=true but no NetnsKillFn.
// Without this guard the package-level killNetnsProcessFn (real syscall.Kill)
// would send SIGKILL to that process group. The guard makes the hazard
// structurally unreachable: any apply attempt against a synthetic-ProcDir
// orphan must supply NetnsKillFn, so the real Kill is never reached by accident.
//
// The test uses a SPAWNED child (not os.Getpid()) so that if the guard is
// accidentally removed during mutation testing the real Kill hits only this
// test's own child — not the test binary or a live developer sandbox.
//
// Mutation proof: remove the "ProcDir != /proc && NetnsKillFn == nil" guard
// in Reap (reap.go, the line `if opt.ProcDir != "/proc" && opt.NetnsKillFn == nil`)
// → Reap succeeds (returns nil error, kills the child) → this test goes RED
// at the `err == nil` check. Restore → GREEN.
func TestReap_SyntheticProcDir_RequiresNetnsKillFn(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	// Spawn a real child so the guard has a genuine orphan to evaluate.
	// If the guard is removed, the real killNetnsProcessFn sends SIGKILL to
	// this child — which is safe because it belongs to this test.
	cmd := spawnSleeperForTest(t)
	pid := cmd.Process.Pid

	orphanID := domain.NewSandboxID()
	orphanSocket := filepath.Join(sockDir, orphanID.String()+".sock")
	writeSyntheticNetnsProcess(t, procDir, pid, orphanSocket)
	writeSyntheticStat(t, procDir, pid, pid)

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	ctx := context.Background()
	// Deliberately omit NetnsKillFn — the guard must reject this.
	_, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{
		ProcDir: procDir,
		// NetnsKillFn intentionally absent — that is the unsafe shape being guarded.
	})
	if err == nil {
		t.Fatal("Reap succeeded with synthetic ProcDir and no NetnsKillFn; guard must refuse to prevent real syscall.Kill on an unknown pid")
	}
	if !strings.Contains(err.Error(), "NetnsKillFn") {
		t.Errorf("guard error %q does not mention NetnsKillFn (wrong guard fired?)", err)
	}

	// The child must still be alive — the guard blocked the kill.
	if pgErr := cmd.Process.Signal(syscall.Signal(0)); pgErr != nil {
		t.Errorf("child pid %d is dead after guarded Reap; guard did not prevent the kill", pid)
	}
}

// TestReap_ForeignUidProcess_ProducesNoEntry verifies the uid-gate added in Fix 1:
// a process owned by a DIFFERENT uid than os.Geteuid() must produce NO entry at
// all — not Orphan, not Suspect, just silently skipped.
//
// Rationale: StartNetnsRuntime spawns the netns child as the SAME uid as the
// calling process, so a foreign-uid process cannot be a child we spawned.
// EACCES is positive evidence of non-ownership, not ambiguity. Emitting a
// Suspect for every foreign-uid process would produce hundreds of bogus entries
// on any normal multi-user host and destroy the "0 suspects → exit 0" contract.
//
// Since setting a foreign uid on a filesystem directory requires root, we inject
// the ownership lookup via ReapOptions.ProcOwnerLookup. This seam mirrors how
// ProcDir is already injectable and lets the test make the synthetic dir appear
// to be owned by a different user without elevated privileges.
//
// Mutation proof: revert the uid-gate (make the ownerUID != euid branch produce
// a Suspect instead of continuing) → this test goes RED. Restore → GREEN.
func TestReap_ForeignUidProcess_ProducesNoEntry(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	orphanID := domain.NewSandboxID()
	orphanSocket := filepath.Join(sockDir, orphanID.String()+".sock")

	// Plant a synthetic netns-child sentinel owned by "us" on disk, but
	// the injected lookup will claim it belongs to a foreign uid.
	const foreignPID = 424243
	writeSyntheticNetnsProcess(t, procDir, foreignPID, orphanSocket)

	// Claim this pid is owned by a uid that is NOT ours.
	foreignUID := uint32(os.Geteuid() + 1)
	opts := service.ReapOptions{
		ProcDir: procDir,
		ProcOwnerLookup: func(dir string, pid int) (uint32, error) {
			return foreignUID, nil
		},
	}

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	report, err := service.Reap(context.Background(), st, idx, false, opts)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for _, e := range report.Entries {
		if e.Resource.Kind == service.KindNetnsProcess && e.ProcessPID == foreignPID {
			t.Errorf("foreign-uid process pid %d produced a %q entry — should produce no entry at all; entry: %+v",
				foreignPID, e.Status, e)
		}
	}
}

// TestNetnsConstantDrift is a behavioral guard that fails if the mirrored
// constants in reap.go (netnsRunEnv = "NEXUS3_NETNS_RUN",
// netnsEnvAPISocket = "NEXUS3_NETNS_API_SOCKET") drift from the ABI-defining
// values set by cloudhypervisor.StartNetnsRuntime on every netns child's own
// environ (ch_netns.go:228-233).
//
// cloudhypervisor does NOT import service (verified: `go list -deps
// ./internal/core/driver/cloudhypervisor | grep -c core/service` == 0), so
// this external test package (package service_test) can import both. The test
// uses the real package symbols so a rename in ch_netns.go immediately breaks
// the test rather than letting both sides silently use the old wire value.
//
// When reap.go's private constants (netnsRunEnv / netnsEnvAPISocket) drift
// from the cloudhypervisor exported values, this synthetic child is invisible
// to sweepOrphanNetnsProcesses and the test fails.
func TestNetnsConstantDrift(t *testing.T) {
	// Assert against the REAL cloudhypervisor symbols so that a constant rename
	// in ch_netns.go makes this test go RED rather than letting both the test
	// and reap.go silently use the old wire value.
	// (cloudhypervisor does not import service, so this import is cycle-free.)
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	orphanID := domain.NewSandboxID()
	orphanSocket := filepath.Join(sockDir, orphanID.String()+".sock")

	// Plant a synthetic /proc entry using the exact environ that
	// StartNetnsRuntime writes (ch_netns.go:228-233). Using the real package
	// constants means a rename in cloudhypervisor immediately breaks this test.
	const driftPID = 424245
	pidDir := filepath.Join(procDir, fmt.Sprintf("%d", driftPID))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	environ := cloudhypervisor.NetnsRunEnv + "=1\x00" + cloudhypervisor.NetnsEnvAPISocket + "=" + orphanSocket + "\x00PATH=/usr/bin\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "environ"), []byte(environ), 0o600); err != nil {
		t.Fatal(err)
	}
	pgidLine := fmt.Sprintf("%d (sleep) S 1 %d 0 0 -1\n", driftPID, driftPID)
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(pgidLine), 0o600); err != nil {
		t.Fatal(err)
	}

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	report, err := service.Reap(context.Background(), st, idx, false, service.ReapOptions{ProcDir: procDir})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	var found *service.ReapEntry
	for i := range report.Entries {
		if report.Entries[i].Resource.Kind == service.KindNetnsProcess && report.Entries[i].ProcessPID == driftPID {
			found = &report.Entries[i]
			break
		}
	}
	if found == nil {
		t.Fatal("synthetic netns child not detected — env-var constant in reap.go has drifted from " +
			"cloudhypervisor ABI (ch_netns.go:56/74); update reap.go netnsRunEnv / netnsEnvAPISocket " +
			"AND the chNetnsRunEnv / chNetnsEnvAPISocket constants in this test")
	}
	if found.Status != service.ReapStatusOrphan {
		t.Errorf("status = %q, want Orphan (no store record, no in-flight intent)", found.Status)
	}
}

// TestNetnsProcess_UninspectableEnviron verifies that a process owned by our
// uid whose /proc/<pid>/environ is unreadable (simulating a cleared dumpable
// flag — e.g. sshd or systemd --user after setuid exec) must produce ZERO
// ReapEntries and increment ReapReport.UninspectableProcesses by 1.
//
// Note: the unreadable environ simulates a cleared dumpable flag, NOT
// ptrace_scope. Yama only gates PTRACE_MODE_ATTACH; /proc/<pid>/environ uses
// PTRACE_MODE_READ_FSCREDS, which own-uid processes can read fine under
// ptrace_scope=1. The real causes of EACCES here are zombies (no mm) and
// processes whose dumpable flag was cleared by a setuid exec.
//
// It must NOT produce a Suspect entry — EACCES on environ is an environmental
// limitation (cleared dumpable flag or process vanished), not evidence that
// the process is an unrecorded netns child.
//
// Mutation proof: change the envErr non-ENOENT branch in sweepOrphanNetnsProcesses
// to emit a Suspect entry instead of incrementing uninspectable → this test goes
// RED (len(report.Entries) becomes 1). Restore → GREEN.
func TestNetnsProcess_UninspectableEnviron(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	// Synthetic pid owned by the test runner (procDir is a t.TempDir() dir, so
	// stat of the pidDir will return our uid).
	const uninspPID = 424249
	pidDir := filepath.Join(procDir, fmt.Sprintf("%d", uninspPID))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatalf("mkdir pidDir: %v", err)
	}

	// Make /proc/<pid>/environ STRUCTURALLY unreadable by making it a directory:
	// os.ReadFile opens it fine and then read(2) fails with EISDIR, for every uid.
	//
	// DO NOT "simplify" this back to os.WriteFile + os.Chmod(0o000). A mode-0000
	// file is only unreadable for a uid that is subject to the DAC check. Root
	// holds CAP_DAC_OVERRIDE and reads it successfully, so under uid 0 the sweep
	// parses the environ and reports the pid as an ORPHAN — the assertions below
	// then cannot fail, and the coverage is vacuous. Every nexus3 sandbox runs
	// its tests as uid 0, so the chmod form was silently dead on the runner that
	// matters most.
	//
	// EISDIR is a non-ENOENT error, so it still lands on exactly the branch this
	// test exercises: the `if os.IsNotExist(envErr)` gate in
	// sweepOrphanNetnsProcesses falls through to the `inaccessible++` arm.
	environPath := filepath.Join(pidDir, "environ")
	if err := os.Mkdir(environPath, 0o700); err != nil {
		t.Fatalf("mkdir environ (as a directory, to force EISDIR): %v", err)
	}
	// The fixture must be self-checking, not comment-checked. A comment cannot
	// fail; this can. It fires as root, and it is agnostic to HOW the file was
	// made unreadable — it survives a revert to chmod, and an ACL, and anything
	// else someone reaches for. Without it, a vacuous fixture goes green.
	if _, err := os.ReadFile(environPath); err == nil {
		t.Fatalf("fixture is vacuous: environ is READABLE as uid %d, so the "+
			"uninspectable branch will never run and the assertions below cannot fail",
			os.Getuid())
	}

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	report, err := service.Reap(context.Background(), st, idx, false, service.ReapOptions{ProcDir: procDir})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// Must produce NO KindNetnsProcess entries at all.
	for _, e := range report.Entries {
		if e.Resource.Kind == service.KindNetnsProcess {
			t.Errorf("uninspectable process produced a %q entry — must produce no entry; got: %+v", e.Status, e)
		}
	}

	// Must increment the uninspectable counter.
	if report.UninspectableProcesses < 1 {
		t.Errorf("UninspectableProcesses = %d, want >= 1; EACCES on environ must be counted", report.UninspectableProcesses)
	}
}

// TestNetnsZombieSkip verifies that a process whose /proc/<pid>/stat shows
// state "Z" (zombie) is never classified as a ReapEntry — even when its
// environ carries NEXUS3_NETNS_RUN=1 and a parseable sandbox socket (which
// would produce a ReapStatusOrphan entry on a live process). A zombie has no
// mm, no tap, no netns, so killing it would be both wrong and useless.
//
// Mutation proof: removing the `state == "Z"` guard makes the zombie fall
// through to the environ read path and produce a ReapStatusOrphan entry →
// this test goes RED (len(Entries) becomes 1). Restore the guard → GREEN.
// Secondary: changing `zombies++` to `inaccessible++` in the zombie branch
// makes ZombieProcesses=0 → this test goes RED. Restore → GREEN.
func TestNetnsZombieSkip(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	// Synthetic zombie pid. The socket is a valid sandbox ID so that without
	// the zombie skip this entry WOULD be classified ReapStatusOrphan — making
	// the mutation meaningful.
	const zombiePID = 424248
	zombieID := domain.NewSandboxID()
	zombieSocket := filepath.Join(sockDir, zombieID.String()+".sock")

	pidDir := filepath.Join(procDir, fmt.Sprintf("%d", zombiePID))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// stat: state field (field 3) is "Z". Use a comm with a space to confirm
	// readProcState parses after the last ")" rather than splitting naively.
	// Format: pid (comm) state ppid pgrp ...
	zombieStat := fmt.Sprintf("%d (my proc) Z 1 %d 0 0 -1\n", zombiePID, zombiePID)
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(zombieStat), 0o600); err != nil {
		t.Fatal(err)
	}
	// environ: carries the netns sentinel so the zombie would match without the skip.
	environ := cloudhypervisor.NetnsRunEnv + "=1\x00" +
		cloudhypervisor.NetnsEnvAPISocket + "=" + zombieSocket + "\x00PATH=/usr/bin\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "environ"), []byte(environ), 0o600); err != nil {
		t.Fatal(err)
	}

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	report, err := service.Reap(context.Background(), st, idx, false, service.ReapOptions{ProcDir: procDir})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// A zombie must produce NO entry of any kind.
	for _, e := range report.Entries {
		if e.ProcessPID == zombiePID {
			t.Errorf("zombie pid %d produced a %q entry — zombies must be skipped; got: %+v",
				zombiePID, e.Status, e)
		}
	}

	// The zombie must be counted as ZombieProcesses (not silently dropped from
	// the aggregate, which would make the sweep look cleaner than it is, and
	// not blended into UninspectableProcesses which is for dumpable-flag cases).
	if report.ZombieProcesses < 1 {
		t.Errorf("ZombieProcesses = %d, want >= 1; zombie must be counted separately", report.ZombieProcesses)
	}
	if report.UninspectableProcesses != 0 {
		t.Errorf("UninspectableProcesses = %d, want 0; zombie must not bleed into inaccessible count", report.UninspectableProcesses)
	}
}
