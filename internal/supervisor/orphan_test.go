package supervisor

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

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
