package service_test

// S2 tests: restore <snapshot> --count n fan-out (edge 5, no new lifecycle edge).
//
// S2-AC1: RestoreFromSnapshot creates n running children from a retained
//         snapshot; parent/origin sandbox is unaffected.
// S2-AC2: A bad/torn snapshot yields a clean failure — no children created,
//         no error state produced.
// S2-AC3: Children carry Provenance.SourceSnapshot == <snap id>.

import (
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// writeSnapWithOrigin writes a snapshot record whose SandboxID points to an
// existing sandbox. D-PD-33 requires a valid origin for RestoreFromSnapshot.
func writeSnapWithOrigin(t *testing.T, st *artifact.Store, id string, originID domain.SandboxID, kind artifact.SnapshotKind) artifact.Snapshot {
	t.Helper()
	snap := artifact.Snapshot{
		ID:           artifact.SnapshotID(id),
		SandboxID:    originID,
		Kind:         kind,
		Size:         4,
		CommitMarker: "committed",
		CreatedAt:    time.Now(),
	}
	if err := st.Write(snap, []byte("data")); err != nil {
		t.Fatalf("aStore.Write(%s): %v", id, err)
	}
	return snap
}

// ── S2-AC1: fan-out N children from a retained snapshot ──────────────────────

func TestS2AC1_RestoreFromSnapshot_CreatesNRunningChildren(t *testing.T) {
	aStore := makeArtifactStore(t)
	svc := newSvc(t).WithArtifacts(aStore)
	c := ctx()

	const snapIDStr = "s2-ac1-retained-snap-000000"
	snapID := artifact.SnapshotID(snapIDStr)

	// D-PD-33: RestoreFromSnapshot requires the origin sandbox to exist so it
	// can reconstruct the egress policy. Create a minimal origin record first.
	origin, err := svc.Create(c, "proj", "s2-ac1-origin", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create origin: %v", err)
	}

	// Seed a retained snapshot record into the artifact store. The fake driver's
	// ForkFrom does not read the store itself — it receives the Snapshot struct
	// from the service, so only the artifact store record matters here.
	writeSnapWithOrigin(t, aStore, snapIDStr, origin.ID, artifact.KindRetained)

	const count = 3
	children, err := svc.RestoreFromSnapshot(c, snapID, count)
	if err != nil {
		t.Fatalf("S2-AC1: RestoreFromSnapshot: %v", err)
	}

	// Assert correct count.
	if len(children) != count {
		t.Fatalf("S2-AC1: expected %d children, got %d", count, len(children))
	}

	// Assert all children are Running with non-empty instance IDs.
	for i, ch := range children {
		if ch.State != domain.Running {
			t.Errorf("S2-AC1: child[%d].State = %v, want Running", i, ch.State)
		}
		if ch.InstanceID == "" {
			t.Errorf("S2-AC1: child[%d].InstanceID is empty", i)
		}
		if ch.ID == (domain.SandboxID{}) {
			t.Errorf("S2-AC1: child[%d].ID is zero", i)
		}
	}

	// S2-AC3: Provenance.SourceSnapshot must equal snapID on every child.
	for i, ch := range children {
		if ch.Provenance == nil {
			t.Errorf("S2-AC1/AC3: child[%d].Provenance is nil", i)
			continue
		}
		if ch.Provenance.SourceSnapshot != snapIDStr {
			t.Errorf("S2-AC1/AC3: child[%d].SourceSnapshot = %q, want %q",
				i, ch.Provenance.SourceSnapshot, snapIDStr)
		}
	}

	// All children must have distinct IDs.
	seen := make(map[domain.SandboxID]bool)
	for _, ch := range children {
		if seen[ch.ID] {
			t.Errorf("S2-AC1: duplicate child ID %s", ch.ID)
		}
		seen[ch.ID] = true
	}
}

// TestS2AC1_RestoreFromSnapshot_ParentSandboxUnaffected verifies that a live
// sandbox used here as an "origin" is not modified by RestoreFromSnapshot.
func TestS2AC1_RestoreFromSnapshot_ParentSandboxUnaffected(t *testing.T) {
	aStore := makeArtifactStore(t)
	svc := newSvc(t).WithArtifacts(aStore)
	c := ctx()

	// Create and start a "parent" sandbox to serve as an observable origin.
	parent, err := svc.Create(c, "proj", "restore-parent", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, parent.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Seed an artifact-store record representing a retained snapshot of parent.
	// D-PD-33: the snapshot's SandboxID must point to the origin (parent) so
	// RestoreFromSnapshot can reconstruct the egress policy.
	writeSnapWithOrigin(t, aStore, "s2-parent-snap-000000", parent.ID, artifact.KindRetained)
	snapID := artifact.SnapshotID("s2-parent-snap-000000")

	children, err := svc.RestoreFromSnapshot(c, snapID, 2)
	if err != nil {
		t.Fatalf("S2-AC1 parent-unaffected: RestoreFromSnapshot: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}

	// The parent sandbox must still be Running and unmodified.
	all, err := svc.List(c)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *domain.Sandbox
	for i := range all {
		if all[i].ID == parent.ID {
			found = &all[i]
			break
		}
	}
	if found == nil {
		t.Fatal("S2-AC1: parent sandbox not found after restore fan-out")
	}
	if found.State != domain.Running {
		t.Errorf("S2-AC1: parent state = %v after restore, want Running", found.State)
	}
}

// ── S2-AC2: bad/torn snapshot yields a clean failure ─────────────────────────

// TestS2AC2_MissingSnapshot_CleanFailure verifies that a non-existent snapshot
// ID (which simulates a bad/missing/torn artifact) yields a clean error with no
// children created and no error state produced.
func TestS2AC2_MissingSnapshot_CleanFailure_NoChildren(t *testing.T) {
	aStore := makeArtifactStore(t)
	svc := newSvc(t).WithArtifacts(aStore)
	c := ctx()

	// Snapshot ID that has no record in the store — simulates a missing or
	// torn-write snapshot (commit marker absent = store.Read returns an error).
	badSnapID := artifact.SnapshotID("nonexistent-snap-000000000")

	_, err := svc.RestoreFromSnapshot(c, badSnapID, 2)
	if err == nil {
		t.Fatal("S2-AC2: expected error for bad snapshot, got nil")
	}

	// No children must have been created (zero sandboxes in store).
	all, err2 := svc.List(c)
	if err2 != nil {
		t.Fatalf("S2-AC2: List after bad restore: %v", err2)
	}
	if len(all) != 0 {
		t.Errorf("S2-AC2: expected 0 sandboxes after bad restore, got %d", len(all))
	}
}

// ── S2: ancillary validation tests ───────────────────────────────────────────

func TestS2_InvalidCount(t *testing.T) {
	aStore := makeArtifactStore(t)
	svc := newSvc(t).WithArtifacts(aStore)
	c := ctx()

	writeSnap(t, aStore, "s2-count-snap", artifact.KindRetained)

	if _, err := svc.RestoreFromSnapshot(c, "s2-count-snap", 0); err == nil {
		t.Fatal("RestoreFromSnapshot(count=0): expected error, got nil")
	}
	if _, err := svc.RestoreFromSnapshot(c, "s2-count-snap", -1); err == nil {
		t.Fatal("RestoreFromSnapshot(count=-1): expected error, got nil")
	}
}

func TestS2_NoArtifactStore_ReturnsError(t *testing.T) {
	svc := newSvc(t) // no WithArtifacts
	c := ctx()

	_, err := svc.RestoreFromSnapshot(c, "any-snap", 1)
	if err == nil {
		t.Fatal("expected error when no artifact store attached")
	}
}
