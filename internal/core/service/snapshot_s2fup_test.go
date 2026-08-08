package service_test

// S2FUP tests: snapshot rm deletes both the artifact-store record AND the
// CH memory-image directory (the defect was that only the record was removed).
//
// FUP-AC1: snapshot rm of an unreferenced snapshot deletes both the
//           artifact-store record and the CH files directory.
// FUP-AC2: retention refusal leaves both the record and the CH dir intact.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/service"
)

// makeSnap writes a minimal retained snapshot record to st and returns the snap.
func makeSnap(t *testing.T, st *artifact.Store, id artifact.SnapshotID) artifact.Snapshot {
	t.Helper()
	snap := artifact.Snapshot{
		ID:           id,
		Kind:         artifact.KindRetained,
		Size:         4,
		CommitMarker: "committed",
		CreatedAt:    time.Now(),
	}
	if err := st.Write(snap, []byte("data")); err != nil {
		t.Fatalf("aStore.Write(%s): %v", id, err)
	}
	return snap
}

// ── FUP-AC1 ──────────────────────────────────────────────────────────────────

func TestFUPAC1_SnapshotRm_DeletesBothRecordAndDir(t *testing.T) {
	// root serves as both the artifact-store root and the fake driver's
	// SnapshotDir, matching the production layout where defaultSnapshotDir and
	// newSnapshotService both resolve to <XDG_STATE_HOME>/nexus3/snapshots.
	root := t.TempDir()

	aStore, err := artifact.NewStore(root)
	if err != nil {
		t.Fatalf("artifact.NewStore: %v", err)
	}

	drv := fake.New()
	drv.SetSnapshotDir(root)

	svc := newSvcWithDriver(t, drv).WithArtifacts(aStore)
	c := ctx()

	const snapIDStr = "fup-ac1-snap0000000000000000"
	snapID := artifact.SnapshotID(snapIDStr)

	// Write artifact record.
	makeSnap(t, aStore, snapID)

	// Create a simulated CH memory-image directory at <root>/<snapID>/.
	// It contains a nested file to exercise RemoveAll recursion.
	snapFilesDir := filepath.Join(root, snapIDStr)
	if err := os.MkdirAll(filepath.Join(snapFilesDir, "ch-state"), 0o755); err != nil {
		t.Fatalf("mkdir ch-state: %v", err)
	}
	chFile := filepath.Join(snapFilesDir, "memory.snapshot")
	if err := os.WriteFile(chFile, []byte("vm-mem"), 0o600); err != nil {
		t.Fatalf("write memory.snapshot: %v", err)
	}

	// Pre-condition: both exist.
	if _, err := os.Stat(snapFilesDir); err != nil {
		t.Fatalf("FUP-AC1 pre: snapshot dir should exist: %v", err)
	}
	if _, err := aStore.Read(snapID); err != nil {
		t.Fatalf("FUP-AC1 pre: artifact record should be readable: %v", err)
	}

	// Remove.
	if err := svc.SnapshotRemove(c, snapID); err != nil {
		t.Fatalf("FUP-AC1: SnapshotRemove: %v", err)
	}

	// Assert CH files directory is gone.
	if _, err := os.Stat(snapFilesDir); !os.IsNotExist(err) {
		t.Errorf("FUP-AC1: CH files dir should not exist after rm; os.Stat err: %v", err)
	}

	// Assert artifact record is gone (via List — mirrors production observe path).
	remaining, err := aStore.List()
	if err != nil {
		t.Fatalf("FUP-AC1: aStore.List after rm: %v", err)
	}
	for _, s := range remaining {
		if s.ID == snapID {
			t.Errorf("FUP-AC1: artifact record still listed after rm")
		}
	}
}

// ── FUP-AC2 ──────────────────────────────────────────────────────────────────

func TestFUPAC2_SnapshotRm_RetentionRefusal_LeavesBothIntact(t *testing.T) {
	root := t.TempDir()

	aStore, err := artifact.NewStore(root)
	if err != nil {
		t.Fatalf("artifact.NewStore: %v", err)
	}

	drv := fake.New()
	drv.SetSnapshotDir(root)

	svc := newSvcWithDriver(t, drv).WithArtifacts(aStore)
	c := ctx()

	// Create a parent sandbox and fork one child so the child carries
	// Provenance.SourceSnapshot == <snapID>.
	parent, err := svc.Create(c, "proj", "fork-parent-fupac2", service.CreateOptions{})
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

	// Write a retained artifact record with the same ID so SnapshotRemove
	// finds it in the store (production does this via TakeSnapshot; here we
	// simulate it directly as S1 tests do).
	makeSnap(t, aStore, snapID)

	// Create the CH memory-image directory.
	snapFilesDir := filepath.Join(root, string(snapID))
	if err := os.MkdirAll(snapFilesDir, 0o755); err != nil {
		t.Fatalf("mkdir snapFilesDir: %v", err)
	}

	// Attempt removal — must be refused because the child references snapID.
	err = svc.SnapshotRemove(c, snapID)
	if err == nil {
		t.Fatal("FUP-AC2: expected refusal when child sandbox references snapshot")
	}

	// CH files directory must survive the refusal.
	if _, statErr := os.Stat(snapFilesDir); statErr != nil {
		t.Errorf("FUP-AC2: CH files dir should survive retention refusal; os.Stat err: %v", statErr)
	}

	// Artifact record must survive the refusal.
	if _, readErr := aStore.Read(snapID); readErr != nil {
		t.Errorf("FUP-AC2: artifact record should survive retention refusal: %v", readErr)
	}
}
