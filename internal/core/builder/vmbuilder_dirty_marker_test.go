// Package builder — BuildInVM dirty-marker fence tests.
//
// These tests verify that the cache-disk dirty marker is correctly managed
// through the full BuildInVM lifecycle, not just at the markCacheDisk*
// primitive level. The advisor mutation M3 (changing "if tearErr == nil" to
// "if true" in BuildInVM step 3.5) was previously undetected because no test
// drove markCacheDiskClean through BuildInVM. This file closes that gap.
//
// Test matrix:
//   - sync fails    → dirty marker SURVIVES (disk must not be trusted)
//   - sync succeeds → dirty marker CLEARED  (warm reuse allowed)
//   - start fails   → dirty marker CLEARED  (disk never attached, safe to reuse)
package builder

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
)

// ── minimal fakeBuilderStore ──────────────────────────────────────────────────

type markerFakeStore struct {
	mu      sync.Mutex
	records map[domain.SandboxID]domain.Sandbox
}

func newMarkerFakeStore() *markerFakeStore {
	return &markerFakeStore{records: make(map[domain.SandboxID]domain.Sandbox)}
}

var _ BuilderStore = (*markerFakeStore)(nil)

func (s *markerFakeStore) Create(_ context.Context, sb domain.Sandbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[sb.ID] = sb
	return nil
}

func (s *markerFakeStore) Update(_ context.Context, id domain.SandboxID, fn func(*domain.Sandbox) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return errors.New("markerFakeStore.Update: not found")
	}
	if err := fn(&rec); err != nil {
		return err
	}
	s.records[id] = rec
	return nil
}

func (s *markerFakeStore) Delete(_ context.Context, id domain.SandboxID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, id)
	return nil
}

// ── helpers ────────────────────────────────────────────────────────────────────

// makeFakeCacheDisk creates a temp image file and returns a CacheDiskSpec.
func makeFakeCacheDisk(t *testing.T) CacheDiskSpec {
	t.Helper()
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "cache.ext4")
	if err := os.WriteFile(imgPath, []byte("fake ext4"), 0o644); err != nil {
		t.Fatalf("makeFakeCacheDisk: %v", err)
	}
	return CacheDiskSpec{
		ImagePath: imgPath,
		MountPath: "/var/lib/buildkit",
	}
}

// setDirtyMarker writes the dirty marker and asserts it is present.
func setDirtyMarker(t *testing.T, spec CacheDiskSpec) {
	t.Helper()
	if err := markCacheDiskDirty(spec.ImagePath); err != nil {
		t.Fatalf("setDirtyMarker: %v", err)
	}
	if !cacheDiskIsDirty(spec.ImagePath) {
		t.Fatal("setDirtyMarker: cacheDiskIsDirty false immediately after mark")
	}
}

// runMarkerTest calls BuildInVM with the given spec and execFn, using a fresh
// fake driver (no start error unless configured via drv before passing) and a
// fresh store. The error is returned for inspection but is usually ignored in
// marker tests — only the marker state matters.
func runMarkerTest(ctx context.Context, drv *fake.FakeDriver, spec BuilderVMSpec, execFn GuestExecFn) error {
	_, err := BuildInVM(ctx, drv, spec, nil, execFn, newMarkerFakeStore())
	return err
}

// syncFailExecFn returns an execFn where sync exits non-zero (simulating EIO —
// data did not reach the host). The build step returns exit 0 so the failure
// is isolated to the sync.
func syncFailExecFn() GuestExecFn {
	return func(_ context.Context, argv []string, _ io.Writer) (int32, error) {
		isSync := len(argv) == 1 &&
			(argv[0] == "/bin/sync" || argv[0] == "/usr/bin/sync" || argv[0] == "sync")
		if isSync {
			return 1, nil // non-zero exit: sync failed
		}
		return 0, nil // builder-role succeeds
	}
}

