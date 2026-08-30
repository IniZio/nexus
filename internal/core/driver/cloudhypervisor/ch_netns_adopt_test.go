// ch_netns_adopt_test.go — tests for the non-parent adoption path
// (AdoptNetnsRuntime, and Stop's non-parent confirmation branch).
//
// These are hermetic: no CH binary, no /dev/kvm, no VM. They use plain OS
// processes (sleep) to stand in for "a netns child this process did not
// fork" — exactly the shape AdoptNetnsRuntime is built to hold.
package cloudhypervisor

import (
	"net"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// ── AdoptNetnsRuntime construction ──────────────────────────────────────────

// TestAdoptNetnsRuntime_Rebuilds verifies that AdoptNetnsRuntime wires the
// four persisted values (childPID, childPGID, guestTap, apiSocket) plus the
// transferred perimeter fd into an equivalent NetnsRuntime, with cmd left
// nil (the non-parent marker Stop branches on).
func TestAdoptNetnsRuntime_Rebuilds(t *testing.T) {
	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	t.Cleanup(func() { pumpFile.Close() })

	rt, err := AdoptNetnsRuntime(4242, 4242, "nx3g-test", "/tmp/nx3-test.sock", perimFile)
	if err != nil {
		t.Fatalf("AdoptNetnsRuntime: %v", err)
	}
	t.Cleanup(func() { rt.PerimConn.Close() })

	if rt.ChildPID != 4242 {
		t.Errorf("ChildPID = %d, want 4242", rt.ChildPID)
	}
	if rt.ChildPGID != 4242 {
		t.Errorf("ChildPGID = %d, want 4242", rt.ChildPGID)
	}
	if rt.GuestTap != "nx3g-test" {
		t.Errorf("GuestTap = %q, want %q", rt.GuestTap, "nx3g-test")
	}
	if rt.APISocket != "/tmp/nx3-test.sock" {
		t.Errorf("APISocket = %q, want %q", rt.APISocket, "/tmp/nx3-test.sock")
	}
	if rt.cmd != nil {
		t.Error("cmd must be nil for an adopted runtime — Stop() branches on this")
	}

	// PerimConn must be live: round-trip a frame through the socketpair.
	msg := []byte("adopt-test")
	if _, err := syscall.Write(int(pumpFile.Fd()), msg); err != nil {
		t.Fatalf("write pumpFile: %v", err)
	}
	buf := make([]byte, 64)
	n, err := rt.PerimConn.Read(buf)
	if err != nil {
		t.Fatalf("read rt.PerimConn: %v", err)
	}
	if string(buf[:n]) != string(msg) {
		t.Errorf("round-trip: got %q want %q", buf[:n], msg)
	}
}

// TestAdoptNetnsRuntime_RejectsMissingPerimFile is a mutation-relevant
// precondition check: a nil perimFile must be rejected, not silently
// accepted into a NetnsRuntime with a nil PerimConn (which would panic or
// hang the first time a caller reads guest frames).
func TestAdoptNetnsRuntime_RejectsMissingPerimFile(t *testing.T) {
	_, err := AdoptNetnsRuntime(100, 100, "nx3g-test", "/tmp/nx3-test.sock", nil)
	if err == nil {
		t.Fatal("expected error for nil perimFile, got nil")
	}
}

// TestAdoptNetnsRuntime_RejectsNonPositivePIDs guards against a caller
// passing zero-value persisted fields (e.g. a Sandbox record predating
// NetnsChildPID) into AdoptNetnsRuntime, which would otherwise construct a
// NetnsRuntime whose Stop() sends kill(-0, SIGKILL) — the whole host's
// process group.
func TestAdoptNetnsRuntime_RejectsNonPositivePIDs(t *testing.T) {
	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	defer pumpFile.Close()

	if _, err := AdoptNetnsRuntime(0, 100, "nx3g-test", "/tmp/nx3-test.sock", perimFile); err == nil {
		perimFile.Close()
		t.Error("expected error for childPID=0, got nil")
	} else {
		perimFile.Close()
	}
}

// ── waitForGroupExit: the non-parent Stop confirmation mechanism ───────────

// TestWaitForGroupExit_ConfirmsAfterReap proves waitForGroupExit reports
// true only once the process group has actually been reaped, not merely
// once the process has exited (a zombie is still a valid kill(2) target and
// must NOT be reported as "gone").
func TestWaitForGroupExit_ConfirmsAfterReap(t *testing.T) {
	cmd := exec.Command("sleep", "0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pgid := cmd.Process.Pid

	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait() // reap once sleep exits on its own
		close(reaped)
	}()

	start := time.Now()
	ok := waitForGroupExit(pgid, 3*time.Second)
	elapsed := time.Since(start)
	<-reaped

	if !ok {
		t.Fatal("waitForGroupExit: want true (group reaped within timeout), got false")
	}
	// A false pass here (returning true immediately, e.g. from a stub that
	// never actually polls) would show as elapsed ~0s despite sleep needing
	// ~100ms to exit and be reaped.
	if elapsed < 50*time.Millisecond {
		t.Errorf("waitForGroupExit returned in %v — too fast to have actually observed the group exit; suspect it isn't polling", elapsed)
	}
}

// TestWaitForGroupExit_TimesOutWhileAlive proves waitForGroupExit does NOT
// report the group gone while a member is still alive — the mutation this
// guards against is a stub that returns true unconditionally, which would
// turn every adopter into exactly the orphan generator this mechanism
// exists to prevent (Stop would believe teardown succeeded while the VM is
// still running).
func TestWaitForGroupExit_TimesOutWhileAlive(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pgid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	ok := waitForGroupExit(pgid, 150*time.Millisecond)
	if ok {
		t.Fatal("waitForGroupExit: want false (process still alive), got true")
	}
}

// ── Stop(): non-parent path end to end ──────────────────────────────────────

// TestStop_AdoptedRuntime_KillsAndConfirms exercises Stop() on a NetnsRuntime
// built by AdoptNetnsRuntime (cmd == nil): Stop must both signal the group
// AND block until waitForGroupExit confirms it is gone, then close
// PerimConn. This is the same shape as TestLifecycle_NormalStop_NoLeaks for
// the parent-owned path, but hermetic (plain "sleep", no CH/KVM).
func TestStop_AdoptedRuntime_KillsAndConfirms(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pgid := cmd.Process.Pid
	// Reap in the background as soon as the kill lands, standing in for the
	// kernel's subreaper/init in the real non-parent scenario (in-process,
	// this Go test IS the OS parent of "sleep" since it used exec.Command —
	// but AdoptNetnsRuntime's cmd field is still nil, so Stop() takes the
	// non-parent branch regardless of who the OS parent actually is).
	go func() { _ = cmd.Wait() }()

	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	t.Cleanup(func() { pumpFile.Close() })

	rt, err := AdoptNetnsRuntime(pgid, pgid, "nx3g-test", "/tmp/nx3-test.sock", perimFile)
	if err != nil {
		t.Fatalf("AdoptNetnsRuntime: %v", err)
	}

	if e := syscall.Kill(pgid, 0); e != nil {
		t.Fatalf("sleep pid=%d not alive before Stop: %v", pgid, e)
	}

	done := make(chan struct{})
	go func() {
		rt.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("rt.Stop() did not return within 5s")
	}

	if e := syscall.Kill(-pgid, 0); e != syscall.ESRCH {
		t.Errorf("group pgid=%d not confirmed gone after Stop() returned: kill(-pgid,0) = %v, want ESRCH", pgid, e)
	}

	// PerimConn must be closed: a further read must fail (EOF/closed).
	buf := make([]byte, 8)
	if _, err := rt.PerimConn.Read(buf); err == nil {
		t.Error("rt.PerimConn.Read succeeded after Stop(); want closed connection")
	}
}

// TestStop_ParentOwnedRuntime_Unaffected is a regression guard: constructing
// a NetnsRuntime the normal way (cmd != nil) and calling Stop must still use
// the cmd.Wait() path, not the adoption path — i.e. adding AdoptNetnsRuntime
// must not change parent-owned behaviour. This does not spawn CH; it directly
// builds the struct the way StartNetnsRuntime does, using a throwaway process
// as a stand-in cmd, and asserts Stop() reaps via cmd.Wait() (cmd.ProcessState
// becomes non-nil) rather than silently no-op'ing.
func TestStop_ParentOwnedRuntime_Unaffected(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pgid := cmd.Process.Pid

	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	t.Cleanup(func() { pumpFile.Close() })
	perimConn, err := net.FileConn(perimFile)
	perimFile.Close()
	if err != nil {
		t.Fatalf("net.FileConn: %v", err)
	}

	rt := &NetnsRuntime{
		PerimConn: perimConn,
		APISocket: "/tmp/nx3-test.sock",
		GuestTap:  "nx3g-test",
		ChildPID:  pgid,
		ChildPGID: pgid,
		cmd:       cmd,
	}

	done := make(chan struct{})
	go func() {
		rt.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("rt.Stop() did not return within 5s")
	}

	if cmd.ProcessState == nil {
		t.Error("cmd.ProcessState is nil after Stop() on a parent-owned runtime — cmd.Wait() was not called")
	}
}
