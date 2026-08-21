package volumestore_test

// Deadline tests for Create, Rm, and Detach acquisition paths.
//
// TryExclusive uses non-blocking LOCK_EX|LOCK_NB with a per-iteration ctx
// check, so a caller with a short-deadline context gets a context.DeadlineExceeded
// error within one retry interval (~5 ms) instead of parking in the kernel.
//
// These tests verify C2: that a blocked acquisition surfaces as a deadline
// error, not a hung process, when a contending fd holds the lock.
//
// Mutation check discipline:
//   Revert one site back to lk.Exclusive(context.Background()) and re-run:
//   the test hangs (never returns) — a hang IS the failure signal.  Use
//   -timeout 10s to make the hang observable as a test failure in CI.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// holdLock opens the lock file at path from a SEPARATE file descriptor and
// acquires LOCK_EX on it. Returns a release function that unlocks and closes.
// The separate open() call creates a distinct open file description, so flock
// exclusion applies even within the same process.
func holdLock(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("holdLock: open %s: %v", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		t.Fatalf("holdLock: flock: %v", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

// TestRm_DeadlineWhenLocked proves that Rm returns a deadline error rather
// than parking in the kernel when the per-volume advisory lock is held by
// another file descriptor.
//
// Mutation check: revert store.go:Rm to lk.Exclusive(context.Background()) and
// this test hangs indefinitely (requires -timeout 10s to surface in CI).
func TestRm_DeadlineWhenLocked(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.Create(ctx, "rm-deadline-vol", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Hold the lock from a separate fd — Rm must not park waiting for it.
	release := holdLock(t, s.LockPath("rm-deadline-vol"))
	defer release()

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := s.Rm(deadlineCtx, "rm-deadline-vol")
	if err == nil {
		t.Fatal("Rm with held lock: expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Rm: expected DeadlineExceeded in error chain, got: %v", err)
	}
}

// TestDetach_DeadlineWhenLocked proves that Detach returns a deadline error
// rather than parking in the kernel when the per-volume advisory lock is held.
//
// Mutation check: revert store.go:Detach to lk.Exclusive(context.Background())
// and this test hangs indefinitely (requires -timeout 10s to surface in CI).
func TestDetach_DeadlineWhenLocked(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.Create(ctx, "det-deadline-vol", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Attach(ctx, "det-deadline-vol", "sb-test"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Hold the lock from a separate fd — Detach must not park waiting for it.
	release := holdLock(t, s.LockPath("det-deadline-vol"))
	defer release()

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := s.Detach(deadlineCtx, "det-deadline-vol", "sb-test")
	if err == nil {
		t.Fatal("Detach with held lock: expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Detach: expected DeadlineExceeded in error chain, got: %v", err)
	}
}

// TestCreate_DeadlineWhenLocked proves that Create returns a bounded
// DeadlineExceeded error rather than spinning indefinitely when the per-volume
// advisory lock is held by a contending file descriptor.
//
// The ctx passed to Create is context.Background() — the same shape as both
// production callers (cmd_volume.go and service/create.go): cancellable via
// signal but carrying no deadline of its own.  The bound comes from inside
// Create itself (TBD-PD-42), so this test validates that the internal bound
// is real and not merely inherited from the caller.
//
// Mutation check: remove the context.WithTimeout inside volumestore.Create and
// re-run with -timeout 30s.  The test hangs and the suite times out — a hang
// IS the failure signal.  Restore the bound and the test returns with
// DeadlineExceeded within ~10 s.
func TestCreate_DeadlineWhenLocked(t *testing.T) {
	s := newStore(t)
	volName := "create-deadline-vol"

	// Pre-create the volume directory and lock file so we can open a competing
	// fd BEFORE calling Create.  Create itself calls os.MkdirAll and then
	// store.OpenLock(O_CREATE), so we replicate that setup here.
	lockPath := s.LockPath(volName)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("create lock file: %v", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		_ = lf.Close()
		t.Fatalf("flock: %v", err)
	}
	defer func() {
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		_ = lf.Close()
	}()

	// Drive Create with an unbounded context — production's shape.  The bound
	// must come from inside Create (TBD-PD-42), not from the caller.
	_, err = s.Create(context.Background(), volName, volumestore.KindDir, 0, "")
	if err == nil {
		t.Fatal("Create with held lock: expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Create: expected DeadlineExceeded in error chain, got: %v", err)
	}
}

