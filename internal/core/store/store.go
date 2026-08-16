package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// Sentinel errors returned by Store implementations. Callers should use
// errors.Is to branch rather than string matching.
var (
	// ErrNotFound is returned when a requested sandbox does not exist.
	ErrNotFound = errors.New("store: sandbox not found")

	// ErrAlreadyExists is returned by Create when the sandbox ID is already
	// registered.
	ErrAlreadyExists = errors.New("store: sandbox already exists")
)

// Store is the durable record layer for sandboxes. Multiple concurrent CLI
// invocations may read and write simultaneously; implementations must be safe
// under concurrent multi-process use.
//
// # State is a cache
// The State field stored on disk is a CACHE, not the truth. The substrate (the
// VMM) is authoritative; a later recovery pass reconciles stored records against
// the live substrate. No method on this interface implies that writing a state
// makes it canonical — callers should treat reads as advisory until reconciled.
//
// # In-flight operations and leases
// Transient operation states (cloning, forking, snapshotting, stopping,
// restoring) are deliberately absent from domain.State. An in-flight operation
// is represented as an exclusive flock held alongside the record — never as a
// state written into it. The kernel releases the flock automatically when the
// holding process dies (including SIGKILL), so a crashed operation cannot leave
// a sandbox stuck in a fake state. See lock.go for the crash-safety guarantee.
type Store interface {
	// Create persists a new sandbox record. Returns ErrAlreadyExists if a
	// sandbox with the same ID is already registered.
	Create(ctx context.Context, sb domain.Sandbox) error

	// Get retrieves a sandbox by its exact ID. Returns ErrNotFound if no such
	// sandbox exists.
	Get(ctx context.Context, id domain.SandboxID) (domain.Sandbox, error)

	// List returns all sandboxes whose records can be decoded by this binary.
	// A corrupt or unreadable individual record is silently skipped rather than
	// failing the entire list; this includes records written by a newer binary
	// (future schema version). An error is returned only if the root directory
	// itself cannot be read.
	List(ctx context.Context) ([]domain.Sandbox, error)

	// Update performs a read-modify-write of a sandbox record under an exclusive
	// per-sandbox flock, making concurrent CLI invocations safe. The callback
	// receives a pointer to the current record and may mutate it in place. If
	// the callback returns an error the record is not written. Returns
	// ErrNotFound if the sandbox does not exist.
	Update(ctx context.Context, id domain.SandboxID, fn func(*domain.Sandbox) error) error

	// Delete removes all persistent state for a sandbox. Returns ErrNotFound if
	// no such sandbox exists.
	Delete(ctx context.Context, id domain.SandboxID) error

	// SetRemovalMarker sets the write-ahead removal marker. It MUST be called
	// and must succeed before any destructive removal work begins. The required
	// ordering is:
	//
	//   SetRemovalMarker → destructive work (Delete)
	//
	// On successful removal, the marker dies with the record: Delete removes the
	// entire sandbox directory, including the marker, so nothing needs to clear
	// it afterward. If the process crashes after Set but before Delete, the
	// marker survives on disk. On the next recovery pass, a set marker on an
	// absent VM means removal was interrupted and is terminal: the sandbox must
	// be treated as gone and removal must not be retried. This mirrors Docker
	// writing a Dead state before its own destructive steps.
	SetRemovalMarker(ctx context.Context, id domain.SandboxID) error

	// ClearRemovalMarker clears the write-ahead removal marker. It is used
	// exclusively in the abandonment case: when recovery observes the marker set
	// but the VM is still alive, proving the removal did not complete and is no
	// longer in progress. Clearing the marker is correct only when the live
	// substrate proves removal did not occur — never call this after a successful
	// removal (the marker dies with the record via Delete in the normal path).
	ClearRemovalMarker(ctx context.Context, id domain.SandboxID) error

	// ResolveByPrefix finds the unique sandbox whose ID string starts with
	// prefix. Propagates domain.ErrNoMatch or domain.ErrAmbiguous (wrapped with
	// %w) so callers can use errors.As to extract the candidate list.
	ResolveByPrefix(ctx context.Context, prefix string) (domain.Sandbox, error)

	// ResolveByHandle finds the unique sandbox with the "<project>/<name>"
	// handle. Returns ErrNotFound if no sandbox matches.
	ResolveByHandle(ctx context.Context, handle string) (domain.Sandbox, error)

	// GetByLabels returns all sandboxes whose Labels map contains every
	// key=value pair in labels (AND-semantics). An empty labels argument
	// matches nothing and returns an empty (non-nil) slice.
	GetByLabels(ctx context.Context, labels map[string]string) ([]domain.Sandbox, error)

	// GetByMotive returns all sandboxes associated with the given motive ID.
	// Convenience wrapper over GetByLabels for the motive=<id> pattern.
	// An unknown motive ID returns an empty (non-nil) slice and nil error.
	// An empty motiveID argument matches nothing and returns an empty slice.
	GetByMotive(ctx context.Context, motiveID string) ([]domain.Sandbox, error)
}

// DefaultRoot returns the default state directory for nexus3, following the
// XDG Base Directory Specification: $XDG_STATE_HOME/nexus3 when set, otherwise
// ~/.local/state/nexus3.
func DefaultRoot() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "nexus3"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("store: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "nexus3"), nil
}