// syncOKExecFn returns an execFn where every command exits 0.
func syncOKExecFn() GuestExecFn {
	return func(_ context.Context, argv []string, _ io.Writer) (int32, error) {
		return 0, nil
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestBuildInVM_SyncFail_DirtyMarkerSurvives is the primary fence test: when
// the guest sync exits non-zero (e.g. EIO on the virtio-blk device, meaning
// data did NOT reach the host), the dirty marker must not be cleared.
//
// Mutation proof (M3): changing "if tearErr == nil" to "if true" in BuildInVM
// step 3.5 makes this test RED.
// Mutation proof (FIX 1): restoring guestSync to discard the exit code also
// makes this test RED.
func TestBuildInVM_SyncFail_DirtyMarkerSurvives(t *testing.T) {
	cd := makeFakeCacheDisk(t)
	setDirtyMarker(t, cd)

	drv := fake.New()
	spec := BuilderVMSpec{
		RootfsDiskPath:   "/dev/null",
		ArtifactDiskPath: "",
		CacheDisks:       []CacheDiskSpec{cd},
	}

	_ = runMarkerTest(context.Background(), drv, spec, syncFailExecFn())

	// CRITICAL: dirty marker must survive a failed sync.
	if !cacheDiskIsDirty(cd.ImagePath) {
		t.Fatal("dirty marker was cleared despite sync failure — " +
			"false-clean: poisoned disk would be served as warm cache")
	}
}

// TestBuildInVM_SyncOK_DirtyMarkerCleared is the mirror case: when sync
// succeeds (exit 0) and the VMM stops cleanly, BuildInVM must clear the dirty
// marker so the next lease can reuse the warm cache.
func TestBuildInVM_SyncOK_DirtyMarkerCleared(t *testing.T) {
	cd := makeFakeCacheDisk(t)
	setDirtyMarker(t, cd)

	drv := fake.New()
	spec := BuilderVMSpec{
		RootfsDiskPath:   "/dev/null",
		ArtifactDiskPath: "", // empty → harvest error, but tearErr is nil
		CacheDisks:       []CacheDiskSpec{cd},
	}

	// BuildInVM returns an error (missing ArtifactDiskPath), but tearErr is nil
	// because sync succeeded and VMM stopped cleanly. The marker must be cleared.
	_ = runMarkerTest(context.Background(), drv, spec, syncOKExecFn())

	if cacheDiskIsDirty(cd.ImagePath) {
		t.Fatal("dirty marker was NOT cleared after clean sync — " +
			"warm cache will be unnecessarily wiped on next lease")
	}
}

// TestBuildInVM_CancelledBuild_DirtyMarkerSurvives proves that when the build
// context is cancelled while sync SUCCEEDS (exit 0, tearErr == nil), the dirty
// marker is NOT cleared.
//
// This is the actual hole: lc.SyncAndStop() runs guestSync on
// context.Background() (not on the caller's ctx), so tearErr alone cannot
// witness caller cancellation. When ctx is cancelled (create-timeout expiry or
// Ctrl-C), buildkitd may still be mid-write when the background-context sync
// runs; sync returns exit 0, tearErr == nil, and — without the ctx.Err() guard
// — the marker is falsely cleared, poisoning the cache disk for the next lease.
//
// Mutation proof (M4): reverting the fix to "if tearErr == nil" (removing
// "&& ctx.Err() == nil") makes this test RED, because the condition is
// satisfied by tearErr == nil alone and the marker is cleared.
func TestBuildInVM_CancelledBuild_DirtyMarkerSurvives(t *testing.T) {
	cd := makeFakeCacheDisk(t)
	setDirtyMarker(t, cd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// execFn cancels the build ctx during the builder-role step (simulating a
	// create-timeout expiry or Ctrl-C), but then reports sync as exit 0
	// (simulating: guestSync ran on context.Background() and the guest's sync
	// syscall happened to return 0 despite the build being torn down mid-write).
	execFn := func(_ context.Context, argv []string, _ io.Writer) (int32, error) {
		isSync := len(argv) == 1 &&
			(argv[0] == "/bin/sync" || argv[0] == "/usr/bin/sync" || argv[0] == "sync")
		if isSync {
			// Sync reports success — this is the dangerous case.
			return 0, nil
		}
		// Builder role: cancel the caller's context to simulate timeout/kill.
		cancel()
		return 0, nil
	}

	drv := fake.New()
	spec := BuilderVMSpec{
		RootfsDiskPath:   "/dev/null",
		ArtifactDiskPath: "",
		CacheDisks:       []CacheDiskSpec{cd},
	}

	_ = runMarkerTest(ctx, drv, spec, execFn)

	// CRITICAL: dirty marker must survive even when tearErr == nil, because
	// ctx.Err() != nil means the flush is not trustworthy.
	if !cacheDiskIsDirty(cd.ImagePath) {
		t.Fatal("dirty marker was cleared despite ctx cancellation (tearErr nil, ctx cancelled) — " +
			"false-clean: poisoned disk would be served as warm cache")
	}
}

// TestBuildInVM_StartFail_DirtyMarkerCleared verifies FIX 3 (start path):
// when drv.Start fails, the cache disk was never attached to the VMM, so no
// guest writes were possible. The dirty marker must be cleared to preserve the
// warm cache for the next build.
func TestBuildInVM_StartFail_DirtyMarkerCleared(t *testing.T) {
	cd := makeFakeCacheDisk(t)
	setDirtyMarker(t, cd)

	drv := fake.New()
	injected := errors.New("injected start failure")
	drv.SetStartError(injected)

	spec := BuilderVMSpec{
		RootfsDiskPath:   "/dev/null",
		ArtifactDiskPath: "",
		CacheDisks:       []CacheDiskSpec{cd},
	}

	err := runMarkerTest(context.Background(), drv, spec, syncOKExecFn())
	if !errors.Is(err, injected) {
		t.Fatalf("want injected start error in chain; got: %v", err)
	}

	// Disk never attached → no unflushed writes → safe to clear.
	if cacheDiskIsDirty(cd.ImagePath) {
		t.Fatal("dirty marker was NOT cleared after start failure — " +
			"disk was never attached; spurious wipe on next lease")
	}
}
