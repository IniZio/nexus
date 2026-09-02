package service

// Tests for scratch-disk ordering: SD-AC1, SD-AC9, SD-AC12.
//
// All tests invoke the real CreateAndBoot. The testing seam is the
// DriverFactory: a capturing closure records the ExtraDisks it receives so
// assertions do not touch any VM or vsock plumbing.
//
// No named-volume mounts are used so this file has no volumestore dependency.
// Images use ImageSpec.RootfsPath (no cache needed) matching the pattern in
// vol_hold_reorder_test.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
)

// TestScratchDisk_IsLast_SD_AC1 verifies that the scratch disk is appended
// AFTER caller-supplied ExtraDisks and the workspace disk.
//
// Expected factory ExtraDisks order (4 total):
//
//	[0] callerDisk0
//	[1] callerDisk1
//	[2] workspace disk        ← stubCapturer writes the real path here
//	[3] scratch disk          ← must carry suffix "-scratch.ext4"
//
// Mutation targets:
//
//	MUTATION-A (append→prepend): scratch lands at [0], not [3].
//	  Caught by assertion (b): capturedDisks[3] ends with "-scratch.ext4" → FAIL.
//
//	MUTATION-B (scratch removed): only 3 disks reach the factory.
//	  Caught by assertion (a): len == 4 → FAIL.
//
//	MUTATION-C (workspace after scratch): capturedDisks[2] is scratch, not workspace.
//	  Caught by assertion (c): capturedDisks[2].Path == capturedWorkspacePath → FAIL.
//
//	MUTATION-D (caller disks shifted): caller disk positions change.
//	  Caught by assertions (d) and (e).
// @verifies SD-AC1
func TestScratchDisk_IsLast_SD_AC1(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	diskDir := t.TempDir()

	// Two caller-supplied disks at fake paths.
	// CreateAndBoot does not stat ExtraDisks before forwarding them to the
	// factory, so fake paths work (confirmed by TestCreateAndBoot_ExtraDisksReachFactory).
	callerDisk0 := ExtraDisk{Path: "/fake/caller-vdb.ext4"}
	callerDisk1 := ExtraDisk{Path: "/fake/caller-vdc.ext4"}

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = make([]ExtraDisk, len(extraDisks))
		copy(capturedDisks, extraDisks)
		return fake.New(), nil
	})

	fd := fake.New()
	svc := newTestSvc(t, fd)

	// stubCapturer creates a real file at outExt4 (an empty placeholder) and
	// records the path so we can verify the workspace disk is at index 2.
	var capturedWorkspacePath string
	_, err := CreateAndBoot(ctx, svc, nil, capturingFactory, noopProbe,
		"proj", "scratch-ac1",
		CreateAndBootOptions{
			Image:   ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			DiskDir: diskDir,
			ExtraDisks: []ExtraDisk{callerDisk0, callerDisk1},
			Workspace: &WorkspaceSpec{
				SourcePath: "/host/repo",
				GuestPath:  "/workspace/repo",
			},
			WorkspaceCapturer: stubCapturer(nil, &capturedWorkspacePath, nil),
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// (a) Catches MUTATION-B (scratch removed entirely).
	if len(capturedDisks) != 4 {
		t.Fatalf("factory received %d extra disks, want 4 [callerDisk0, callerDisk1, workspace, scratch]", len(capturedDisks))
	}

	// (b) Catches MUTATION-A (scratch prepended instead of appended):
	//     under prepend, scratch lands at [0] so [3] holds callerDisk0's path.
	if !strings.HasSuffix(capturedDisks[3].Path, "-scratch.ext4") {
		t.Errorf("capturedDisks[3].Path = %q, want suffix \"-scratch.ext4\" (scratch must be last)", capturedDisks[3].Path)
	}

	// (c) Catches MUTATION-C (workspace and scratch positions swapped):
	//     if they are swapped, [2] holds the scratch path, not the workspace path.
	if capturedDisks[2].Path != capturedWorkspacePath {
		t.Errorf("capturedDisks[2].Path = %q, want workspace path %q (workspace must precede scratch)", capturedDisks[2].Path, capturedWorkspacePath)
	}

	// (d) Catches MUTATION-D for callerDisk0.
	if capturedDisks[0].Path != callerDisk0.Path {
		t.Errorf("capturedDisks[0].Path = %q, want %q (caller disk 0 must not shift)", capturedDisks[0].Path, callerDisk0.Path)
	}

	// (e) Catches MUTATION-D for callerDisk1.
	if capturedDisks[1].Path != callerDisk1.Path {
		t.Errorf("capturedDisks[1].Path = %q, want %q (caller disk 1 must not shift)", capturedDisks[1].Path, callerDisk1.Path)
	}
}

// TestScratchDisk_BuilderDisksUnaffected_SD_AC9 verifies that builder-style
// sandboxes (Workspace == nil) receive exactly the caller-supplied ExtraDisks
// with no scratch disk appended.
//
// Builder disk arithmetic hard-codes device letter assignments:
//
//	ExtraDisks[0] → /dev/vdb  (context disk)
//	ExtraDisks[1] → /dev/vdc  (artifact disk)
//	ExtraDisks[2] → /dev/vdd  (cache disk)
//
// Appending a scratch disk would silently shift /dev/vdd to /dev/vde, breaking
// cache hit rates for every subsequent build. This test proves that path is
// unchanged.
// @verifies SD-AC9
func TestScratchDisk_BuilderDisksUnaffected_SD_AC9(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Three caller disks mimicking a builder sandbox layout.
	callerDisks := []ExtraDisk{
		{Path: "/fake/builder-vdb.ext4"}, // context disk
		{Path: "/fake/builder-vdc.ext4"}, // artifact disk
		{Path: "/fake/builder-vdd.ext4"}, // cache disk → /dev/vdd
	}

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = make([]ExtraDisk, len(extraDisks))
		copy(capturedDisks, extraDisks)
		return fake.New(), nil
	})

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err := CreateAndBoot(ctx, svc, nil, capturingFactory, noopProbe,
		"proj", "scratch-ac9",
		CreateAndBootOptions{
			Image:      ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			ExtraDisks: callerDisks,
			// Workspace intentionally nil — builder sandbox style.
			// SD-AC12a guarantees nil Workspace → no scratch, so builder
			// disk positions are stable.
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// Exactly 3 disks; scratch must NOT appear.
	if len(capturedDisks) != 3 {
		t.Fatalf("factory received %d extra disks for builder sandbox, want 3 (scratch must not be appended when Workspace=nil)", len(capturedDisks))
	}
	for i, want := range callerDisks {
		if capturedDisks[i].Path != want.Path {
			t.Errorf("capturedDisks[%d].Path = %q, want %q (builder disk order must not shift)", i, capturedDisks[i].Path, want.Path)
		}
	}
}

// TestScratchDisk_NilWorkspace_NoScratch_SD_AC12a verifies that sandboxes with
// opts.Workspace == nil receive no scratch disk. The factory's ExtraDisks must
// be byte-identical to before this feature was added.
// @verifies SD-AC12
func TestScratchDisk_NilWorkspace_NoScratch_SD_AC12a(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = make([]ExtraDisk, len(extraDisks))
		copy(capturedDisks, extraDisks)
		return fake.New(), nil
	})

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err := CreateAndBoot(ctx, svc, nil, capturingFactory, noopProbe,
		"proj", "scratch-ac12a",
		CreateAndBootOptions{
			Image: ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			// Workspace nil — no workspace, no scratch disk expected.
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	for _, d := range capturedDisks {
		if strings.HasSuffix(d.Path, "-scratch.ext4") {
			t.Errorf("unexpected scratch disk %q with nil Workspace (must not add scratch when Workspace=nil)", d.Path)
		}
	}
}

// TestScratchDisk_NoScratchDiskFlag_SD_AC12b verifies that setting
// NoScratchDisk=true suppresses the scratch disk even when Workspace is
// non-nil, and that the workspace disk IS still present.
//
// This tests the explicit off-switch: operators may need deterministic disk
// layouts without the guest /tmp disk for specialised configurations.
// @verifies SD-AC12
func TestScratchDisk_NoScratchDiskFlag_SD_AC12b(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	diskDir := t.TempDir()

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = make([]ExtraDisk, len(extraDisks))
		copy(capturedDisks, extraDisks)
		return fake.New(), nil
	})

	fd := fake.New()
	svc := newTestSvc(t, fd)

	var capturedWorkspacePath string
	_, err := CreateAndBoot(ctx, svc, nil, capturingFactory, noopProbe,
		"proj", "scratch-ac12b",
		CreateAndBootOptions{
			Image:   ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			DiskDir: diskDir,
			Workspace: &WorkspaceSpec{
				SourcePath: "/host/repo",
				GuestPath:  "/workspace/repo",
			},
			WorkspaceCapturer: stubCapturer(nil, &capturedWorkspacePath, nil),
			NoScratchDisk:     true,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// No scratch disk despite non-nil Workspace.
	for _, d := range capturedDisks {
		if strings.HasSuffix(d.Path, "-scratch.ext4") {
			t.Errorf("unexpected scratch disk %q with NoScratchDisk=true", d.Path)
		}
	}

	// Workspace IS still processed — its disk must appear.
	found := false
	for _, d := range capturedDisks {
		if d.Path == capturedWorkspacePath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("workspace disk %q not found in factory ExtraDisks %v (Workspace must still be processed when NoScratchDisk=true)", capturedWorkspacePath, capturedDisks)
	}
}

// TestScratchDisk_LiveMountWorkspace_GetsScratch verifies D-SD-05: a sandbox
// built via the herdrWorktreeSandbox path (Workspace==nil, LiveMounts entry at
// /workspace) receives a scratch disk. This is the exact shape that was always
// missing a scratch disk before the hostWorkspacePath predicate fix.
//
// Production shape: CreateAndBootOptions{Workspace: nil, LiveMounts: [{GuestPath:"/workspace", ...}]}.
// Workspace==nil means no workspace capturer runs; the only disk reaching the
// factory must be the scratch disk.
//
// Mutation targets:
//
//	MUTATION-A (revert gate): change `_, hasWorkspace := hostWorkspacePath(opts)` back
//	  to `opts.Workspace != nil` → hasWorkspace=false with Workspace=nil → no scratch disk
//	  appended → len(capturedDisks)==0 → FAIL.
//
//	MUTATION-B (remove scratch append): drop the opts.ExtraDisks append → len==0 → FAIL.
// @verifies D-SD-05
func TestScratchDisk_LiveMountWorkspace_GetsScratch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	diskDir := t.TempDir()

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = make([]ExtraDisk, len(extraDisks))
		copy(capturedDisks, extraDisks)
		return fake.New(), nil
	})

	fd := fake.New()
	svc := newTestSvc(t, fd)

	// herdrWorktreeSandbox option shape: Workspace==nil, LiveMounts with /workspace.
	_, err := CreateAndBoot(ctx, svc, nil, capturingFactory, noopProbe,
		"proj", "scratch-lm-ws",
		CreateAndBootOptions{
			Image:   ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			DiskDir: diskDir,
			// Workspace intentionally nil — the worktree-sandbox path.
			LiveMounts: []domain.LiveMount{
				{HostPath: "/host/worktree", GuestPath: "/workspace"},
			},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// (a) Exactly one disk: the scratch disk. No workspace capture runs when Workspace==nil.
	if len(capturedDisks) != 1 {
		t.Fatalf("factory received %d extra disks, want 1 (scratch only; no workspace capture for nil Workspace)", len(capturedDisks))
	}

	// (b) That disk IS the scratch disk. Catches MUTATION-A and MUTATION-B.
	if !strings.HasSuffix(capturedDisks[0].Path, "-scratch.ext4") {
		t.Errorf("capturedDisks[0].Path = %q, want suffix \"-scratch.ext4\" (scratch disk required for LiveMount workspace)", capturedDisks[0].Path)
	}
}

// TestScratchDisk_LiveMountWorkspace_NoScratchDiskSuppresses verifies that
// NoScratchDisk=true suppresses the scratch disk even when the workspace is
// conveyed via a /workspace LiveMount rather than opts.Workspace.
// @verifies D-SD-05
func TestScratchDisk_LiveMountWorkspace_NoScratchDiskSuppresses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = make([]ExtraDisk, len(extraDisks))
		copy(capturedDisks, extraDisks)
		return fake.New(), nil
	})

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err := CreateAndBoot(ctx, svc, nil, capturingFactory, noopProbe,
		"proj", "scratch-lm-noscratch",
		CreateAndBootOptions{
			Image: ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			LiveMounts: []domain.LiveMount{
				{HostPath: "/host/worktree", GuestPath: "/workspace"},
			},
			NoScratchDisk: true,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	for _, d := range capturedDisks {
		if strings.HasSuffix(d.Path, "-scratch.ext4") {
			t.Errorf("unexpected scratch disk %q with NoScratchDisk=true and LiveMount workspace", d.Path)
		}
	}
}
