package cloudhypervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/artifact"
)

// TestVMSnapshotRequest_JSON verifies that vmSnapshotRequest marshals to the
// JSON body Cloud Hypervisor's PUT /api/v1/vm.snapshot expects.
func TestVMSnapshotRequest_JSON(t *testing.T) {
	req := vmSnapshotRequest{DestinationURL: "file:///tmp/snap-abc"}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	want := `{"destination_url":"file:///tmp/snap-abc"}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

// TestVMRestoreRequest_JSON verifies that vmRestoreRequest marshals to the
// JSON body Cloud Hypervisor's PUT /api/v1/vm.restore expects, with
// source_url and prefault=true for eager restore.
func TestVMRestoreRequest_JSON(t *testing.T) {
	req := vmRestoreRequest{SourceURL: "file:///tmp/snap-abc", Prefault: true}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got["source_url"] != "file:///tmp/snap-abc" {
		t.Errorf("source_url: got %v, want file:///tmp/snap-abc", got["source_url"])
	}
	if got["prefault"] != true {
		t.Errorf("prefault: got %v, want true", got["prefault"])
	}
}

// TestVMRestoreRequest_PrefaultDefault verifies that a zero-value
// vmRestoreRequest marshals prefault as false (not omitted).
func TestVMRestoreRequest_PrefaultDefault(t *testing.T) {
	req := vmRestoreRequest{SourceURL: "file:///tmp/snap"}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got["prefault"] != false {
		t.Errorf("prefault default: got %v, want false", got["prefault"])
	}
}

// TestSnapshotDirPath verifies that snapshotDirPath joins SnapshotDir and
// the SnapshotID correctly.
func TestSnapshotDirPath(t *testing.T) {
	d := &CHDriver{cfg: Config{SnapshotDir: "/data/snaps"}}
	got := d.snapshotDirPath(artifact.SnapshotID("abc123"))
	want := filepath.Join("/data/snaps", "abc123")
	if got != want {
		t.Errorf("snapshotDirPath: got %q, want %q", got, want)
	}
}

// TestNewSnapshotID verifies that newSnapshotID returns a unique non-empty ID
// on successive calls and produces valid SnapshotIDs (32 hex chars).
func TestNewSnapshotID(t *testing.T) {
	id1, err := newSnapshotID()
	if err != nil {
		t.Fatalf("newSnapshotID: %v", err)
	}
	if id1 == "" {
		t.Error("newSnapshotID returned empty ID")
	}
	// 16 random bytes → 32 hex chars
	if len(string(id1)) != 32 {
		t.Errorf("newSnapshotID length: got %d, want 32", len(string(id1)))
	}

	id2, err := newSnapshotID()
	if err != nil {
		t.Fatalf("newSnapshotID (second call): %v", err)
	}
	if id1 == id2 {
		t.Error("newSnapshotID returned the same ID twice (collision)")
	}
}

// TestBuildManifest verifies that buildManifest walks a real on-disk directory
// and returns a sorted manifest with the correct file sizes.
func TestBuildManifest(t *testing.T) {
	dir := t.TempDir()

	// Write two files with known content.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"vm":"state"}`), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.snapshot"), []byte("MEMDATA"), 0o600); err != nil {
		t.Fatalf("write memory.snapshot: %v", err)
	}

	m, err := buildManifest(dir)
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}

	if m.Dir != dir {
		t.Errorf("manifest Dir: got %q, want %q", m.Dir, dir)
	}
	if len(m.Files) != 2 {
		t.Fatalf("manifest Files count: got %d, want 2", len(m.Files))
	}
	// Files must be sorted by relative path.
	if m.Files[0].Path != "config.json" {
		t.Errorf("Files[0].Path: got %q, want \"config.json\"", m.Files[0].Path)
	}
	if m.Files[0].Size != int64(len(`{"vm":"state"}`)) {
		t.Errorf("Files[0].Size: got %d, want %d", m.Files[0].Size, len(`{"vm":"state"}`))
	}
	if m.Files[1].Path != "memory.snapshot" {
		t.Errorf("Files[1].Path: got %q, want \"memory.snapshot\"", m.Files[1].Path)
	}
	if m.Files[1].Size != 7 {
		t.Errorf("Files[1].Size: got %d, want 7", m.Files[1].Size)
	}
}

// TestBuildManifest_Empty verifies that buildManifest on an empty directory
// returns a manifest with a nil (or empty) Files slice and no error.
func TestBuildManifest_Empty(t *testing.T) {
	dir := t.TempDir()
	m, err := buildManifest(dir)
	if err != nil {
		t.Fatalf("buildManifest on empty dir: %v", err)
	}
	if len(m.Files) != 0 {
		t.Errorf("expected 0 files, got %d", len(m.Files))
	}
}

// TestVerifyManifest verifies that verifyManifest returns nil when all files
// in the manifest are present on disk with the correct sizes.
func TestVerifyManifest(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello snapshot")
	if err := os.WriteFile(filepath.Join(dir, "state.bin"), content, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := snapshotManifest{
		Dir: dir,
		Files: []snapshotManifestEntry{
			{Path: "state.bin", Size: int64(len(content))},
		},
	}
	if err := verifyManifest(m); err != nil {
		t.Errorf("verifyManifest on valid manifest: %v", err)
	}
}

// TestVerifyManifest_MissingFile verifies that verifyManifest returns an error
// when a file listed in the manifest does not exist on disk.
func TestVerifyManifest_MissingFile(t *testing.T) {
	dir := t.TempDir()
	m := snapshotManifest{
		Dir:   dir,
		Files: []snapshotManifestEntry{{Path: "missing.bin", Size: 42}},
	}
	if err := verifyManifest(m); err == nil {
		t.Error("verifyManifest expected error for missing file, got nil")
	}
}

// TestVerifyManifest_SizeMismatch verifies that verifyManifest returns an error
// when a file's on-disk size differs from the manifest's recorded size.
func TestVerifyManifest_SizeMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mem.snapshot"), []byte("actual"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := snapshotManifest{
		Dir:   dir,
		Files: []snapshotManifestEntry{{Path: "mem.snapshot", Size: 999}}, // wrong size
	}
	if err := verifyManifest(m); err == nil {
		t.Error("verifyManifest expected error for size mismatch, got nil")
	}
}

// TestManifestRoundtrip verifies that a snapshotManifest JSON-marshals and
// unmarshals correctly, preserving Dir and all Files entries.
func TestManifestRoundtrip(t *testing.T) {
	original := snapshotManifest{
		Dir: "/data/snaps/abc123",
		Files: []snapshotManifestEntry{
			{Path: "config.json", Size: 512},
			{Path: "memory.snapshot", Size: 134217728},
		},
	}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got snapshotManifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Dir != original.Dir {
		t.Errorf("Dir: got %q, want %q", got.Dir, original.Dir)
	}
	if len(got.Files) != len(original.Files) {
		t.Fatalf("Files count: got %d, want %d", len(got.Files), len(original.Files))
	}
	for i, want := range original.Files {
		if got.Files[i] != want {
			t.Errorf("Files[%d]: got %+v, want %+v", i, got.Files[i], want)
		}
	}
}
