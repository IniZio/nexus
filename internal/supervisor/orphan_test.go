package supervisor

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── WaitForExit tests ─────────────────────────────────────────────────────────

// TestWaitForExit_NoPidfile verifies that WaitForExit returns immediately when
// no pidfile exists (supervisor never wrote it, or already removed it on exit).
func TestWaitForExit_NoPidfile(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := WaitForExit(ctx, dir); err != nil {
		t.Errorf("WaitForExit with absent pidfile: got %v, want nil", err)
	}
}

// TestWaitForExit_PidfileWithDeadPID verifies that WaitForExit returns when the
// pidfile contains a PID that is no longer alive (process already exited).
func TestWaitForExit_PidfileWithDeadPID(t *testing.T) {
	dir := t.TempDir()
	// Write a pidfile with a PID that is guaranteed to be dead.
	if err := os.WriteFile(PidfilePath(dir), []byte("9999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := WaitForExit(ctx, dir); err != nil {
		t.Errorf("WaitForExit with dead-PID pidfile: got %v, want nil", err)
	}
}

// TestWaitForExit_PidfileRemovedWhilePolling verifies the primary production
// path: the pidfile is present when WaitForExit starts, then removed (by the
// supervisor exiting) while the poll loop is running.
func TestWaitForExit_PidfileRemovedWhilePolling(t *testing.T) {
	dir := t.TempDir()
	pidfile := PidfilePath(dir)
	// Write our own PID (guaranteed alive) so PidAlive returns true initially,
	// forcing WaitForExit to actually poll until the goroutine below removes it.
	pidStr := fmt.Sprintf("%d\n", os.Getpid())
	if err := os.WriteFile(pidfile, []byte(pidStr), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Remove the pidfile after a short delay to simulate the supervisor exiting.
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.Remove(pidfile)
	}()

	if err := WaitForExit(ctx, dir); err != nil {
		t.Errorf("WaitForExit: got %v, want nil (pidfile removed while polling)", err)
	}
}

// TestWaitForExit_ContextCancelled verifies that WaitForExit returns
// ctx.Err() when the context is cancelled before the pidfile disappears.
func TestWaitForExit_ContextCancelled(t *testing.T) {
	dir := t.TempDir()
	// Write our own PID (guaranteed alive) so PidAlive returns true indefinitely.
	// Using os.Getpid() avoids the "PID out of range → treated as dead" pitfall
	// that would cause WaitForExit to return nil before the context fires.
	pidStr := fmt.Sprintf("%d\n", os.Getpid())
	if err := os.WriteFile(PidfilePath(dir), []byte(pidStr), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := WaitForExit(ctx, dir)
	if err == nil {
		t.Error("WaitForExit: got nil, want context error")
	}
}

// ── CheckAndReconcile tests ───────────────────────────────────────────────────

// TestCheckAndReconcile_PidDead verifies that a dead PID is treated as stale
// and that stale artifact files are cleaned up.
func TestCheckAndReconcile_PidDead(t *testing.T) {
	dir := t.TempDir()
	// Write dummy pid/sock files to confirm cleanup.
	pidFile := PidfilePath(dir)
	sockFile := SockPath(dir)
	if err := os.WriteFile(pidFile, []byte("9999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sockFile, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	alive, err := CheckAndReconcile(9999999, sockFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alive {
		t.Fatal("expected stale (dead pid), got alive")
	}
	// Artifact files should have been removed.
	if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
		t.Error("expected pid file to be cleaned up")
	}
	if _, statErr := os.Stat(sockFile); !os.IsNotExist(statErr) {
		t.Error("expected sock file to be cleaned up")
	}
}

// TestCheckAndReconcile_PidAliveSocketConnectable verifies that a live PID
// with a connectable socket is correctly reported as alive.
func TestCheckAndReconcile_PidAliveSocketConnectable(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "supervisor.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	defer ln.Close()

	// Accept connections in the background so DialTimeout doesn't hang.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	alive, err := CheckAndReconcile(os.Getpid(), sockPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alive {
		t.Fatal("expected alive (pid live + socket connectable), got stale")
	}
}

// TestCheckAndReconcile_PidAliveSocketNotConnectable verifies that a live PID
// whose recorded socket path is not connectable (PID reuse scenario) is treated
// as stale and its artifacts are cleaned up.
func TestCheckAndReconcile_PidAliveSocketNotConnectable(t *testing.T) {
	dir := t.TempDir()
	sockPath := SockPath(dir)
	pidFile := PidfilePath(dir)

	// Write dummy artifact files (no listener behind sockPath).
	if err := os.WriteFile(pidFile, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// sockPath intentionally has no listener — just write a placeholder file so
	// CleanupStaleFiles has something to remove (and confirm it does).
	if err := os.WriteFile(sockPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	alive, err := CheckAndReconcile(os.Getpid(), sockPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alive {
		t.Fatal("expected stale (pid alive but socket not connectable), got alive")
	}
	// Artifact files should have been removed.
	if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
		t.Error("expected pid file to be cleaned up")
	}
	if _, statErr := os.Stat(sockPath); !os.IsNotExist(statErr) {
		t.Error("expected sock file to be cleaned up")
	}
}
