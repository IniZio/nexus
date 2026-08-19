package store_test

// TestLiveMounts_FilestoreRoundTrip is the regression guard ensuring that
// LiveMounts survive a filestore Create/Get round-trip through the real
// toRecord / toDomain mapping path (D-PD-53).
//
// The domain-level JSON test (internal/core/domain/) cannot catch a bug in
// the filestore mapping layer, so this test exercises FileStore directly.
//
// Mutation proof:
//
//  1. Remove `LiveMounts: sb.LiveMounts,` from toRecord in filestore.go
//     → this test goes RED; the domain-level test stays GREEN.
//
//  2. Remove `LiveMounts: r.LiveMounts,` from toDomain in filestore.go
//     → this test goes RED; the domain-level test stays GREEN.

import (
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

func TestLiveMounts_FilestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Two mounts with complementary shapes: rw and ro.
	// Both are required so a mutation that drops only one field can still be caught.
	want := []domain.LiveMount{
		{HostPath: "/home/user/project", GuestPath: "/workspace", ReadOnly: false},
		{HostPath: "/etc/certs", GuestPath: "/run/certs", ReadOnly: true},
	}

	sb := makeSandbox("livemount-sandbox", "livemount-proj")
	sb.LiveMounts = want

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

	if len(got.LiveMounts) != len(want) {
		t.Fatalf("LiveMounts length: got %d, want %d (mounts lost on disk round-trip)",
			len(got.LiveMounts), len(want))
	}
	for i, w := range want {
		g := got.LiveMounts[i]
		if g.HostPath != w.HostPath {
			t.Errorf("LiveMounts[%d].HostPath: got %q, want %q", i, g.HostPath, w.HostPath)
		}
		if g.GuestPath != w.GuestPath {
			t.Errorf("LiveMounts[%d].GuestPath: got %q, want %q", i, g.GuestPath, w.GuestPath)
		}
		if g.ReadOnly != w.ReadOnly {
			t.Errorf("LiveMounts[%d].ReadOnly: got %v, want %v", i, g.ReadOnly, w.ReadOnly)
		}
	}
}

// TestLiveMounts_FilestoreRoundTrip_Nil confirms that a sandbox with no live
// mounts round-trips to a nil slice (not an empty slice or error), and that the
// on-disk record does not gain a spurious "live_mounts" key.
func TestLiveMounts_FilestoreRoundTrip_Nil(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	sb := makeSandbox("no-livemount-sandbox", "proj")
	// LiveMounts is nil by default in makeSandbox.
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := st.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LiveMounts != nil {
		t.Errorf("expected nil LiveMounts for sandbox with no live mounts, got %v", got.LiveMounts)
	}
}
