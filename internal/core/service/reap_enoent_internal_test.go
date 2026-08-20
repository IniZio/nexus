package service

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/store"
)

// TBD-PD-37, idempotency half. A resource that vanished between enumeration
// and deletion is a SUCCESSFUL reclamation — another reaper or a concurrent
// `sandbox rm` finished the job — and must not be reported as a failure.
//
// If it were, two reapers racing would each exit non-zero on work that
// completed correctly, and operators would learn to ignore the exit code,
// which is the one signal TBD-PD-37 exists to make trustworthy.
//
// This lives in the internal test package because the window it needs cannot
// be produced by arranging real files: Reap enumerates first and deletes
// second, so a file removed beforehand is never enumerated at all.
func TestReap_VanishedBetweenScanAndDeleteIsNotAFailure(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(disksDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(disksDir, "raced.shadow.node_modules.ext4")
	if err := os.WriteFile(path, []byte("fake-ext4-data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	orig := deleteResourceFn
	t.Cleanup(func() { deleteResourceFn = orig })
	stubbed := false
	deleteResourceFn = func(res HostResource) error {
		stubbed = true
		// Someone else got here first.
		_ = os.Remove(res.Path)
		return &fs.PathError{Op: "remove", Path: res.Path, Err: fs.ErrNotExist}
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})

	report, err := Reap(context.Background(), st, idx, true /*apply*/, ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	// Without this the test passes for the wrong reason if Reap ever bypasses
	// the seam: the real os.Remove succeeds and produces the same visible
	// outcome, so ENOENT would never actually be exercised.
	if !stubbed {
		t.Fatal("Reap did not route deletion through deleteResourceFn; the ENOENT branch was never reached")
	}
	if len(report.Failed) != 0 {
		t.Errorf("a path someone else reclaimed was reported as a failure: %v", report.Failed)
	}
	if len(report.Deleted) != 1 || report.Deleted[0] != path {
		t.Errorf("Deleted = %v, want exactly [%s] — a completed reclamation must still count", report.Deleted, path)
	}
}
