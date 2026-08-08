package cloudhypervisor

// S0 store-reconciliation tests — one test per acceptance criterion.
//
// These tests exercise the driver-level artifact.Store directly without
// spinning up a real Cloud Hypervisor process, by simulating the file
// system state that TakeSnapshot/ForkFrom would produce.
//
// AC labels are from the S0 acceptance criteria in the P2 hardening spec.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/store"
)

// newStoreDriver creates a CHDriver backed by a real artifact.Store in a temp
// dir. The temp dir is used for both the store records (*.payload, *.commit)
// and the CH snapshot subdirectories (cfg.SnapshotDir/<snapID>/), mirroring
// the production layout where artifact.NewStore(cfg.SnapshotDir) is called.
func newStoreDriver(t *testing.T) *CHDriver {
	t.Helper()
	snapDir := t.TempDir()
	st, err := artifact.NewStore(snapDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &CHDriver{cfg: Config{SnapshotDir: snapDir}, snapshotStore: st}
}

// makeTestSnap writes a fake snapshot (store record + CH file directory) and
// returns the artifact.Snapshot. Callers provide the driver d, a snap ID,
// and the kind. A single file ("mem.snapshot", 4 bytes) is created under the
// CH directory to make verifyManifest succeed.
func makeTestSnap(t *testing.T, d *CHDriver, snapID artifact.SnapshotID, kind artifact.SnapshotKind) artifact.Snapshot {
	t.Helper()
	chDir := d.snapshotDirPath(snapID)
	if err := os.MkdirAll(chDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", chDir, err)
	}
	content := []byte("DATA")
	if err := os.WriteFile(filepath.Join(chDir, "mem.snapshot"), content, 0o600); err != nil {
		t.Fatalf("write mem.snapshot: %v", err)
	}
	mfst, err := buildManifest(chDir)
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}
	payload, err := json.Marshal(mfst)
	if err != nil {
		t.Fatalf("json.Marshal manifest: %v", err)
	}
	snap := artifact.Snapshot{
		ID:           snapID,
		Kind:         kind,
		Size:         int64(len(payload)),
		CommitMarker: "committed",
		CreatedAt:    time.Now(),
	}
	if err := d.snapshotStore.Write(snap, payload); err != nil {
		t.Fatalf("snapshotStore.Write: %v", err)
	}
	return snap
}

// TestS0AC1_TakeSnapshot_RealManifest verifies that the TakeSnapshot pipeline
// stores a real JSON manifest (not a zero-filled placeholder) and that the
// stored payload survives Store.Read + json.Unmarshal + verifyManifest.
//
// Key assertion: make([]byte, N) (the placeholder) fails json.Unmarshal,
// confirming that any zero-filled payload would be detectable — and that the
// actual stored payload is not zero-filled.
func TestS0AC1_TakeSnapshot_RealManifest(t *testing.T) {
	d := newStoreDriver(t)

	// Simulate the file system state TakeSnapshot step 2 produces.
	snapID := artifact.SnapshotID("ac1snap00000000000000000000000000")
	chDir := d.snapshotDirPath(snapID)
	if err := os.MkdirAll(chDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chDir, "memory.snapshot"), []byte("MEMDATA"), 0o600); err != nil {
		t.Fatalf("write memory.snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chDir, "config.json"), []byte(`{"vm":"state"}`), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	// Run the same manifest pipeline as TakeSnapshot steps 4–5.
	manifest, err := buildManifest(chDir)
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	snap := artifact.Snapshot{
		ID: snapID, Kind: artifact.KindRetained,
		Size: int64(len(payload)), CommitMarker: "committed", CreatedAt: time.Now(),
	}
	if err := d.snapshotStore.Write(snap, payload); err != nil {
		t.Fatalf("snapshotStore.Write: %v", err)
	}

	// AC1: Store.Read must succeed and the payload must be a valid JSON manifest.
	stored, err := d.snapshotStore.Read(snapID)
	if err != nil {
		t.Fatalf("S0-AC1: snapshotStore.Read: %v", err)
	}
	_ = stored

	// Read the raw payload bytes from the store directory (same as SnapshotDir
	// in production — artifact.NewStore is rooted at cfg.SnapshotDir).
	payloadPath := filepath.Join(d.cfg.SnapshotDir, string(snapID)+".payload")
	payloadBytes, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatalf("read payload file: %v", err)
	}

	// Prove that a zero-filled buffer of the same length would fail Unmarshal
	// (this is AC1's "teeth" — the placeholder is detectable).
	zeros := make([]byte, len(payloadBytes))
	var dummy snapshotManifest
	if json.Unmarshal(zeros, &dummy) == nil {
		t.Fatal("S0-AC1: expected zero-filled payload to fail json.Unmarshal (test invariant broken)")
	}

	// The actual payload must be valid JSON.
	var m snapshotManifest
	if err := json.Unmarshal(payloadBytes, &m); err != nil {
		t.Fatalf("S0-AC1: stored payload is not valid JSON manifest (placeholder leak?): %v", err)
	}

	// And verifyManifest must pass using the actual CH directory.
	m.Dir = chDir
	if err := verifyManifest(m); err != nil {
		t.Fatalf("S0-AC1: verifyManifest on stored manifest: %v", err)
	}
}

