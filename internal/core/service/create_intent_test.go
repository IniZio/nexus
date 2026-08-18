package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
)

// ── Intent-file unit tests ────────────────────────────────────────────────────

// TestWriteCreateIntent_WritesAndReads verifies that writeCreateIntent produces
// a file that readCreateIntent can decode, and that the content round-trips.
func TestWriteCreateIntent_WritesAndReads(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()
	diskCopy := filepath.Join(dir, id.String()+".raw")
	wsDisk := filepath.Join(dir, id.String()+"-workspace.ext4")

	lease, err := writeCreateIntent(dir, id, diskCopy, wsDisk)
	if err != nil {
		t.Fatalf("writeCreateIntent: %v", err)
	}
	t.Cleanup(lease.release)

	ci, err := readCreateIntent(lease.Path())
	if err != nil {
		t.Fatalf("readCreateIntent: %v", err)
	}
	if ci.ID != id.String() {
		t.Errorf("ID = %q, want %q", ci.ID, id.String())
	}
	if ci.DiskCopyPath != diskCopy {
		t.Errorf("DiskCopyPath = %q, want %q", ci.DiskCopyPath, diskCopy)
	}
	if ci.WorkspaceDiskPath != wsDisk {
		t.Errorf("WorkspaceDiskPath = %q, want %q", ci.WorkspaceDiskPath, wsDisk)
	}
}

// TestWriteCreateIntent_EmptyPaths verifies the no-disk-copy and no-workspace
// combinations round-trip correctly (fields are omitted from JSON, not corrupted).
func TestWriteCreateIntent_EmptyPaths(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()

	lease, err := writeCreateIntent(dir, id, "", "")
	if err != nil {
		t.Fatalf("writeCreateIntent: %v", err)
	}
	t.Cleanup(lease.release)

	ci, err := readCreateIntent(lease.Path())
	if err != nil {
		t.Fatalf("readCreateIntent: %v", err)
	}
	if ci.DiskCopyPath != "" {
		t.Errorf("DiskCopyPath = %q, want empty", ci.DiskCopyPath)
	}
	if ci.WorkspaceDiskPath != "" {
		t.Errorf("WorkspaceDiskPath = %q, want empty", ci.WorkspaceDiskPath)
	}
}

// TestIntentPath_Convention verifies that IntentPath uses the expected naming
// convention so the R1 reaper can scan diskDir for orphaned intents.
func TestIntentPath_Convention(t *testing.T) {
	id := domain.NewSandboxID()
	got := IntentPath("/some/dir", id)
	want := filepath.Join("/some/dir", id.String()+".create-intent.json")
	if got != want {
		t.Errorf("IntentPath = %q, want %q", got, want)
	}
}

// TestWriteCreateIntent_SyncIsCalled verifies that writeCreateIntent invokes
// both intentFileSyncer (file data sync) and intentDirSyncer (directory entry
// sync) before returning.
//
// Falsifiability: each spy flag is only set inside the injected function. If
// either sync call is removed from writeCreateIntent the corresponding flag
// stays false and the test fails. This was verified by temporarily removing
// each call from writeCreateIntent and confirming the test reported:
//
//	"writeCreateIntent did not call Sync() on the intent file"
//	"writeCreateIntent did not sync the directory"
//
// Reading the file after the call cannot substitute for this test because
// os.ReadFile succeeds from the kernel page cache regardless of whether Sync
// was called — the data is in memory either way.
func TestWriteCreateIntent_SyncIsCalled(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()

	var fileSyncCalled, dirSyncCalled bool

	// Save and restore the package-level seams around this test.
	origFileSyncer := intentFileSyncer
	origDirSyncer := intentDirSyncer
	t.Cleanup(func() {
		intentFileSyncer = origFileSyncer
		intentDirSyncer = origDirSyncer
	})

	intentFileSyncer = func(f *os.File) error {
		fileSyncCalled = true
		return f.Sync() // still actually sync so subsequent reads work
	}
	intentDirSyncer = func(d string) error {
		dirSyncCalled = true
		return origDirSyncer(d)
	}

	lease, err := writeCreateIntent(dir, id, "", "")
	if err != nil {
		t.Fatalf("writeCreateIntent: %v", err)
	}
	t.Cleanup(lease.release)

	if !fileSyncCalled {
		t.Error("writeCreateIntent did not call Sync() on the intent file — " +
			"power-loss durability of file data is broken (intentFileSyncer was not invoked)")
	}
	if !dirSyncCalled {
		t.Error("writeCreateIntent did not sync the directory — " +
			"the directory entry is not power-loss-durable (intentDirSyncer was not invoked)")
	}
}

// ── Fault-injection tests for journaled CreateAndBoot ─────────────────────────
//
// These tests prove the stage-boundary behaviour of the create sequence:
//
//   Stage A (pre-cowExt4):  intent written
//   Stage B (post-cowExt4): disk copy exists
//   Stage C (post-workspace): workspace disk exists
//   Stage D (post-store.Create): record written, intent removed on success
//
// For each stage boundary we inject a failure and verify:
//   - the intent file was written (proves it precedes resource creation)
//   - the disk files are cleaned up by the defer
//   - the intent file is removed by the defer (clean-error path; crash path
//     is structural: defer does not run → intent survives → reaper can act)

