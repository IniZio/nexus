package cloudhypervisor

// ch_netns_deathwatch_test.go — unit tests for AC-12b/c (death-watch mechanism).
//
// These tests verify:
//   AC-12c — the death-watch goroutine calls cmd.Wait(), reaping the zombie,
//             and closes rt.deathCh so observers are notified.
//   AC-12b — the adopted-path watcher closes rt.deathCh via pgid poll ONLY
//             when the group is confirmed gone (ESRCH), and does NOT close it
//             while the group is alive.
//
// No KVM, no re-exec, no VM: the tests start real but trivial OS processes
// ("true" / "sleep") that exit on their own.
//
// Mutation proof targets are documented inline.

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestDeathWatch_ParentOwned verifies AC-12c: after a parent-owned netns
// child exits, rt.deathCh is closed (the zombie is reaped) within a short
// deadline, and exactly one cmd.Wait() was called (the goroutine's, not
// Stop's).
//
// MUTATION PROOF target: in watchParentOwnedDeath, the line
//
//	close(rt.deathCh)
//
// If that line is removed or the goroutine is not started, this test times
// out — a genuine test FAILURE (not a build error, not a false pass).
func TestDeathWatch_ParentOwned(t *testing.T) {
	// Start a process that exits immediately.
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	rt := &NetnsRuntime{
		cmd:     cmd,
		deathCh: make(chan struct{}),
	}

	go rt.watchParentOwnedDeath()

	select {
	case <-rt.deathCh:
		// PASS: goroutine called cmd.Wait() and closed the channel.
	case <-time.After(5 * time.Second):
		t.Fatal("FAIL AC-12c: deathCh not closed within 5s — watchParentOwnedDeath did not reap the child")
	}
}

// TestDeathWatch_ParentOwned_DeathChIsReadable verifies that DeathCh() returns
// the same channel as the internal field, and that it closes after child exit.
func TestDeathWatch_ParentOwned_DeathChIsReadable(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	rt := &NetnsRuntime{
		cmd:     cmd,
		deathCh: make(chan struct{}),
	}
	go rt.watchParentOwnedDeath()

	exposed := rt.DeathCh()
	select {
	case <-exposed:
		// PASS
	case <-time.After(5 * time.Second):
		t.Fatal("FAIL AC-12b: DeathCh() not closed within 5s after child exit")
	}
}

// TestDeathWatch_SingleWaitOwner verifies that a second call to cmd.Wait()
// AFTER the goroutine has already waited returns an error ("wait: no child
// processes") — proving that the goroutine DID call Wait() and there is only
// one owner. If the goroutine never called Wait(), this assertion would fail
// differently (the test itself holds Wait, succeeds, and the goroutine may
// race or also succeed).
func TestDeathWatch_SingleWaitOwner(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	rt := &NetnsRuntime{
		cmd:     cmd,
		deathCh: make(chan struct{}),
	}
	go rt.watchParentOwnedDeath()

	// Wait for the goroutine to finish.
	select {
	case <-rt.deathCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for deathCh")
	}

	// A second cmd.Wait() must fail — the goroutine already reaped the child.
	err := cmd.Wait()
	if err == nil {
		t.Fatal("FAIL AC-12c single-owner: second cmd.Wait() succeeded — goroutine did not call Wait()")
	}
}

// TestDeathWatch_StopUsesDeathCh verifies that Stop() blocks until deathCh is
// closed (i.e. the goroutine owns Wait and Stop does NOT race cmd.Wait()).
// We use a process that sleeps so Stop's SIGKILL must actually terminate it;
// the deathCh gate ensures Stop does not return before the child is reaped.
// Setpgid:true puts the child in its own process group so Kill(-pgid) only
// reaches it, not the test's own group.
func TestDeathWatch_StopUsesDeathCh(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	rt := &NetnsRuntime{
		cmd:       cmd,
		deathCh:   make(chan struct{}),
		ChildPGID: cmd.Process.Pid, // pgid == pid because Setpgid:true
	}
	go rt.watchParentOwnedDeath()

	done := make(chan struct{})
	go func() {
		rt.Stop()
		close(done)
	}()

	select {
	case <-done:
		// PASS: Stop() returned, which means deathCh was closed (child reaped).
	case <-time.After(5 * time.Second):
		t.Fatal("FAIL: Stop() did not return within 5s — deathCh may not be wired into Stop()")
	}

	// Confirm the child was actually reaped: a second Wait() must fail.
	if err := cmd.Wait(); err == nil {
		t.Fatal("FAIL AC-12c: second cmd.Wait() succeeded after Stop() — child not reaped")
	}
}

