package service_test

// TestForkWorkspaceReap_Fix verifies D-PD-80(b): the child workspace disk
// produced by the fork naming convention is parseable by ParseSandboxID, so
// ResourceIndex.List classifies it as KindDiskWorkspace and Reap reports it as
// ReapStatusOrphan when no store record exists for the child.
//
// Spec reference: shadow-disk-v2.md §7.6.
//
// Mutation proof: reverting ChildExtraDiskPath in fork.go to the old
// childID+"-"+basename(parentPath) naming causes:
//   - ParseSandboxID to fail on the stripped stem (step 3)
//   - ResourceIndex.List to skip the file (not classified as KindDiskWorkspace)
//   - Reap to return 0 orphan workspace disks (step 4)
// Both failures are demonstrated in the report alongside the passing run.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/service"
)

// TestForkWorkspaceDiskNaming_ChildNameParseable exercises cloudhypervisor.ChildExtraDiskPath
// for the workspace-disk case and asserts that ParseSandboxID succeeds on the
// stem of the returned filename (spec §7.6 steps 2–3).
func TestForkWorkspaceDiskNaming_ChildNameParseable(t *testing.T) {
	parentID := domain.NewSandboxID()
	childID := domain.NewSandboxID()

	// Simulate the parent workspace disk path as produced by CreateAndBoot.
	dir := t.TempDir()
	parentPath := filepath.Join(dir, parentID.String()+"-workspace.ext4")

	childPath := cloudhypervisor.ChildExtraDiskPath(childID, parentPath)

	// Step 2: child copy must be named <childULID>-workspace.ext4.
	wantBase := childID.String() + "-workspace.ext4"
	if got := filepath.Base(childPath); got != wantBase {
		t.Fatalf("child disk name = %q, want %q", got, wantBase)
	}

	// Step 3: ParseSandboxID on the stripped stem must succeed.
	stem := strings.TrimSuffix(filepath.Base(childPath), "-workspace.ext4")
	parsed, err := domain.ParseSandboxID(stem)
	if err != nil {
		t.Fatalf("ParseSandboxID(%q) failed: %v — stem not parseable, reaper will skip the file", stem, err)
	}
	if parsed != childID {
		t.Fatalf("ParseSandboxID returned %v, want %v", parsed, childID)
	}
}

// TestForkWorkspaceReap_OrphanClassified verifies that a child workspace disk
// named by ChildExtraDiskPath is classified as ReapStatusOrphan by the reaper
// when no store record exists for the child (spec §7.6 step 4).
func TestForkWorkspaceReap_OrphanClassified(t *testing.T) {
	parentID := domain.NewSandboxID()
	childID := domain.NewSandboxID()

	// Step 1: create a temp disks directory with a parent workspace disk.
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	parentPath := filepath.Join(disksDir, parentID.String()+"-workspace.ext4")
	mustTouch(t, parentPath)

	// Step 2: compute the child copy path the way ForkFrom would after the fix.
	childPath := cloudhypervisor.ChildExtraDiskPath(childID, parentPath)

	// Write a small amount of data so AllocatedBytes > 0.
	if err := os.WriteFile(childPath, make([]byte, 4096), 0o600); err != nil {
		t.Fatalf("write child workspace disk: %v", err)
	}

	// Store is empty — no record for childID (simulates a leaked orphan disk).
	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, false /*dry-run*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// Step 4: child workspace disk must be classified as ReapStatusOrphan.
	var found bool
	for _, e := range report.Entries {
		if e.Resource.Path == childPath {
			found = true
			if e.Status != service.ReapStatusOrphan {
				t.Errorf("child workspace disk status = %q, want %q", e.Status, service.ReapStatusOrphan)
			}
			if e.Resource.Kind != service.KindDiskWorkspace {
				t.Errorf("child workspace disk kind = %q, want %q", e.Resource.Kind, service.KindDiskWorkspace)
			}
			if e.Resource.OwnerID != childID {
				t.Errorf("child workspace disk ownerID = %v, want %v", e.Resource.OwnerID, childID)
			}
		}
	}
	if !found {
		t.Errorf("child workspace disk %s not found in reap report at all — ResourceIndex skipped it (ParseSandboxID likely failed)", filepath.Base(childPath))
	}
}
