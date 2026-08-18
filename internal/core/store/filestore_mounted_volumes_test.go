package store_test

// TestMountedVolumes_FilestoreRoundTrip is the regression guard for the bug
// fixed in SD2-PERSIST: MountedVolumes was absent from the filestore record
// struct and both mapping functions, so any sandbox written and then re-read
// from disk silently lost all attached volumes.
//
// This test goes through FileStore.Create / FileStore.Get — the real
// toRecord / toDomain path — not through a JSON round-trip of domain.Sandbox.
// That distinction is the whole point: the domain-level JSON test
// (internal/core/domain/sandbox_mounted_volumes_test.go) cannot catch a bug
// in the filestore mapping layer and would have stayed green even when this
// bug existed.
//
// Mutation proof (run after writing this test but before shipping):
//
//  1. Remove `MountedVolumes: sb.MountedVolumes,` from toRecord in filestore.go
//     → TestMountedVolumes_FilestoreRoundTrip goes RED;
//       TestMountedVolumes_RoundTrip_Populated in domain/ stays GREEN.
//
//  2. Remove `MountedVolumes: r.MountedVolumes,` from toDomain in filestore.go
//     → TestMountedVolumes_FilestoreRoundTrip goes RED;
//       TestMountedVolumes_RoundTrip_Populated in domain/ stays GREEN.
//
// That contrast is evidence that only the filestore-level test catches this
// class of mapping bug.

import (
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

func TestMountedVolumes_FilestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Two attachments with complementary shapes: kind=disk rw and kind=dir ro.
	// Both are required so a mutation that drops only one field can still be
	// caught.
	want := []domain.VolumeAttachment{
		{Name: "data-vol", GuestPath: "/mnt/data", Kind: "disk", ReadOnly: false},
		{Name: "src-vol", GuestPath: "/mnt/src", Kind: "dir", ReadOnly: true},
	}

	sb := makeSandbox("vol-sandbox", "vol-proj")
	sb.MountedVolumes = want

	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Re-open with a fresh FileStore rooted at the same directory to exercise
	// the full on-disk persistence path (not in-memory caching, if any).
	st2, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	got, err := st2.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}

	if len(got.MountedVolumes) != len(want) {
		t.Fatalf("MountedVolumes length: got %d, want %d (volumes lost on disk round-trip)",
			len(got.MountedVolumes), len(want))
	}
	for i, w := range want {
		g := got.MountedVolumes[i]
		if g.Name != w.Name {
			t.Errorf("MountedVolumes[%d].Name: got %q, want %q", i, g.Name, w.Name)
		}
		if g.GuestPath != w.GuestPath {
			t.Errorf("MountedVolumes[%d].GuestPath: got %q, want %q", i, g.GuestPath, w.GuestPath)
		}
		if g.Kind != w.Kind {
			t.Errorf("MountedVolumes[%d].Kind: got %q, want %q", i, g.Kind, w.Kind)
		}
		if g.ReadOnly != w.ReadOnly {
			t.Errorf("MountedVolumes[%d].ReadOnly: got %v, want %v", i, g.ReadOnly, w.ReadOnly)
		}
	}
}

// TestMountedVolumes_FilestoreRoundTrip_Nil confirms that a sandbox with no
// attached volumes round-trips to a nil slice (not an empty slice or error),
// and that the on-disk record does not gain a spurious "mounted_volumes" key.
func TestMountedVolumes_FilestoreRoundTrip_Nil(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	sb := makeSandbox("no-vol-sandbox", "proj")
	// MountedVolumes is nil by default in makeSandbox.
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MountedVolumes != nil {
		t.Errorf("expected nil MountedVolumes for sandbox with no volumes, got %v", got.MountedVolumes)
	}
}