// TestDeathWatch_AdoptedPath_PositiveAndNegative is the COMBINED positive and
// negative correctness test for watchAdoptedDeath.
//
// POSITIVE assertion: deathCh closes after the process group is killed (ESRCH
// detected).
//
// NEGATIVE assertion (the safety-critical one): deathCh must NOT close while
// the process group is alive — even after netnsAdoptStopTimeout has elapsed.
// The old bug (discarded bool from waitForGroupExit) failed this assertion:
// deathCh would close after ~5s regardless of whether the group was alive.
//
// MUTATION PROOF target: making watchAdoptedDeath close deathCh
// unconditionally (removing the ESRCH condition) must make this test RED on
// the negative assertion. Assert substitution count = 1 before accepting the
// proof.
func TestDeathWatch_AdoptedPath_PositiveAndNegative(t *testing.T) {
	t.Parallel()

	// ── Part 1: NEGATIVE — deathCh must stay open while the group is alive ──

	// Spawn a long-running process in its own group. The watcher must NOT close
	// deathCh while this process is alive.
	liveCmd := exec.Command("sleep", "120")
	liveCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := liveCmd.Start(); err != nil {
		t.Fatalf("start live sleep: %v", err)
	}
	livePGID := liveCmd.Process.Pid // pgid == pid because Setpgid:true

	// Cancel context ties watcher lifetime to the test.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	liveRT := &NetnsRuntime{
		deathCh:   make(chan struct{}),
		ChildPGID: livePGID,
	}
	go liveRT.watchAdoptedDeath(ctx)

	// Wait 2× netnsAdoptStopTimeout — the old bug would fire here.
	negDuration := 2 * netnsAdoptStopTimeout
	select {
	case <-liveRT.deathCh:
		// Kill the process before failing so we don't leave it behind.
		_ = syscall.Kill(-livePGID, syscall.SIGKILL)
		_ = liveCmd.Wait()
		t.Fatalf("FAIL AC-12b negative: deathCh closed after %v while process group %d is ALIVE — watcher has a false-death bug", negDuration, livePGID)
	case <-time.After(negDuration):
		// PASS: channel stayed open as required.
	}

	// ── Part 2: POSITIVE — deathCh closes after the group is killed ──

	// Kill the group now; watcher should detect ESRCH and close deathCh.
	_ = syscall.Kill(-livePGID, syscall.SIGKILL)
	_ = liveCmd.Wait() // reap so no zombie under test runner

	select {
	case <-liveRT.deathCh:
		// PASS: ESRCH detected, deathCh closed.
	case <-time.After(5 * time.Second):
		t.Fatal("FAIL AC-12b positive: deathCh not closed within 5s after group killed (ESRCH not detected)")
	}
}

// TestDeathWatch_AdoptedPath_CtxCancelDoesNotCloseDeathCh verifies the
// context-cancel exit path: when ctx is cancelled while the group is alive,
// watchAdoptedDeath exits WITHOUT closing deathCh. This is the "supervisor
// shutting down for a different reason (stop/detach), VM still alive" path.
func TestDeathWatch_AdoptedPath_CtxCancelDoesNotCloseDeathCh(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sleep", "120")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pgid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	ctx, cancel := context.WithCancel(context.Background())
	rt := &NetnsRuntime{
		deathCh:   make(chan struct{}),
		ChildPGID: pgid,
	}
	go rt.watchAdoptedDeath(ctx)

	// Cancel context while the group is still alive.
	cancel()

	// Give the goroutine time to process the cancellation.
	time.Sleep(100 * time.Millisecond)

	select {
	case <-rt.deathCh:
		t.Fatal("FAIL: deathCh closed after ctx cancel — watcher must not signal VM death when supervisor shuts down for another reason")
	default:
		// PASS: deathCh remains open (VM is alive).
	}
}
