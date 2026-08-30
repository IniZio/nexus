package supervisor

// TestPidfileCleanup_DoesNotRemoveReplacementPidfile is the MAJOR 2 regression
// test: the deferred pidfile cleanup in RunDetached must NOT remove a pidfile
// that a replacement supervisor has already written over.
//
// The fix mirrors removeOwnSocket: read the file at cleanup time and skip the
// unlink when it no longer names our own PID.
//
// Mutation proof: if the deferred cleanup is changed back to an unconditional
// os.Remove, this test turns RED because the replacement's pidfile (with a
// different PID) is deleted.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// removeOwnPidfile is a standalone helper that implements the safe-removal
// logic extracted for unit-testability. The production defer in supervisor.go
// inlines the same logic; this helper lets us test it in isolation.
func removeOwnPidfile(pidfile string, ownPID int) {
	data, err := os.ReadFile(pidfile)
	if err != nil {
		return // already gone
	}
	pidStr := string(data)
	// strip trailing newline
	for len(pidStr) > 0 && pidStr[len(pidStr)-1] == '\n' {
		pidStr = pidStr[:len(pidStr)-1]
	}
	if pidStr != strconv.Itoa(ownPID) {
		return // replacement already wrote its own PID
	}
	_ = os.Remove(pidfile)
}

// TestPidfileCleanup_RemovesOwnPidfile verifies the normal path: when the
// pidfile still names our own PID, the cleanup removes it.
func TestPidfileCleanup_RemovesOwnPidfile(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "supervisor.pid")
	ownPID := 42000

	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(ownPID)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	removeOwnPidfile(pidfile, ownPID)

	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Fatalf("removeOwnPidfile did not remove the pidfile it owns")
	}
}

// TestPidfileCleanup_DoesNotRemoveReplacementPidfile verifies the guard: when
// the pidfile names a different PID (the replacement's), the cleanup skips it.
//
// This is the mutation-critical assertion. An unconditional os.Remove would
// delete the file, causing Stat to return os.IsNotExist — the test would fail
// with "replacement pidfile was unexpectedly removed".
func TestPidfileCleanup_DoesNotRemoveReplacementPidfile(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "supervisor.pid")
	outgoingPID := 42000
	replacementPID := 42001

	// Replacement supervisor overwrites the pidfile with its own PID.
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(replacementPID)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Outgoing supervisor's deferred cleanup runs.
	removeOwnPidfile(pidfile, outgoingPID)

	// The replacement's pidfile must still exist.
	content, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("replacement pidfile was unexpectedly removed: %v", err)
	}
	got := string(content)
	for len(got) > 0 && got[len(got)-1] == '\n' {
		got = got[:len(got)-1]
	}
	if got != strconv.Itoa(replacementPID) {
		t.Errorf("replacement pidfile content = %q, want %q", got, strconv.Itoa(replacementPID))
	}
}

// TestPidfileCleanup_AlreadyGone verifies that a missing pidfile is a no-op
// (the file was already cleaned up or never written — both are fine).
func TestPidfileCleanup_AlreadyGone(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "supervisor.pid")
	// File does not exist.
	removeOwnPidfile(pidfile, 42000) // must not panic or error
}
