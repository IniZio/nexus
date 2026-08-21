package service

// TestD2_DirVolumeLeaseHeldAcrossStoreCreate — wiring test for D2.
//
// Acceptance criterion: this test FAILS when the else branch of the named-volume
// loop in CreateAndBoot is reverted from vs.AttachLocked to vs.Attach.
//
// Mechanism:
//
//   The testHookBeforeStoreCreate seam fires inside CreateAndBoot while
//   volumeLeases is still populated (locks not yet released).  The hook runs
//   Prune with Apply=true and IncludeDetached=true against an empty store (no
//   sandbox record exists yet — the hook fires BEFORE svc.store.Create).
//
//   With the fix (AttachLocked): the per-volume exclusive flock is still held
//   by the lock in volumeLeases.  Prune probes the lock, sees EWOULDBLOCK, and
//   skips the volume → DetachedDeleted is empty → volume survives → PASS.
//
//   With the revert (Attach): the flock was released when Attach returned.
//   Prune acquires the lock cleanly, finds no live sandbox record, classifies
//   the volume as detached, and deletes it → DetachedDeleted is non-empty →
//   FAIL.
//
// The test does NOT call AttachLocked directly; it drives the full
// CreateAndBoot path so that the wiring in create.go is the thing under test.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

func TestD2_DirVolumeLeaseHeldAcrossStoreCreate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Volume store in its own temp dir.
	vsRoot := filepath.Join(t.TempDir(), "volumes")
	vs := volumestore.New(vsRoot)

	const volName = "d2w-test-vol"
	if _, err := vs.Create(ctx, volName, volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("vs.Create: %v", err)
	}

	// Sandbox store — shared between the service and the hook's Prune call.
	// It is EMPTY when the hook fires (svc.store.Create has not run yet).
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	fd := fake.New()
	svc := New(st, fd, lifecycle.New())

	// hookResult captures what Prune did during the D2 window.
	type hookResult struct {
		detachedDeleted []string
		err             error
	}
	var result hookResult
	var hookRan bool

	cleanup := SetHookBeforeStoreCreate(func() error {
		hookRan = true
		res, pruneErr := vs.Prune(ctx, st, volumestore.PruneOptions{
			Apply:           true,
			IncludeDetached: true,
		})
		if pruneErr != nil {
			result.err = pruneErr
			return nil // don't abort CreateAndBoot; let the assertion below report it
		}
		result.detachedDeleted = res.DetachedDeleted
		return nil
	})
	defer cleanup()

	// Drive the service create path. RootfsPath skips the CoW copy step.
	// The fake driver handles Start without a real VM. noopProbe passes.
	diskDir := t.TempDir()
	_, createErr := CreateAndBoot(ctx, svc, nil, fakeDriverFactory(fd), noopProbe,
		"proj", "d2wbox",
		CreateAndBootOptions{
			Image:             ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			Volumes:           vs,
			NamedVolumeMounts: []NamedVolumeMount{{Name: volName, Kind: volumestore.KindDir}},
			DiskDir:           diskDir,
			DiskPreflight:     func(_ string, _ int64, _ string) (*DiskPreflightResult, error) { return &DiskPreflightResult{}, nil },
		},
	)
	// CreateAndBoot may fail for reasons unrelated to the D2 window (e.g.
	// driver.Start behaves unexpectedly). Any Prune-related failure is
	// detected via result.detachedDeleted below, independent of createErr.
	_ = createErr

	if !hookRan {
		t.Fatal("D2 hook did not fire — seam not wired or CreateAndBoot returned before reaching it")
	}
	if result.err != nil {
		t.Fatalf("Prune inside D2 hook returned error: %v", result.err)
	}
	// @verifies D2 (dir-volume else-branch holds flock across store.Create)
	if len(result.detachedDeleted) > 0 {
		t.Errorf("D2 REGRESSION: Prune deleted dir volume in the store.Create window (lock not held): %v\n"+
			"This means the else branch calls vs.Attach (lock released on return) instead of vs.AttachLocked.",
			result.detachedDeleted)
	}
}