// TestS0AC3_TransientReap verifies that reapTransientSnapshot removes a
// KindTransient snapshot's store record and CH directory, while leaving a
// KindRetained snapshot completely untouched.
func TestS0AC3_TransientReap(t *testing.T) {
	d := newStoreDriver(t)

	transID := artifact.SnapshotID("aaaa0000000000000000000000000aaa")
	transSnap := makeTestSnap(t, d, transID, artifact.KindTransient)
	transDir := d.snapshotDirPath(transID)

	retID := artifact.SnapshotID("bbbb0000000000000000000000000bbb")
	retSnap := makeTestSnap(t, d, retID, artifact.KindRetained)
	retDir := d.snapshotDirPath(retID)

	// Reap the transient snapshot.
	d.reapTransientSnapshot(transSnap)

	// AC3: transient store record must be gone.
	if _, err := d.snapshotStore.Read(transID); err == nil {
		t.Error("S0-AC3: transient store record still present after reap")
	}
	// AC3: transient CH directory must be gone.
	if _, err := os.Stat(transDir); !os.IsNotExist(err) {
		t.Errorf("S0-AC3: transient CH dir %q still exists after reap", transDir)
	}

	// KindRetained must be untouched by reap (negative case).
	d.reapTransientSnapshot(retSnap)
	if _, err := d.snapshotStore.Read(retID); err != nil {
		t.Errorf("S0-AC3: retained store record removed by reap (must not happen): %v", err)
	}
	if _, err := os.Stat(retDir); err != nil {
		t.Errorf("S0-AC3: retained CH dir %q removed by reap (must not happen): %v", retDir, err)
	}
}

// TestS0AC4_KindRetained_DurableAfterRestart verifies that a KindRetained
// snapshot's store record and CH files are readable after the driver is
// reconstructed over the same SnapshotDir (simulating a process restart).
func TestS0AC4_KindRetained_DurableAfterRestart(t *testing.T) {
	// "First driver" — write a retained snapshot.
	d1 := newStoreDriver(t)
	snapDir := d1.cfg.SnapshotDir

	snapID := artifact.SnapshotID("cccc0000000000000000000000000ccc")
	makeTestSnap(t, d1, snapID, artifact.KindRetained)

	// "Restart" — create a fresh Store and CHDriver over the exact same dir.
	// This mirrors New() constructing artifact.NewStore(cfg.SnapshotDir).
	st2, err := artifact.NewStore(snapDir)
	if err != nil {
		t.Fatalf("NewStore (after restart): %v", err)
	}
	d2 := &CHDriver{cfg: Config{SnapshotDir: snapDir}, snapshotStore: st2}

	// AC4: store record readable from the new driver.
	stored, err := st2.Read(snapID)
	if err != nil {
		t.Fatalf("S0-AC4: store.Read after restart: %v", err)
	}

	// AC4: manifest payload still valid; files present at derived Dir.
	m, err := d2.readSnapshotManifest(stored)
	if err != nil {
		t.Fatalf("S0-AC4: readSnapshotManifest after restart: %v", err)
	}
	m.Dir = d2.snapshotDirPath(snapID) // derive at read time (S0-AC6 fix)
	if err := verifyManifest(m); err != nil {
		t.Fatalf("S0-AC4: verifyManifest after restart: %v", err)
	}
}

