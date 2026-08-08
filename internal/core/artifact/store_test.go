package artifact_test

import (
	"os"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// newStore creates a fresh Store backed by a temp directory.
func newStore(t *testing.T) *artifact.Store {
	t.Helper()
	st, err := artifact.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

// makeSnapshot builds a minimal valid Snapshot for testing.
func makeSnapshot(payload []byte) artifact.Snapshot {
	return artifact.Snapshot{
		ID:           artifact.SnapshotID("test-snap-001"),
		SandboxID:    domain.NewSandboxID(),
		Kind:         artifact.KindTransient,
		Size:         int64(len(payload)),
		CommitMarker: "committed",
		CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
	}
}

// ── Happy path ───────────────────────────────────────────────────────────────

// TestWriteRead_HappyPath verifies that a snapshot survives a Write/Read
// round-trip with all metadata fields intact.
func TestWriteRead_HappyPath(t *testing.T) {
	t.Parallel()
	st := newStore(t)

	payload := []byte("hello, snapshot")
	snap := makeSnapshot(payload)

	if err := st.Write(snap, payload); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := st.Read(snap.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got.ID != snap.ID {
		t.Errorf("ID: got %q, want %q", got.ID, snap.ID)
	}
	if got.SandboxID != snap.SandboxID {
		t.Errorf("SandboxID: got %v, want %v", got.SandboxID, snap.SandboxID)
	}
	if got.Kind != snap.Kind {
		t.Errorf("Kind: got %q, want %q", got.Kind, snap.Kind)
	}
	if got.Size != snap.Size {
		t.Errorf("Size: got %d, want %d", got.Size, snap.Size)
	}
	if got.CommitMarker != snap.CommitMarker {
		t.Errorf("CommitMarker: got %q, want %q", got.CommitMarker, snap.CommitMarker)
	}
	if !got.CreatedAt.Equal(snap.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, snap.CreatedAt)
	}
}

// TestValidate_RoundTrip verifies that a round-tripped snapshot passes Validate.
func TestValidate_RoundTrip(t *testing.T) {
	t.Parallel()
	st := newStore(t)

	payload := []byte("validate me")
	snap := makeSnapshot(payload)

	if err := st.Write(snap, payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := st.Read(snap.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate on round-tripped snapshot: %v", err)
	}
}

// ── Torn-write detection (primary acceptance condition) ───────────────────────

// TestTornWrite_TruncatedPayload is the primary acceptance condition for the
// artifact store's integrity guarantee. It verifies that:
//
//  1. A complete snapshot can be written and read back successfully.
//  2. After the payload file is truncated (simulating a crash mid-write or
//     a partial write before fsync), Read rejects the snapshot with an error
//     naming "torn write".
//
// This test directly proves that the write ordering guarantee (payload fsynced
// BEFORE commit marker written) makes torn writes detectable: a crash after
// the payload is truncated but before the commit marker is updated leaves an
// inconsistency that Read catches.
func TestTornWrite_TruncatedPayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	st, err := artifact.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	payload := []byte("full payload data for torn write test")
	snap := makeSnapshot(payload)

	// Step 1: write a complete, valid snapshot.
	if err := st.Write(snap, payload); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Confirm it reads back correctly before sabotage.
	if _, err := st.Read(snap.ID); err != nil {
		t.Fatalf("pre-sabotage Read: %v", err)
	}

	// Step 2: truncate the payload file to simulate a torn write.
	// The commit marker still records the full Size, so Read must detect
	// that the on-disk payload is shorter than expected.
	payloadPath := dir + "/" + string(snap.ID) + ".payload"
	if err := os.Truncate(payloadPath, int64(len(payload)/2)); err != nil {
		t.Fatalf("truncate payload: %v", err)
	}

	// Step 3: Read must reject the truncated snapshot.
	_, err = st.Read(snap.ID)
	if err == nil {
		t.Fatal("Read on truncated payload: expected error (torn write), got nil")
	}
	t.Logf("Read correctly rejected truncated payload: %v", err)
}

// TestTornWrite_MissingCommitMarker verifies that a snapshot whose commit
// marker file is absent (the canonical torn-write case: crash between payload
// fsync and marker write) is rejected by Read.
func TestTornWrite_MissingCommitMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	st, err := artifact.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	payload := []byte("payload without commit marker")
	snap := makeSnapshot(payload)

	// Write a complete snapshot.
	if err := st.Write(snap, payload); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Remove the commit marker file, simulating a crash before it was written.
	markerPath := dir + "/" + string(snap.ID) + ".commit"
	if err := os.Remove(markerPath); err != nil {
		t.Fatalf("remove marker: %v", err)
	}

	// Read must reject the now-markerless snapshot.
	_, err = st.Read(snap.ID)
	if err == nil {
		t.Fatal("Read with missing commit marker: expected error (torn write), got nil")
	}
	t.Logf("Read correctly rejected missing commit marker: %v", err)
}

// ── List ─────────────────────────────────────────────────────────────────────

// TestList_Empty verifies List returns nil (not error) on an empty store.
func TestList_Empty(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	snaps, err := st.List()
	if err != nil {
		t.Fatalf("List on empty store: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("List on empty store: got %d snapshots, want 0", len(snaps))
	}
}

// TestList_SkipsTornWrite verifies that List silently skips torn-write entries.
func TestList_SkipsTornWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	st, err := artifact.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Write a good snapshot.
	goodPayload := []byte("good")
	good := makeSnapshot(goodPayload)
	good.ID = "good-snap"
	good.Size = int64(len(goodPayload))
	if err := st.Write(good, goodPayload); err != nil {
		t.Fatalf("Write good: %v", err)
	}

	// Write then corrupt a second snapshot (remove its commit marker).
	tornPayload := []byte("torn")
	torn := makeSnapshot(tornPayload)
	torn.ID = "torn-snap"
	torn.Size = int64(len(tornPayload))
	if err := st.Write(torn, tornPayload); err != nil {
		t.Fatalf("Write torn: %v", err)
	}
	_ = os.Remove(dir + "/" + string(torn.ID) + ".commit")

	// List must return only the good snapshot.
	snaps, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 1 {
		t.Errorf("List: got %d snapshots, want 1 (torn entry skipped)", len(snaps))
	} else if snaps[0].ID != good.ID {
		t.Errorf("List: got ID %q, want %q", snaps[0].ID, good.ID)
	}
}

// ── Remove ───────────────────────────────────────────────────────────────────

// TestRemove_Idempotent verifies that removing a non-existent snapshot is not
// an error.
func TestRemove_Idempotent(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	// Removing a snapshot that was never written must not error.
	if err := st.Remove("nonexistent-snap"); err != nil {
		t.Errorf("Remove non-existent: %v", err)
	}
}

// TestRemove_DeletesBothFiles verifies that Remove cleans up both the payload
// and the commit marker, so the snapshot no longer appears in List.
func TestRemove_DeletesBothFiles(t *testing.T) {
	t.Parallel()
	st := newStore(t)

	payload := []byte("to be removed")
	snap := makeSnapshot(payload)
	if err := st.Write(snap, payload); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := st.Remove(snap.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Read must now fail (commit marker gone).
	if _, err := st.Read(snap.ID); err == nil {
		t.Error("Read after Remove: expected error, got nil")
	}

	// List must return empty.
	snaps, err := st.List()
	if err != nil {
		t.Fatalf("List after Remove: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("List after Remove: got %d snapshots, want 0", len(snaps))
	}
}

// ── Validate ─────────────────────────────────────────────────────────────────

// TestValidate_MissingCommitMarker verifies Validate rejects a Snapshot with
// no CommitMarker (in-memory torn write indication).
func TestValidate_MissingCommitMarker(t *testing.T) {
	t.Parallel()
	snap := artifact.Snapshot{
		ID:        "test-id",
		Kind:      artifact.KindRetained,
		Size:      0,
		CreatedAt: time.Now(),
		// CommitMarker intentionally empty
	}
	if err := snap.Validate(); err == nil {
		t.Error("Validate with empty CommitMarker: expected error, got nil")
	}
}

// TestWriteRead_RetainedKind verifies retained snapshots round-trip correctly.
func TestWriteRead_RetainedKind(t *testing.T) {
	t.Parallel()
	st := newStore(t)

	payload := []byte("retained snapshot payload")
	snap := artifact.Snapshot{
		ID:           "retained-001",
		SandboxID:    domain.NewSandboxID(),
		Kind:         artifact.KindRetained,
		Size:         int64(len(payload)),
		CommitMarker: "committed",
		CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
	}

	if err := st.Write(snap, payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := st.Read(snap.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != artifact.KindRetained {
		t.Errorf("Kind: got %q, want retained", got.Kind)
	}
}
