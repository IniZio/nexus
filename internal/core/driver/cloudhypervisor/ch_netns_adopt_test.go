// ch_netns_adopt_test.go — tests for the non-parent adoption path
// (AdoptNetnsRuntime, and Stop's non-parent confirmation branch).
//
// These are hermetic: no CH binary, no /dev/kvm, no VM. They use plain OS
// processes (sleep) to stand in for "a netns child this process did not
// fork" — exactly the shape AdoptNetnsRuntime is built to hold.
package cloudhypervisor

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
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

	// A real live process, because the pid-reuse guard is fail-closed: there is
	// no "skip the check" value to hand a made-up pid any more.
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	startTime, err := readProcStartTime(pid)
	if err != nil {
		t.Fatalf("readProcStartTime(%d): %v", pid, err)
	}

	rt, err := AdoptNetnsRuntime(context.Background(), pid, pid, startTime, "nx3g-test", "/tmp/nx3-test.sock", perimFile)
	if err != nil {
		t.Fatalf("AdoptNetnsRuntime: %v", err)
	}
	t.Cleanup(func() { rt.PerimConn.Close() })

	if rt.ChildPID != pid {
		t.Errorf("ChildPID = %d, want %d", rt.ChildPID, pid)
	}
	if rt.ChildPGID != pid {
		t.Errorf("ChildPGID = %d, want %d", rt.ChildPGID, pid)
	}
	if rt.ChildStartTime != startTime {
		t.Errorf("ChildStartTime = %d, want %d", rt.ChildStartTime, startTime)
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
	_, err := AdoptNetnsRuntime(context.Background(), 100, 100, 0, "nx3g-test", "/tmp/nx3-test.sock", nil)
	if err == nil {
		t.Fatal("expected error for nil perimFile, got nil")
	}
}

