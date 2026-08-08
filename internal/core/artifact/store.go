package artifact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// Store persists snapshot artifacts on disk with commit-marker integrity.
//
// # Write ordering (crash safety)
//
// Write always fsyncs the payload file before writing or fsyncing the commit
// marker file. A crash between the two leaves a torn write that Read detects
// and rejects. The two files are:
//
//   - <dir>/<id>.payload — raw payload bytes
//   - <dir>/<id>.commit  — JSON metadata including expected payload size and
//     a non-empty CommitMarker string that certifies completion
//
// # Torn-write detection
//
// Read checks two conditions:
//  1. The commit marker file exists and its CommitMarker field is non-empty.
//  2. The on-disk payload file size equals the Size recorded in the commit.
//
// Either failure returns an error naming the specific integrity violation so
// that diagnostics are unambiguous.
type Store struct {
	dir string
}

// NewStore creates a Store rooted at dir, creating the directory if it does
// not exist.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("artifact.NewStore: mkdir %q: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// commitRecord is the JSON structure written to the commit marker file.
// It carries all snapshot metadata; the payload file carries only raw bytes.
type commitRecord struct {
	ID           SnapshotID       `json:"id"`
	SandboxID    domain.SandboxID `json:"sandbox_id"`
	Kind         SnapshotKind     `json:"kind"`
	Size         int64            `json:"size"`
	CreatedAt    time.Time        `json:"created_at"`
	CommitMarker string           `json:"commit_marker"`
}

// Write persists payload as a snapshot artifact on disk.
//
// The payload length must equal snap.Size. The write sequence is:
//  1. Write payload to <id>.payload and fsync.
//  2. Write commit metadata (including snap.CommitMarker) to <id>.commit and fsync.
//
// If the process crashes between steps 1 and 2, the commit file is absent and
// a subsequent Read will reject the snapshot as a torn write.
func (s *Store) Write(snap Snapshot, payload []byte) error {
	if int64(len(payload)) != snap.Size {
		return fmt.Errorf("artifact.Store.Write %s: payload length %d != snap.Size %d",
			snap.ID, len(payload), snap.Size)
	}

	payloadPath := s.payloadPath(snap.ID)
	markerPath := s.markerPath(snap.ID)

	// Step 1: write + fsync payload BEFORE writing the commit marker.
	// A crash here leaves the commit marker absent → detected as torn write.
	if err := writeAndSync(payloadPath, payload); err != nil {
		_ = os.Remove(payloadPath)
		return fmt.Errorf("artifact.Store.Write %s: payload: %w", snap.ID, err)
	}

	// Step 2: write + fsync commit marker. Only reached after payload is durable.
	rec := commitRecord{
		ID:           snap.ID,
		SandboxID:    snap.SandboxID,
		Kind:         snap.Kind,
		Size:         snap.Size,
		CreatedAt:    snap.CreatedAt,
		CommitMarker: snap.CommitMarker,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		_ = os.Remove(payloadPath)
		return fmt.Errorf("artifact.Store.Write %s: marshal commit: %w", snap.ID, err)
	}
	if err := writeAndSync(markerPath, data); err != nil {
		_ = os.Remove(payloadPath)
		_ = os.Remove(markerPath)
		return fmt.Errorf("artifact.Store.Write %s: commit marker: %w", snap.ID, err)
	}
	return nil
}

// Read retrieves the snapshot identified by id, rejecting torn writes.
//
// Torn-write rejection: if the commit marker file is absent, its CommitMarker
// field is empty, or the payload file size does not equal the recorded Size,
// Read returns an error describing the specific integrity failure.
func (s *Store) Read(id SnapshotID) (Snapshot, error) {
	markerPath := s.markerPath(id)
	payloadPath := s.payloadPath(id)

	// Check commit marker first. Its absence is the canonical torn-write signal.
	data, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, fmt.Errorf(
				"artifact.Store.Read %s: commit marker absent (torn write: payload written but commit marker not fsynced)",
				id,
			)
		}
		return Snapshot{}, fmt.Errorf("artifact.Store.Read %s: read commit marker: %w", id, err)
	}

	var rec commitRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return Snapshot{}, fmt.Errorf("artifact.Store.Read %s: parse commit marker: %w", id, err)
	}
	if rec.CommitMarker == "" {
		return Snapshot{}, fmt.Errorf(
			"artifact.Store.Read %s: empty CommitMarker field (torn write?)", id,
		)
	}

	// Verify payload size matches the size recorded at write time.
	// A mismatch means the payload was truncated — a torn write.
	info, err := os.Stat(payloadPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, fmt.Errorf(
				"artifact.Store.Read %s: payload file absent (torn write?)", id,
			)
		}
		return Snapshot{}, fmt.Errorf("artifact.Store.Read %s: stat payload: %w", id, err)
	}
	if info.Size() != rec.Size {
		return Snapshot{}, fmt.Errorf(
			"artifact.Store.Read %s: payload size %d != recorded size %d (torn write: payload truncated)",
			id, info.Size(), rec.Size,
		)
	}

	return Snapshot{
		ID:           rec.ID,
		SandboxID:    rec.SandboxID,
		Kind:         rec.Kind,
		Size:         rec.Size,
		CommitMarker: rec.CommitMarker,
		CreatedAt:    rec.CreatedAt,
	}, nil
}

// List returns all valid (non-torn) snapshots in the store. Torn-write entries
// are silently skipped; callers can separately handle them via Read.
func (s *Store) List() ([]Snapshot, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("artifact.Store.List: read dir: %w", err)
	}
	var snaps []Snapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".commit") {
			continue
		}
		id := SnapshotID(strings.TrimSuffix(e.Name(), ".commit"))
		snap, err := s.Read(id)
		if err != nil {
			// Skip torn or unreadable entries; do not surface them in a list.
			continue
		}
		snaps = append(snaps, snap)
	}
	return snaps, nil
}

// Remove deletes the snapshot payload and commit marker for id. Idempotent:
// removing a non-existent snapshot is not an error.
func (s *Store) Remove(id SnapshotID) error {
	payloadErr := os.Remove(s.payloadPath(id))
	markerErr := os.Remove(s.markerPath(id))

	if payloadErr != nil && !os.IsNotExist(payloadErr) {
		return fmt.Errorf("artifact.Store.Remove %s: remove payload: %w", id, payloadErr)
	}
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return fmt.Errorf("artifact.Store.Remove %s: remove marker: %w", id, markerErr)
	}
	return nil
}

func (s *Store) payloadPath(id SnapshotID) string {
	return filepath.Join(s.dir, string(id)+".payload")
}

func (s *Store) markerPath(id SnapshotID) string {
	return filepath.Join(s.dir, string(id)+".commit")
}

// writeAndSync atomically writes data to path and fsyncs the file before
// returning. Used to guarantee durability before the commit marker is written.
func writeAndSync(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	defer f.Close() // double-close after Sync is harmless

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %q: %w", path, err)
	}
	return nil
}
