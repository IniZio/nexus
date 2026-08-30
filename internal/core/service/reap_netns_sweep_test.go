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
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
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
	report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: procDir})
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
