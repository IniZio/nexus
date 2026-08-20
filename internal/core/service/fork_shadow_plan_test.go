package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/diskname"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// The single most dangerous drift in TBD-PD-38: childShadowPlan predicts where
// the driver will write each child's shadow copy, and the intent lease is
// published for those predicted paths. If the two ever disagree, the intent
// protects a path nothing writes while the real copy sits unguarded — and
// every existing test still passes, because the fork succeeds and the reaper
// keeps something.
//
// This pins the prediction to the driver's naming without importing the driver
// (which would create a cycle): it reproduces ChildExtraDiskPath's rule for
// non-workspace extra disks and asserts the handle round-trips through the
// reaper's own extractor. fork_shadow_inflight_test.go covers the other
// direction by calling cloudhypervisor.ChildExtraDiskPath directly.
func TestChildShadowPlan_PredictsDriverPathsAndHandle(t *testing.T) {
	diskDir := "/state/disks"
	childID := domain.NewSandboxID()
	parentSafe := "proj_box"
	parentNames := []string{
		parentSafe + ".shadow.node_modules.ext4",
		parentSafe + ".shadow.dist.ext4",
	}

	handle, paths := childShadowPlan(diskDir, childID, parentNames)

	if len(paths) != len(parentNames) {
		t.Fatalf("planned %d paths, want %d", len(paths), len(parentNames))
	}
	for i, base := range parentNames {
		// ChildExtraDiskPath: non-workspace extra disks become
		// <dir>/<childID>-<basename>.
		want := filepath.Join(diskDir, childID.String()+"-"+base)
		if paths[i] != want {
			t.Errorf("path[%d] = %q, want %q (driver naming)", i, paths[i], want)
		}
	}

	// Every copy must carry the SAME handle, because the reaper looks each one
	// up in a single map keyed by that handle. If they differed, one intent
	// could not cover them all.
	wantHandle := childID.String() + "-" + parentSafe
	if handle != wantHandle {
		t.Errorf("handle = %q, want %q", handle, wantHandle)
	}
	for _, p := range paths {
		got, ok := diskname.ShadowDiskSafeHandle(filepath.Base(p))
		if !ok {
			t.Errorf("planned path %q is not recognised as a shadow disk", filepath.Base(p))
			continue
		}
		if got != handle {
			t.Errorf("reaper reads handle %q out of %q, but the intent was published for %q",
				got, filepath.Base(p), handle)
		}
	}
}

// Legacy pre-B1 shadow disks (*.shadow.ext4) carry no handle, match no live
// sandbox, and are unconditionally reclaimable. Leasing a copy of one would
// resurrect garbage the reaper is supposed to remove.
func TestParentShadowDiskNames_ExcludesLegacyAndOtherSandboxes(t *testing.T) {
	diskDir := t.TempDir()
	write := func(name string) {
		if err := writeFileForTest(filepath.Join(diskDir, name)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("proj_box.shadow.node_modules.ext4") // ours
	write("node_modules.shadow.ext4")          // legacy, no handle
	write("other_box.shadow.dist.ext4")        // a different sandbox
	write("proj_box-workspace.ext4")           // not a shadow disk
	write("proj_box.shadow.node_modules.txt")  // wrong suffix

	names, err := parentShadowDiskNames(diskDir, "proj/box")
	if err != nil {
		t.Fatalf("parentShadowDiskNames: %v", err)
	}
	if len(names) != 1 || names[0] != "proj_box.shadow.node_modules.ext4" {
		t.Errorf("got %v, want exactly [proj_box.shadow.node_modules.ext4]", names)
	}
}

// A missing disk dir means the sandbox has no shadow disks — not an error.
func TestParentShadowDiskNames_MissingDirIsNotAnError(t *testing.T) {
	names, err := parentShadowDiskNames(filepath.Join(t.TempDir(), "absent"), "proj/box")
	if err != nil {
		t.Fatalf("missing disk dir returned an error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("got %v, want none", names)
	}
}

func writeFileForTest(path string) error {
	return os.WriteFile(path, []byte("x"), 0o600)
}