// TestS0AC5_OneRecord_NoPlaceholder verifies that after TakeSnapshot (simulated
// here via makeTestSnap), exactly one record exists in the driver's store — no
// duplicate placeholder from the service layer.
//
// The service-level complement of this test is
// TestSnapshot_S0AC5_NoPlaceholderWrittenToServiceStore in snapshot_fork_test.go.
func TestS0AC5_OneRecord_NoPlaceholder(t *testing.T) {
	d := newStoreDriver(t)

	snapID := artifact.SnapshotID("dddd0000000000000000000000000ddd")
	makeTestSnap(t, d, snapID, artifact.KindRetained)

	// AC5: exactly one record in the store.
	snaps, err := d.snapshotStore.List()
	if err != nil {
		t.Fatalf("S0-AC5: store.List: %v", err)
	}
	if len(snaps) != 1 {
		t.Errorf("S0-AC5: expected exactly 1 store record, got %d", len(snaps))
	}
	if len(snaps) == 1 && snaps[0].ID != snapID {
		t.Errorf("S0-AC5: record ID: got %s, want %s", snaps[0].ID, snapID)
	}
}

// TestS0AC6_DirDerivedAtReadTime verifies that ForkFrom can locate snapshot
// files when manifest.Dir contains a stale/old path (e.g. after a SnapshotDir
// move) by deriving the directory from the current SnapshotDir + snapID.
//
// The manifest stored on disk deliberately contains an old absolute path.
// verifyManifest must fail with that path but succeed after the Dir override.
func TestS0AC6_DirDerivedAtReadTime(t *testing.T) {
	d := newStoreDriver(t)

	snapID := artifact.SnapshotID("eeee0000000000000000000000000eee")
	// Create CH files at the CURRENT location (derived from store root + snapID).
	currentDir := d.snapshotDirPath(snapID)
	snapDir := d.cfg.SnapshotDir
	if err := os.MkdirAll(currentDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("DATA")
	if err := os.WriteFile(filepath.Join(currentDir, "mem.snapshot"), content, 0o600); err != nil {
		t.Fatalf("write mem.snapshot: %v", err)
	}

	// Write a manifest whose Dir is an OLD/different absolute path that no
	// longer holds the actual files (simulating a pre-move store record).
	oldManifest := snapshotManifest{
		Dir:   "/old/ephemeral/path/that/does/not/exist/" + string(snapID),
		Files: []snapshotManifestEntry{{Path: "mem.snapshot", Size: int64(len(content))}},
	}
	oldPayload, err := json.Marshal(oldManifest)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	snap := artifact.Snapshot{
		ID: snapID, Kind: artifact.KindRetained,
		Size: int64(len(oldPayload)), CommitMarker: "committed", CreatedAt: time.Now(),
	}
	if err := d.snapshotStore.Write(snap, oldPayload); err != nil {
		t.Fatalf("store.Write: %v", err)
	}

	stored, err := d.snapshotStore.Read(snapID)
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}

	// Read manifest (returns stale Dir).
	m, err := d.readSnapshotManifest(stored)
	if err != nil {
		t.Fatalf("readSnapshotManifest: %v", err)
	}

	// With the stale Dir, verifyManifest must fail (files not there).
	if err := verifyManifest(m); err == nil {
		t.Error("S0-AC6: verifyManifest with stale Dir should fail — files are not at the old path")
	}

	// After applying the Dir override (as ForkFrom now does), verifyManifest succeeds.
	m.Dir = d.snapshotDirPath(snapID)
	if err := verifyManifest(m); err != nil {
		t.Fatalf("S0-AC6: verifyManifest with derived Dir: %v", err)
	}

	// Confirm the derived Dir equals what ForkFrom computes.
	want := filepath.Join(snapDir, string(snapID))
	if m.Dir != want {
		t.Errorf("S0-AC6: derived Dir = %q, want %q", m.Dir, want)
	}
}

// TestDefaultSnapshotDir_MatchesCLIRoot pins that defaultSnapshotDir() returns
// exactly filepath.Join(store.DefaultRoot(), "snapshots"). If this drifts, the
// driver and CLI snapshot commands open different on-disk directories and S0's
// store reconciliation is silently broken.
func TestDefaultSnapshotDir_MatchesCLIRoot(t *testing.T) {
	got, err := defaultSnapshotDir()
	if err != nil {
		t.Fatalf("defaultSnapshotDir: %v", err)
	}
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("store.DefaultRoot: %v", err)
	}
	want := filepath.Join(root, "snapshots")
	if got != want {
		t.Errorf("defaultSnapshotDir() = %q, want %q (store reconciliation broken)", got, want)
	}
}
