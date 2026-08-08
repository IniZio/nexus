// Package artifact defines the data model and on-disk store for snapshot
// artifacts produced by the snapshot and fork operations.
//
// # Integrity guarantee
//
// Every snapshot on disk consists of two files: a payload file and a commit
// marker file. The payload is always written and fsynced BEFORE the commit
// marker is written and fsynced. A crash between the two writes leaves the
// commit marker absent or incomplete; Store.Read detects and rejects this as
// a torn write. This prevents a partially-written snapshot from being used as
// a fork source.
package artifact

import (
	"fmt"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// SnapshotID uniquely identifies a snapshot artifact. It is opaque to callers.
type SnapshotID string

// SnapshotKind classifies the durability contract of a snapshot.
type SnapshotKind string

const (
	// KindTransient marks a snapshot used as an intermediate for fork and then
	// discarded. Not guaranteed to outlive the fork operation.
	KindTransient SnapshotKind = "transient"

	// KindRetained marks a snapshot explicitly kept for later use (repeated
	// forks, manual restore). Persisted until explicitly removed.
	KindRetained SnapshotKind = "retained"
)

// Valid reports whether k is a known SnapshotKind.
func (k SnapshotKind) Valid() bool {
	return k == KindTransient || k == KindRetained
}

// Snapshot is the artifact produced by a snapshot operation. It is immutable
// after creation and safe to pass by value.
type Snapshot struct {
	// ID uniquely identifies this snapshot.
	ID SnapshotID

	// SandboxID is the sandbox that was snapshotted.
	SandboxID domain.SandboxID

	// Kind classifies the durability contract.
	Kind SnapshotKind

	// Size is the byte count of the payload file as recorded at write time.
	// Store.Read rejects a snapshot whose on-disk payload differs from this.
	Size int64

	// CommitMarker is a non-empty string written to the commit marker file
	// AFTER the payload is fsynced. An empty CommitMarker means the write was
	// torn: the payload was not fully committed before a crash.
	CommitMarker string

	// CreatedAt is the wall-clock time when the snapshot was completed.
	CreatedAt time.Time
}

// Validate checks that the Snapshot fields are internally consistent.
// It does not perform any I/O; for on-disk integrity use Store.Read.
func (s Snapshot) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("artifact.Snapshot.Validate: ID is empty")
	}
	if !s.Kind.Valid() {
		return fmt.Errorf("artifact.Snapshot.Validate: invalid kind %q", s.Kind)
	}
	if s.Size < 0 {
		return fmt.Errorf("artifact.Snapshot.Validate: negative size %d", s.Size)
	}
	if s.CommitMarker == "" {
		return fmt.Errorf("artifact.Snapshot.Validate: missing commit marker (torn write?)")
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("artifact.Snapshot.Validate: zero CreatedAt")
	}
	return nil
}
