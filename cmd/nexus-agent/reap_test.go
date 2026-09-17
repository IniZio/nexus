package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestDrainChildren_BurstExceedsBuffer verifies that drainChildren reaps every
// zombie in a single call, even when more children exit than the old 8-slot
// signal channel buffer could accommodate.
//
// MUTATION PROOF: if the inner "for" loop in drainChildren is collapsed to a
// single Wait4 call (removing the looping), exactly one child is reaped per
// invocation. With n=20 children and one drainChildren call, 19 remain as
// zombies and the test reports session-not-reaped failures for those 19.
// To verify the mutation: change the "for {" in drainChildren to "if true {"
// (or otherwise remove the loop). The substitution count must be 1; the build
// must still compile; and this test must then fail.
// Restore via Edit — never via git checkout/stash.
//
// SAFETY: children are /bin/true — trivial external processes. os.Executable()
// is never called. There is no self-re-exec path in this test.
func TestDrainChildren_BurstExceedsBuffer(t *testing.T) {
	// n must exceed the old 8-slot buffer so that without the drain loop a
	// single drainChildren call could not reap every child.
	const n = 20

	st := newSessionTable()
	a := &Agent{sessions: st}

	type child struct {
		cmd  *exec.Cmd
		sess *Session
	}
	children := make([]child, n)

	for i := range children {
		cmd := exec.Command("/bin/true")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %d: %v", i, err)
		}
		sess := &Session{
			id:     fmt.Sprintf("drain-test-%d", i),
			pid:    cmd.Process.Pid,
			cmd:    cmd,
			exitCh: make(chan int32, 1),
			// ring is nil: notifyExit only sends to exitCh, never calls setExited.
		}
		st.add(sess)
		children[i] = child{cmd: cmd, sess: sess}
	}

	// Give all children time to exit and become zombies.
	// /bin/true exits in microseconds; 500 ms is generous even under load.
	time.Sleep(500 * time.Millisecond)

	// One call to drainChildren must reap all n zombies.
	// Under the correct implementation (inner loop) every zombie is collected.
	// Under the mutation (single Wait4, no loop) only one is collected.
	a.drainChildren()

	var notReaped int
	for i, c := range children {
		select {
		case <-c.sess.exitCh:
			// reaped correctly
		default:
			t.Errorf("session %d (pid %d) not reaped by drainChildren", i, c.cmd.Process.Pid)
			notReaped++
		}
	}
	if notReaped > 0 {
		t.Fatalf("%d/%d sessions not reaped in a single drainChildren call", notReaped, n)
	}
}

// TestReapLoop_TickerBackstop proves that the 30-second backstop ticker in
// reapLoop reaps zombie children even when NO SIGCHLD is ever delivered to
// the loop's signal channel.
//
// The injectable seams used:
//   - reapInterval (package var): overridden to 50 ms so the test completes
//     quickly rather than waiting 30 s.
//   - Agent.reapSigCh (struct field): set to a channel that is never written
//     to. reapLoop detects the non-nil value and skips signal.Notify entirely,
//     so SIGCHLD cannot reach the loop. The ticker is then the only path that
//     calls drainChildren.
//
// MUTATION PROOF: remove the "case <-ticker.C:" arm from reapLoop's select
// (the substitution count must be 1 and the file must still compile). With the
// ticker arm gone and the signal channel permanently blocked, drainChildren is
// never called and the test times out with:
//
//	"timeout: child was not reaped by the ticker backstop"
//
// Restore with Edit — never via git checkout/stash.
//
// SAFETY: the child is /bin/true — a trivial external binary. os.Executable()
// is never called; there is no self-re-exec path.
func TestReapLoop_TickerBackstop(t *testing.T) {
	// Override the production 30 s interval with something fast.
	old := reapInterval
	reapInterval = 50 * time.Millisecond
	t.Cleanup(func() { reapInterval = old })

	st := newSessionTable()
	// reapSigCh is never written to: this proves the ticker, not the signal
	// path, is what causes the child to be reaped.
	a := &Agent{
		sessions:  st,
		reapSigCh: make(chan os.Signal), // unbuffered, never sent to
	}

	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	sess := &Session{
		id:     "ticker-backstop-test",
		pid:    cmd.Process.Pid,
		cmd:    cmd,
		exitCh: make(chan int32, 1),
		// ring is nil: notifyExit sends to exitCh only, never calls setExited.
	}
	st.add(sess)

	// Run reapLoop in the background. It will never receive a SIGCHLD
	// (reapSigCh is permanently blocked); the ticker is the only wakeup.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go a.reapLoop(ctx)

	// /bin/true exits in microseconds. Wait for the ticker to fire and collect
	// the zombie. Five seconds is generous even on a loaded host with 50 ms ticks.
	select {
	case code := <-sess.exitCh:
		if code != 0 {
			t.Errorf("expected exit code 0 from /bin/true, got %d", code)
		}
	case <-ctx.Done():
		t.Fatal("timeout: child was not reaped by the ticker backstop within 5s")
	}
}
