package service_test

// S1 tests: snapshot list + snapshot rm with retention refusal.
//
// S1-AC1: snapshot list returns all valid snapshots via Store.List().
// S1-AC2: snapshot rm removes a snapshot when no sandbox references it.
// S1-AC3: snapshot rm of a retained snapshot refuses when any sandbox has
//         Provenance.SourceSnapshot == id; snapshot is retained after refusal.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/service"
)

// makeArtifactStore creates an artifact.Store in a fresh temp dir.
func makeArtifactStore(t *testing.T) *artifact.Store {
	t.Helper()
	st, err := artifact.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("artifact.NewStore: %v", err)
	}
	return st
}

// writeSnap writes a minimal valid snapshot record to st and returns the snapshot.
func writeSnap(t *testing.T, st *artifact.Store, id string, kind artifact.SnapshotKind) artifact.Snapshot {
	t.Helper()
	snap := artifact.Snapshot{
		ID:           artifact.SnapshotID(id),
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

// ── S1-AC1: snapshot list returns all valid snapshots ────────────────────────

func TestS1AC1_SnapshotList_ReturnsAll(t *testing.T) {
	aStore := makeArtifactStore(t)
	svc := newSvc(t).WithArtifacts(aStore)

	// Start empty.
	snaps, err := svc.SnapshotList()
	if err != nil {
		t.Fatalf("SnapshotList (empty): %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("expected 0 snapshots, got %d", len(snaps))
	}

	// Write two records.
	writeSnap(t, aStore, "aaa", artifact.KindRetained)
	writeSnap(t, aStore, "bbb", artifact.KindTransient)

	snaps, err = svc.SnapshotList()
	if err != nil {
		t.Fatalf("SnapshotList (2 entries): %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("S1-AC1: expected 2 snapshots, got %d", len(snaps))
	}
	seen := make(map[string]bool)
	for _, s := range snaps {
		seen[string(s.ID)] = true
	}
	if !seen["aaa"] || !seen["bbb"] {
		t.Errorf("S1-AC1: missing expected IDs; got %v", snaps)
	}
}

func TestS1AC1_SnapshotList_NilWhenNoStore(t *testing.T) {
	svc := newSvc(t) // no WithArtifacts
	snaps, err := svc.SnapshotList()
	if err != nil {
		t.Fatalf("SnapshotList (no store): %v", err)
	}
	if snaps != nil {
		t.Errorf("S1-AC1: expected nil when no store, got %v", snaps)
	}
}

// ── S1-AC2: snapshot rm removes when no sandbox references it ────────────────

func TestS1AC2_SnapshotRm_NoChildren_Succeeds(t *testing.T) {
	aStore := makeArtifactStore(t)
	svc := newSvc(t).WithArtifacts(aStore)
	c := ctx()

	writeSnap(t, aStore, "snap-to-rm", artifact.KindRetained)

	if err := svc.SnapshotRemove(c, "snap-to-rm"); err != nil {
		t.Fatalf("S1-AC2: SnapshotRemove: %v", err)
	}

	// Verify record is gone.
	_, err := aStore.Read("snap-to-rm")
	if err == nil {
		t.Errorf("S1-AC2: snapshot record still present after rm")
	}
}

func TestS1AC2_SnapshotRm_Idempotent(t *testing.T) {
	aStore := makeArtifactStore(t)
	svc := newSvc(t).WithArtifacts(aStore)
	c := ctx()

	// Removing a non-existent snapshot must not error (idempotent via Store.Remove).
	if err := svc.SnapshotRemove(c, "nonexistent"); err != nil {
		t.Errorf("S1-AC2: expected idempotent rm of nonexistent, got: %v", err)
	}
}

func TestS1AC2_SnapshotRm_ErrorWhenNoStore(t *testing.T) {
	svc := newSvc(t) // no WithArtifacts
	c := ctx()

	err := svc.SnapshotRemove(c, "some-id")
	if err == nil {
		t.Fatal("S1-AC2: expected error when no artifact store attached")
	}
	if !errors.Is(err, service.ErrNoArtifactStore) {
		t.Errorf("S1-AC2: expected ErrNoArtifactStore in chain, got: %v", err)
	}
}

// ── S1-AC3: snapshot rm refuses when a sandbox references the snapshot ────────

func TestS1AC3_SnapshotRm_RefusesWhenChildExists(t *testing.T) {
	aStore := makeArtifactStore(t)
	svc := newSvc(t).WithArtifacts(aStore)
	c := ctx()

	// Create parent and fork children. The fork uses a transient snapshot
	// internally; the children's Provenance.SourceSnapshot records the snap ID.
	parent, err := svc.Create(c, "proj", "fork-parent-s1ac3", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, parent.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	children, err := svc.Fork(c, parent.ID.String(), 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	snapID := artifact.SnapshotID(children[0].Provenance.SourceSnapshot)

	// Write a retained snapshot record with the same ID that the child references.
	// (In production the driver writes this; here we simulate that record existing.)
	rec := artifact.Snapshot{
		ID:           snapID,
		Kind:         artifact.KindRetained,
		Size:         4,
		CommitMarker: "committed",
		CreatedAt:    time.Now(),
	}
	if err := aStore.Write(rec, []byte("data")); err != nil {
		t.Fatalf("aStore.Write: %v", err)
	}

	// Attempt to remove — must be refused because children[0] references snapID.
	err = svc.SnapshotRemove(c, snapID)
	if err == nil {
		t.Fatal("S1-AC3: expected refusal when child sandbox references snapshot")
	}
	// The error message must name the sandbox.
	if !strings.Contains(err.Error(), children[0].ID.String()) {
		t.Errorf("S1-AC3: error should name the referencing sandbox; got: %v", err)
	}

	// Snapshot must still be present.
	if _, readErr := aStore.Read(snapID); readErr != nil {
		t.Errorf("S1-AC3: snapshot removed despite refusal: %v", readErr)
	}
}

