package store_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/store"
)

// TestHelperHoldsLock is a subprocess helper: when NEXUS3_TEST_LOCK_HELPER=1 it
// acquires an exclusive flock on the lock file named by NEXUS3_TEST_LOCK_FILE,
// prints "ready\n" to stdout (signalling the parent), then blocks forever.
// The parent SIGKILLs this process to prove the kernel releases the lock.
func TestHelperHoldsLock(t *testing.T) {
	if os.Getenv("NEXUS3_TEST_LOCK_HELPER") != "1" {
		t.Skip("subprocess helper mode not active")
	}
	path := os.Getenv("NEXUS3_TEST_LOCK_FILE")
	if path == "" {
		t.Fatal("NEXUS3_TEST_LOCK_FILE not set")
	}
	lk, err := store.OpenLock(path)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer lk.Close()
	if err := lk.Exclusive(context.Background()); err != nil {
		t.Fatalf("acquire exclusive: %v", err)
	}
	// Signal readiness; parent reads this line before asserting.
	os.Stdout.WriteString("ready\n")
	// Block until SIGKILL.
	select {}
}

// TestLockCrossProcess verifies that when a lock holder is SIGKILLed the
// kernel releases the flock automatically, and the parent can then acquire it.
// This is the property that makes flock crash-safe for multi-process CLI
// invocations: a dead holder never permanently blocks a new invocation.
func TestLockCrossProcess(t *testing.T) {
	dir := t.TempDir()
	lockFile := filepath.Join(dir, "lock")

	// Pre-create the lock file so the subprocess can open it without
	// a parent directory that only we know.
	f, err := os.Create(lockFile)
	if err != nil {
		t.Fatalf("create lock file: %v", err)
	}
	f.Close()

	// Launch subprocess that acquires the lock and signals readiness.
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsLock$", "-test.v")
	cmd.Env = append(os.Environ(),
		"NEXUS3_TEST_LOCK_HELPER=1",
		"NEXUS3_TEST_LOCK_FILE="+lockFile,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}

	// Wait for the "ready" line — no time.Sleep handshake.
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if scanner.Text() == "ready" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read subprocess stdout: %v", err)
	}

	// Kill the child. The kernel releases the flock automatically.
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill subprocess: %v", err)
	}
	_, _ = cmd.Process.Wait()

	// Acquire with a timeout to bound the test. Exclusive parks in the kernel
	// until the lock is free; since the child is already dead the flock is
	// released and this should return promptly.
	lk, err := store.OpenLock(lockFile)
	if err != nil {
		t.Fatalf("open lock in parent: %v", err)
	}
	defer lk.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := lk.Exclusive(ctx); err != nil {
		t.Fatalf("expected to acquire lock after child killed, got %v", err)
	}
	_ = lk.Unlock()
}

// TestHelperHoldsLockForTryTest is a subprocess helper for TestTryExclusive_deadline.
// Unlike TestHelperHoldsLock (which uses select{} and triggers Go's deadlock detector),
// this helper uses time.Sleep so the runtime stays alive while holding the lock.
// The parent SIGKILLs this subprocess when done.
func TestHelperHoldsLockForTryTest(t *testing.T) {
	if os.Getenv("NEXUS3_TEST_LOCK_TRY_HELPER") != "1" {
		t.Skip("subprocess helper mode not active")
	}
	path := os.Getenv("NEXUS3_TEST_LOCK_FILE")
	if path == "" {
		t.Fatal("NEXUS3_TEST_LOCK_FILE not set")
	}
	lk, err := store.OpenLock(path)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer lk.Close()
	if err := lk.Exclusive(context.Background()); err != nil {
		t.Fatalf("acquire exclusive: %v", err)
	}
	// Signal readiness AFTER holding the lock.
	os.Stdout.WriteString("ready\n")
	// Sleep long enough for the parent's TryExclusive test to complete.
	// Parent sends SIGKILL before this expires.
	time.Sleep(30 * time.Second)
}

// TestTryExclusive_deadline verifies that TryExclusive returns a context-deadline
// error within the deadline when another process holds the lock, rather than
// blocking indefinitely past the deadline.
//
// Mutation proof: replacing TryExclusive's LOCK_NB retry loop with a blocking
// LOCK_EX call (i.e., reverting to Exclusive) would cause this test to hang
// because the subprocess holds the lock for the full test duration and no signal
// fires to interrupt the blocking flock syscall.
func TestTryExclusive_deadline(t *testing.T) {
	dir := t.TempDir()
	lockFile := filepath.Join(dir, "lock")

	// Pre-create the lock file so the subprocess can open it.
	f, err := os.Create(lockFile)
	if err != nil {
		t.Fatalf("create lock file: %v", err)
	}
	f.Close()

	// Spawn a subprocess that acquires the lock and holds it.
	// Uses TestHelperHoldsLockForTryTest (not TestHelperHoldsLock) to avoid
	// triggering Go's deadlock detector: select{} in a blocked test goroutine
	// causes a deadlock panic which exits the subprocess, releasing the lock.
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsLockForTryTest$", "-test.v")
	cmd.Env = append(os.Environ(),
		"NEXUS3_TEST_LOCK_TRY_HELPER=1",
		"NEXUS3_TEST_LOCK_FILE="+lockFile,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	}()

	// Wait for "ready" (written by subprocess after holding the lock).
	scanner := bufio.NewScanner(stdout)
	gotReady := false
	for scanner.Scan() {
		if scanner.Text() == "ready" {
			gotReady = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read subprocess stdout: %v", err)
	}
	if !gotReady {
		t.Fatalf("subprocess exited without printing 'ready' (subprocess may have panicked)")
	}

	// TryExclusive with a short deadline: must return an error within the
	// deadline, not hang in the kernel past it.
	lk, err := store.OpenLock(lockFile)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer lk.Close()

	const deadline = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	start := time.Now()
	tryErr := lk.TryExclusive(ctx)
	elapsed := time.Since(start)

	if tryErr == nil {
		t.Fatal("TryExclusive: expected error (lock is held by subprocess), got nil")
	}
	// Must return within a reasonable margin of the deadline (not hang).
	const margin = 100 * time.Millisecond
	if elapsed > deadline+margin {
		t.Errorf("TryExclusive blocked past deadline: elapsed=%v, want ≤%v", elapsed, deadline+margin)
	}
	t.Logf("TryExclusive returned in %v (deadline=%v): %v", elapsed, deadline, tryErr)
}