// TestAdoptNetnsRuntime_RejectsNonPositivePIDs guards against a caller
// passing zero-value persisted fields (e.g. a Sandbox record predating
// NetnsChildPID) into AdoptNetnsRuntime, which would otherwise construct a
// NetnsRuntime whose Stop() sends kill(-0, SIGKILL) — the whole host's
// process group — or kill(0, SIGKILL) — the whole caller's process group.
//
// FIX 3(a): exercise childPGID <= 0 in addition to childPID <= 0.
func TestAdoptNetnsRuntime_RejectsNonPositivePIDs(t *testing.T) {
	cases := []struct {
		name      string
		childPID  int
		childPGID int
	}{
		{name: "childPID=0", childPID: 0, childPGID: 100},
		{name: "childPGID=0", childPID: 100, childPGID: 0},
		{name: "childPGID=-1", childPID: 100, childPGID: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			perimFile, pumpFile, err := netnsSocketpairFiles()
			if err != nil {
				t.Fatalf("netnsSocketpairFiles: %v", err)
			}
			defer pumpFile.Close()
			_, err = AdoptNetnsRuntime(context.Background(), tc.childPID, tc.childPGID, 0, "nx3g-test", "/tmp/nx3-test.sock", perimFile)
			perimFile.Close()
			if err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

// TestAdoptNetnsRuntime_RejectsZeroStartTime pins the pid-reuse guard as
// FAIL-CLOSED. A zero starttime means the identity was never persisted or was
// lost, so there is nothing to verify the pid against — and that is precisely
// when Stop()'s Kill(-ChildPGID, SIGKILL) is most likely to land on a recycled
// group belonging to an unrelated host process.
//
// An earlier revision treated 0 as "skip the check" for backward compatibility
// with Sandbox records predating the field. No such records exist — nothing
// writes the adoption identity onto domain.Sandbox yet — so that path bought
// nothing and silently disarmed the guard. This test exists to stop it coming
// back: a guard that fails open on its own input signal is decorative.
func TestAdoptNetnsRuntime_RejectsZeroStartTime(t *testing.T) {
	// A real, live, verifiable pid — so the ONLY reason to refuse is the
	// missing starttime, not a dead or bogus pid.
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	defer pumpFile.Close()
	defer perimFile.Close()

	rt, err := AdoptNetnsRuntime(context.Background(), pid, pid, 0, "nx3g-test", "/tmp/nx3-test.sock", perimFile)
	if err == nil {
		rt.PerimConn.Close()
		t.Fatal("adoption with childStartTime=0 was ACCEPTED; the pid-reuse guard is disarmed")
	}
	if !strings.Contains(err.Error(), "childStartTime is 0") {
		t.Errorf("error = %v, want it to name the missing starttime", err)
	}
}

// TestAdoptNetnsRuntime_RejectsStaleStartTime is the FIX-1 identity-check
// test: adoption must be REFUSED when the persisted starttime does not match
// the current /proc/<pid>/stat value, which means the pid was recycled between
// the previous supervisor exit and the current adoption.
//
// The test spawns a real short-lived process, records its pid and starttime,
// lets it exit (so the pid may be recycled), then passes a deliberately wrong
// starttime to AdoptNetnsRuntime and asserts it returns an error.
//
// Two sub-cases:
//  (a) pid still exists (as a zombie or newly recycled): wrong starttime → reject.
//  (b) pid no longer exists at all (dead and reaped): adopt must also reject,
//      because we cannot verify identity of a vanished pid.
func TestAdoptNetnsRuntime_RejectsStaleStartTime(t *testing.T) {
	// Sub-case (a): pid exists but starttime is wrong.
	t.Run("wrong_starttime", func(t *testing.T) {
		cmd := exec.Command("sleep", "30")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		pid := cmd.Process.Pid
		t.Cleanup(func() {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = cmd.Wait()
		})

		realST, err := readProcStartTime(pid)
		if err != nil {
			t.Fatalf("readProcStartTime(%d): %v", pid, err)
		}
		wrongST := realST + 12345 // deliberately wrong

		perimFile, pumpFile, err := netnsSocketpairFiles()
		if err != nil {
			t.Fatalf("netnsSocketpairFiles: %v", err)
		}
		defer pumpFile.Close()

		_, err = AdoptNetnsRuntime(context.Background(), pid, pid, wrongST, "nx3g-test", "/tmp/nx3-test.sock", perimFile)
		perimFile.Close()
		if err == nil {
			t.Errorf("expected error for starttime mismatch (real=%d, passed=%d), got nil", realST, wrongST)
		} else {
			t.Logf("correctly rejected: %v", err)
		}
	})

	// Sub-case (b): pid no longer exists — cannot verify identity.
	t.Run("pid_vanished", func(t *testing.T) {
		// Start a process, capture its pid + starttime, then kill+reap it so
		// the pid is gone from /proc before we call AdoptNetnsRuntime.
		cmd := exec.Command("sleep", "0.05")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		pid := cmd.Process.Pid
		savedST, err := readProcStartTime(pid)
		if err != nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = cmd.Wait()
			t.Fatalf("readProcStartTime(%d): %v", pid, err)
		}

		// Kill and reap so the pid disappears from /proc.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()

		// Confirm the pid is gone.
		if _, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			t.Skip("pid still present in /proc after reap (kernel recycled it too fast); skipping sub-case")
		}

		perimFile, pumpFile, err := netnsSocketpairFiles()
		if err != nil {
			t.Fatalf("netnsSocketpairFiles: %v", err)
		}
		defer pumpFile.Close()

		// Even with the correct savedST, the pid is gone → must be rejected.
		_, err = AdoptNetnsRuntime(context.Background(), pid, pid, savedST, "nx3g-test", "/tmp/nx3-test.sock", perimFile)
		perimFile.Close()
		if err == nil {
			t.Errorf("expected error for vanished pid %d (st=%d), got nil", pid, savedST)
		} else {
			t.Logf("correctly rejected: %v", err)
		}
	})
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
//
// FIX 3(b): race fix. The previous version started a background cmd.Wait()
// goroutine BEFORE rt.Stop(), creating a race: if the goroutine won and reaped
// the zombie before the ESRCH assertion, a mutation that skips waitForGroupExit
// would still pass the ESRCH check (4/20 passes). Fixed by:
//  1. Synchronising: wait for the background reaper to complete via a channel
//     before checking ESRCH, so the ESRCH assertion is deterministic regardless
//     of goroutine scheduling.
//  2. Asserting elapsed time: Stop must not return faster than a single poll
//     interval (netnsGroupExitPollInterval), catching "no poll at all" mutations.
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
	// reaped is closed once cmd.Wait() returns so we can synchronise the
	// ESRCH assertion to happen only after the zombie is reaped.
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()

	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	t.Cleanup(func() { pumpFile.Close() })

	// Use the real starttime so the identity check is exercised.
	childST, err := readProcStartTime(pgid)
	if err != nil {
		t.Fatalf("readProcStartTime(%d): %v", pgid, err)
	}

	rt, err := AdoptNetnsRuntime(context.Background(), pgid, pgid, childST, "nx3g-test", "/tmp/nx3-test.sock", perimFile)
	if err != nil {
		t.Fatalf("AdoptNetnsRuntime: %v", err)
	}

	if e := syscall.Kill(pgid, 0); e != nil {
		t.Fatalf("sleep pid=%d not alive before Stop: %v", pgid, e)
	}

	done := make(chan struct{})
	startStop := time.Now()
	go func() {
		rt.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(netnsAdoptStopTimeout + 2*time.Second):
		t.Fatalf("rt.Stop() did not return within %v", netnsAdoptStopTimeout+2*time.Second)
	}
	elapsed := time.Since(startStop)

	// Wait for the reaper to complete so the ESRCH check is deterministic —
	// not racy on goroutine scheduling.
	select {
	case <-reaped:
	case <-time.After(2 * time.Second):
		t.Fatal("background reaper did not complete within 2s after Stop — sleep may still be alive")
	}

	if e := syscall.Kill(-pgid, 0); e != syscall.ESRCH {
		t.Errorf("group pgid=%d not confirmed gone after Stop()+reap: kill(-pgid,0) = %v, want ESRCH", pgid, e)
	}

	// PerimConn must be closed: a further read must fail (EOF/closed).
	buf := make([]byte, 8)
	if _, err := rt.PerimConn.Read(buf); err == nil {
		t.Error("rt.PerimConn.Read succeeded after Stop(); want closed connection")
	}

	// Stop must have polled at least once before returning. A mutation that
	// skips waitForGroupExit entirely returns in ~0 µs; one poll interval
	// (netnsGroupExitPollInterval = 20 ms) is a conservative lower bound.
	if elapsed < netnsGroupExitPollInterval {
		t.Errorf("Stop returned in %v — faster than one poll interval (%v); "+
			"suspect waitForGroupExit was not called (mutation?)", elapsed, netnsGroupExitPollInterval)
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
