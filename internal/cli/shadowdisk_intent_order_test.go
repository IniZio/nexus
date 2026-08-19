package cli

// TBD-PD-25, ordering half: a shadow disk must never exist on disk without a
// marker claiming it.
//
// The test does not inspect source or assert that a call appears. It runs a
// REAL reaper (service.Reap with apply=true) from inside the disk-creation
// callback — i.e. at the exact instant a concurrent `nexus3 reap --apply`
// would observe the half-finished create — and checks that the disks survive.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// reapNow runs a real reap in apply mode over stateRoot with an empty record
// store — the mid-create state, where no committed record names the handle.
func reapNow(t *testing.T, stateRoot string) *service.ReapReport {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	report, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	return report
}

func TestPrepareShadowDisks_SurvivesAConcurrentReapMidCreate(t *testing.T) {
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	specs := buildShadowDiskSpecs([]string{"node_modules", "dist"}, diskDir, "/workspace/app", "proj/box")
	if len(specs) != 2 {
		t.Fatalf("buildShadowDiskSpecs returned %d specs, want 2", len(specs))
	}

	// Stand in for mke2fs: write the file, then let a reaper loose while the
	// create is still running. This is the concurrency the defect needed.
	var reapRuns int
	orig := createShadowDiskFn
	t.Cleanup(func() { createShadowDiskFn = orig })
	createShadowDiskFn = func(_ context.Context, spec ShadowDisk) error {
		if err := os.WriteFile(spec.HostPath, make([]byte, 4096), 0o600); err != nil {
			return err
		}
		reapNow(t, stateRoot)
		reapRuns++
		return nil
	}

	lease, created, err := prepareShadowDisks(context.Background(), diskDir, "proj/box", specs)
	if err != nil {
		t.Fatalf("prepareShadowDisks: %v", err)
	}
	t.Cleanup(lease.Release)

	if reapRuns != 2 {
		t.Fatalf("reaper ran %d times, want 2 — the test did not exercise the window", reapRuns)
	}
	if len(created) != 2 {
		t.Fatalf("created %d disks, want 2", len(created))
	}
	for _, p := range created {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("shadow disk %s was deleted by a reap that ran mid-create: %v", filepath.Base(p), statErr)
		}
	}

	// The lease must still be held on return. The create window does not close
	// until the sandbox record is committed, which happens in CreateAndBoot,
	// well after this function returns — releasing here would leave the disks
	// neither leased nor owned for the whole of CreateAndBoot.
	if _, err := os.Stat(service.ShadowIntentPath(diskDir, "proj/box")); err != nil {
		t.Errorf("shadow intent is gone on return: the lease was released before the record was committed: %v", err)
	}
	// And it must still protect: a reap landing now must not touch the disks.
	reapNow(t, stateRoot)
	for _, p := range created {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("shadow disk %s deleted by a reap after prepareShadowDisks returned: %v", filepath.Base(p), statErr)
		}
	}
}

// A failure partway through must leave nothing: no disks, and no intent
// claiming disks that do not exist.
func TestPrepareShadowDisks_FailureLeavesNothingBehind(t *testing.T) {
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	specs := buildShadowDiskSpecs([]string{"node_modules", "dist"}, diskDir, "/workspace/app", "proj/box")

	orig := createShadowDiskFn
	t.Cleanup(func() { createShadowDiskFn = orig })
	calls := 0
	createShadowDiskFn = func(_ context.Context, spec ShadowDisk) error {
		calls++
		if calls == 2 {
			return os.ErrInvalid
		}
		return os.WriteFile(spec.HostPath, make([]byte, 4096), 0o600)
	}

	if _, _, err := prepareShadowDisks(context.Background(), diskDir, "proj/box", specs); err == nil {
		t.Fatal("prepareShadowDisks returned nil error after a disk failed")
	}
	for _, spec := range specs {
		if _, err := os.Stat(spec.HostPath); err == nil {
			t.Errorf("shadow disk %s survived a failed create", filepath.Base(spec.HostPath))
		}
	}
	if _, err := os.Stat(service.ShadowIntentPath(diskDir, "proj/box")); err == nil {
		t.Error("shadow intent survived a failed create")
	}
}
