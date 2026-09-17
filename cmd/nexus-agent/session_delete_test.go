package main

import (
	"fmt"
	"os/exec"
	"testing"
	"time"
)

// retainedExitedBytes sums ring.cap for every exited session in the table.
// It is a test helper; the sweep uses the same arithmetic to enforce the budget.
func retainedExitedBytes(tbl *SessionTable) int {
	tbl.mu.RLock()
	defer tbl.mu.RUnlock()
	total := 0
	for _, s := range tbl.byID {
		if s.exited.Load() {
			total += s.ring.cap
		}
	}
	return total
}

// TestSessionTable_ByteBudgetAfterManyExecs verifies that after a burst of
// execs drives the total retained ring bytes above the budget, the budget
// eviction path trims retention back to at or below sessionRetentionBudget.
//
// PRODUCTION PATH: this test calls cs.execPipe (the same function the Exec
// RPC dispatches to for non-PTY sessions), which calls sessions.add and wires
// the exit goroutine that calls sweepExited. No eviction helper is called
// directly.
//
// MUTATION PROOF: removing the byte-budget check inside sweepExited (the
// `if totalBytes <= sessionRetentionBudget { return }` block) leaves retained
// bytes above the budget and fails the assertion.
func TestSessionTable_ByteBudgetAfterManyExecs(t *testing.T) {
	// Set budget to exactly 2 ring-widths so that 3 completed sessions exceed
	// it and the eviction path must fire. TTL is set high to isolate the budget
	// branch from TTL eviction.
	origBudget := sessionRetentionBudget
	origTTL := sessionRetentionTTL
	sessionRetentionBudget = 2 * ringCapacity // 2 × 16 MiB = 32 MiB
	sessionRetentionTTL = 100 * time.Hour
	defer func() {
		sessionRetentionBudget = origBudget
		sessionRetentionTTL = origTTL
	}()

	a := &Agent{
		sessions: newSessionTable(),
		copies:   newCopyTable(),
		isPid1:   false,
	}
	cs := newControlServer(a)

	const nExecs = 3 // 3 × 16 MiB = 48 MiB; budget = 32 MiB → must evict 1

	sessions := make([]*Session, nExecs)
	for i := range sessions {
		cmd := exec.Command("true")
		sess := &Session{
			id:     fmt.Sprintf("byte-budget-test-%d", i),
			cmd:    cmd,
			ring:   newRing(ringCapacity),
			exitCh: make(chan int32, 1),
		}
		if err := cs.execPipe(cmd, []string{}, sess); err != nil {
			t.Fatalf("execPipe %d: %v", i, err)
		}
		sessions[i] = sess
	}

	// Wait for all sessions to exit. The exit goroutine calls sweepExited after
	// setExited, so by the time ring.IsDone() is true the sweep has already run
	// (ring.Close is called inside setExited, before the goroutine proceeds to
	// sweepExited — there is a brief window but we give it extra slack below).
	deadline := time.Now().Add(5 * time.Second)
	for _, s := range sessions {
		for !s.ring.IsDone() {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for sessions to exit")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// Small sleep to let the exit goroutines reach sweepExited after setExited.
	time.Sleep(20 * time.Millisecond)

	got := retainedExitedBytes(a.sessions)
	if got > sessionRetentionBudget {
		t.Errorf("retained exited bytes = %d; want ≤ %d (budget)", got, sessionRetentionBudget)
	}
}

// TestSessionTable_SweepOnExit_IdleGuest verifies that a completed session is
// evicted via the exit-path sweep when the TTL expires, without requiring a
// subsequent exec to trigger sweepExited.
//
// PRODUCTION PATH: the test calls cs.execPipe; the exit goroutine wired in
// control.go calls sweepExited after setExited. No add() call follows the
// session's exit — there is no trigger exec.
//
// MUTATION PROOF: removing the cs.a.sessions.sweepExited() call from the exit
// goroutines in control.go means no sweep fires after the session exits; with
// TTL=0 the session is eligible immediately but is never collected, so the
// table remains non-empty and the assertion fails.
func TestSessionTable_SweepOnExit_IdleGuest(t *testing.T) {
	// TTL = 0 makes every exited session eligible for eviction instantly.
	origTTL := sessionRetentionTTL
	origBudget := sessionRetentionBudget
	sessionRetentionTTL = 0
	sessionRetentionBudget = 100 * ringCapacity // disable budget branch; test TTL branch
	defer func() {
		sessionRetentionTTL = origTTL
		sessionRetentionBudget = origBudget
	}()

	a := &Agent{
		sessions: newSessionTable(),
		copies:   newCopyTable(),
		isPid1:   false,
	}
	cs := newControlServer(a)

	cmd := exec.Command("true")
	sess := &Session{
		id:     "idle-sweep-test",
		cmd:    cmd,
		ring:   newRing(ringCapacity),
		exitCh: make(chan int32, 1),
	}
	if err := cs.execPipe(cmd, []string{}, sess); err != nil {
		t.Fatalf("execPipe: %v", err)
	}

	// Wait for the session to exit.
	deadline := time.Now().Add(5 * time.Second)
	for !sess.ring.IsDone() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for session to exit")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Give the exit goroutine time to call sweepExited after setExited.
	time.Sleep(20 * time.Millisecond)

	// No subsequent exec was issued. The session must have been collected by the
	// exit-path sweep (TTL=0 → eligible immediately once exitedAt is set).
	a.sessions.mu.RLock()
	count := len(a.sessions.byID)
	a.sessions.mu.RUnlock()

	if count != 0 {
		t.Errorf("expected 0 sessions after exit-path TTL sweep, got %d (exit-path sweep not wired?)", count)
	}
}