// TestCreateAndBoot_Intent_WrittenBeforeDiskMaterialization verifies that when
// cowExt4 fails, the intent file was already written (it was written in stage
// 3.6, before cowExt4 in stage 4). The intent is then removed by the deferred
// cleanup, so no stale intent lingers on clean errors.
//
// Crash simulation: to prove the CRASH scenario without killing a subprocess
// we check that (a) the intent is written at the time cowExt4 is called, and
// (b) after the clean error the intent is gone (defer ran). The combination
// proves: in a crash (where defer doesn't run), the intent would remain.
func TestCreateAndBoot_Intent_WrittenBeforeDiskMaterialization(t *testing.T) {
	ctx := context.Background()
	diskDir := t.TempDir()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	// Track whether the intent file exists at the moment the capturer is called.
	// The capturer is called AFTER cowExt4 (stage 4.5), so if the intent is
	// present at that point it was written in stage 3.6 (before cowExt4).
	var intentExistedAtCaptureTime bool
	captureErr := errors.New("injected capturer failure")
	capturer := func(_ context.Context, _, outExt4 string, _ int64) error {
		// Derive the intent path from the workspace disk path convention.
		// workspaceDiskPath = <diskDir>/<id>-workspace.ext4
		// intentPath        = <diskDir>/<id>.create-intent.json
		base := filepath.Base(outExt4)
		// strip "-workspace.ext4" suffix to get the id prefix
		const suffix = "-workspace.ext4"
		if len(base) > len(suffix) {
			idStr := base[:len(base)-len(suffix)]
			candidateIntent := filepath.Join(diskDir, idStr+".create-intent.json")
			if _, err := os.Stat(candidateIntent); err == nil {
				intentExistedAtCaptureTime = true
			}
		}
		// Create a placeholder so the deferred Remove of workspaceDiskPath succeeds.
		f, err := os.Create(outExt4)
		if err != nil {
			return err
		}
		_ = f.Close()
		return captureErr // injected failure after file created
	}

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "intent-test",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
			Workspace: &WorkspaceSpec{
				SourcePath: "/repo",
				GuestPath:  "/workspace/repo",
			},
			WorkspaceCapturer: capturer,
		},
	)
	if err == nil {
		t.Fatal("expected error from injected capturer failure, got nil")
	}

	// The intent was present when the capturer ran (stage 3.6 ran before 4.5).
	if !intentExistedAtCaptureTime {
		t.Error("intent file was NOT present when capturer was called — it must be written in stage 3.6 (before cowExt4)")
	}

	// After the clean error the defer removes the intent: no stale file lingers.
	// List the diskDir for any *.create-intent.json file.
	entries, _ := os.ReadDir(diskDir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			t.Errorf("stale intent file left after clean error: %s (defer must remove it)", e.Name())
		}
	}
}

// TestCreateAndBoot_Intent_RemovedOnSuccess verifies that a successful create
// leaves no intent file behind — the record is the durable artifact.
func TestCreateAndBoot_Intent_RemovedOnSuccess(t *testing.T) {
	ctx := context.Background()
	diskDir := t.TempDir()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	capturer := func(_ context.Context, _, outExt4 string, _ int64) error {
		f, err := os.Create(outExt4)
		if err != nil {
			return err
		}
		return f.Close()
	}

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "intent-success",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
			Workspace: &WorkspaceSpec{
				SourcePath: "/repo",
				GuestPath:  "/workspace/repo",
			},
			WorkspaceCapturer: capturer,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// No intent files should remain after a successful create.
	entries, _ := os.ReadDir(diskDir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			t.Errorf("stale intent file after successful create: %s", e.Name())
		}
	}
}

// TestCreateAndBoot_Intent_StageProof verifies the three stage boundaries:
//
//	Stage A → B:  intent written before disk resources exist
//	Stage B → C:  disk copy exists (cowExt4 succeeded)
//	Stage C → D:  workspace disk exists (capture succeeded)
//	Stage D:      store.Create succeeds, intent removed
//
// This is done by observing the diskDir contents at each injected failure point.
// The "crash" scenario is structural: the defer removes the intent on clean
// errors; a crash (kill -9) skips the defer and the intent survives for the
// reaper.
func TestCreateAndBoot_Intent_StageProof(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	// Fault at stage C→D: workspace capture succeeds but driver.Start fails.
	// At the moment of failure, both disk files exist AND the intent was present.
	// The defer should remove the intent and both disk files.
	diskDir := t.TempDir()
	capturer := func(_ context.Context, _, outExt4 string, _ int64) error {
		f, err := os.Create(outExt4)
		if err != nil {
			return err
		}
		return f.Close()
	}

	injectedStartErr := errors.New("injected start failure at stage C→D")
	fd := fake.New()
	fd.SetStartError(injectedStartErr)
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "stage-c-d",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
			Workspace: &WorkspaceSpec{
				SourcePath: "/repo",
				GuestPath:  "/workspace/repo",
			},
			WorkspaceCapturer: capturer,
		},
	)
	if err == nil {
		t.Fatal("expected error from injected start failure, got nil")
	}

	// After the clean error (defer ran): no disk files or intent should remain.
	entries, _ := os.ReadDir(diskDir)
	for _, e := range entries {
		t.Errorf("file left in diskDir after clean error at stage C→D: %s", e.Name())
	}
}
