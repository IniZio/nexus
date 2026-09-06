package leak_test

// Fork-scratch leak tests — SD-AC7 (fork path), w4b-fork-scratch-leak.
//
// Verifies that a forked child's scratch disk image:
//   - is named <childID>-scratch.ext4 (recognised by both ReapDiskCopy and
//     ResourceIndex.List), and
//   - is reported as KindDiskScratch by ResourceIndex.List (operator can see it),
//   - is removed by ReapDiskCopy (operator can reclaim it).
//
// These tests do NOT boot VMs. They call ChildExtraDiskPath directly (the real
// production function, not a stand-in) and then exercise the real reclamation
// path.  Reverting the "-scratch.ext4" special-case in ChildExtraDiskPath makes
// every assertion here fail.
//
// Mutation-proof: the test creates the scratch file at the path ChildExtraDiskPath
// returns, then checks that ResourceIndex.List enumerates it as KindDiskScratch
// and that ReapDiskCopy removes it.  With the fix reverted the returned name is
// "<childID>-<parentID>-scratch.ext4"; that stem does not parse as a SandboxID,
// so ResourceIndex.List silently skips the file (the bug) and ReapDiskCopy does
// not remove it — both assertions fail loudly.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/service"
)

// TestForkScratchDiskName verifies that ChildExtraDiskPath names the child's
// scratch copy "<childID>-scratch.ext4" — the only name that both
// ResourceIndex.List and ReapDiskCopy recognise. This is the premise check:
// if ChildExtraDiskPath returns any other name, the fixture self-fails loudly.
func TestForkScratchDiskName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	parentID := domain.NewSandboxID()
	childID := domain.NewSandboxID()

	parentScratch := filepath.Join(dir, parentID.String()+"-scratch.ext4")
	got := cloudhypervisor.ChildExtraDiskPath(childID, parentScratch)
	want := filepath.Join(dir, childID.String()+"-scratch.ext4")
	if got != want {
		t.Fatalf("ChildExtraDiskPath scratch naming wrong:\n  got  %s\n  want %s\n\nThis means the forked scratch image will escape both ReapDiskCopy and ResourceIndex.List (SD-AC7 fork path).", got, want)
	}
}

// TestForkScratchResourceIndexVisible verifies that a forked child's scratch
// disk is enumerated by ResourceIndex.List as KindDiskScratch with the correct
// OwnerID.  Before the fix, ResourceIndex.List silently skipped the file
// because ParseSandboxID could not parse the mangled "<cID>-<pID>" stem —
// an operator running `nexus3 reap` could not even SEE the leak.
func TestForkScratchResourceIndexVisible(t *testing.T) {
	t.Parallel()

	// Build a minimal state-root layout: only the disks/ subdirectory matters
	// for ResourceIndex.List.
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir disks: %v", err)
	}

	parentID := domain.NewSandboxID()
	childID := domain.NewSandboxID()

	parentScratch := filepath.Join(diskDir, parentID.String()+"-scratch.ext4")
	childScratch := cloudhypervisor.ChildExtraDiskPath(childID, parentScratch)

	// Fixture premise check: ChildExtraDiskPath must return the correct name.
	// Without this guard a broken ChildExtraDiskPath would write a file under
	// the wrong name, making the test pass vacuously ("file not in List" is
	// expected) rather than proving the bug is fixed.
	wantName := childID.String() + "-scratch.ext4"
	if filepath.Base(childScratch) != wantName {
		t.Fatalf("FIXTURE PREMISE FAILED: ChildExtraDiskPath returned %q, want basename %q — the scratch file would be invisible to both ResourceIndex.List and ReapDiskCopy", childScratch, wantName)
	}

	// Write a sparse stub at the child scratch path.
	if err := os.WriteFile(childScratch, make([]byte, 4096), 0o600); err != nil {
		t.Fatalf("write child scratch stub: %v", err)
	}

	// Fixture premise check: confirm the file actually exists before we ask
	// ResourceIndex.List to find it.
	if _, err := os.Stat(childScratch); err != nil {
		t.Fatalf("FIXTURE PREMISE FAILED: child scratch file missing after write (%v)", err)
	}

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	resources, err := idx.List()
	if err != nil {
		t.Fatalf("ResourceIndex.List: %v", err)
	}

	var found bool
	for _, r := range resources {
		if r.Kind == service.KindDiskScratch && r.OwnerID == childID {
			found = true
			if r.Path != childScratch {
				t.Errorf("KindDiskScratch path = %q, want %q", r.Path, childScratch)
			}
		}
	}
	if !found {
		t.Errorf("forked child scratch disk not found in ResourceIndex.List as KindDiskScratch/OwnerID=%s — reap cannot report it (SD-AC7 fork path)\nAll resources listed: %v", childID, resources)
	}
}

// TestForkScratchReapDiskCopyRemoves verifies that ReapDiskCopy removes the
// forked child's scratch disk, leaving no stranded bytes.  This is the
// complement to TestForkScratchResourceIndexVisible: once the operator can SEE
// the file, they must also be able to REMOVE it.
func TestForkScratchReapDiskCopyRemoves(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	parentID := domain.NewSandboxID()
	childID := domain.NewSandboxID()

	parentScratch := filepath.Join(diskDir, parentID.String()+"-scratch.ext4")
	childScratch := cloudhypervisor.ChildExtraDiskPath(childID, parentScratch)

	// Fixture premise check: name must be correct before we test removal.
	wantName := childID.String() + "-scratch.ext4"
	if filepath.Base(childScratch) != wantName {
		t.Fatalf("FIXTURE PREMISE FAILED: ChildExtraDiskPath returned %q, want basename %q", childScratch, wantName)
	}

	// Write a sparse stub at the child scratch path.
	if err := os.WriteFile(childScratch, make([]byte, 4096), 0o600); err != nil {
		t.Fatalf("write child scratch stub: %v", err)
	}
	// Fixture premise check: file must exist before ReapDiskCopy runs.
	if _, err := os.Stat(childScratch); err != nil {
		t.Fatalf("FIXTURE PREMISE FAILED: child scratch file missing before ReapDiskCopy (%v)", err)
	}

	if err := service.ReapDiskCopy(diskDir, childID); err != nil {
		t.Fatalf("ReapDiskCopy: %v", err)
	}

	if _, err := os.Stat(childScratch); !os.IsNotExist(err) {
		t.Errorf("child scratch file still on disk after ReapDiskCopy: %s (want ErrNotExist, got %v)", childScratch, err)
	}
}
