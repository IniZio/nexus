package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// currentSchemaVersion is the schema version written by this binary.
// A record with a higher version was written by a newer nexus3 and must not be
// decoded — partially understanding a record is worse than refusing it.
const currentSchemaVersion = 1

// record is the on-disk JSON representation of a sandbox. It is intentionally
// separate from domain.Sandbox so that:
//
//  1. Only the confirmed durable set of fields is persisted — a future domain
//     field cannot silently land on disk just because it was added to the struct.
//  2. A schema version can be stored without polluting the domain type.
//  3. The encoding contract is explicit and reviewable in one place.
//
// Durable fields (exactly these, nothing more):
//   - identity:         ID, Name, Project
//   - labels:           Labels (map[string]string; omitted when empty)
//   - legacy motive:    MotiveID (read-only backward compat; migrated to Labels["motive"] on load)
//   - frozen config:    Envelope
//   - state cache:      State
//   - run identity:     InstanceID
//   - policy:           RemoveOnExit
//   - WAL marker:       RemovalMarker
//   - stop qualifier:   StopReason (omitted when empty for backward compatibility)
//   - fork lineage:     Provenance (omitted for non-fork sandboxes)
//   - git anchor:       BaseRef (40-hex SHA; omitted for sandboxes without a git workspace)
//   - netns adoption:   NetnsChildPID, NetnsChildPGID, NetnsChildStartTime,
//     GuestTapName, CHAPISocket (all omitted when zero/empty)
type record struct {
	SchemaVersion int              `json:"schema_version"`
	ID            domain.SandboxID `json:"id"`
	Name          string           `json:"name"`
	Project       string           `json:"project"`
	// Labels is the current label store. Written by all new records.
	Labels map[string]string `json:"labels,omitempty"`
	// MotiveID is a legacy field present in records written before the Labels
	// field existed. On read, a non-empty MotiveID is migrated into
	// Labels["motive"]. New records never write this field.
	MotiveID       string            `json:"motive_id,omitempty"`
	State          domain.State      `json:"state"`
	Envelope       domain.Envelope   `json:"envelope"`
	InstanceID     string            `json:"instance_id"`
	RemoveOnExit   bool              `json:"remove_on_exit"`
	RemovalMarker  bool              `json:"removal_marker"`
	StopReason     domain.StopReason `json:"stop_reason,omitempty"`
	Provenance     *provenanceRecord `json:"provenance,omitempty"`
	SupervisorPID  int               `json:"supervisor_pid,omitempty"`
	SupervisorSock string            `json:"supervisor_sock,omitempty"`
	CreatorPID     int               `json:"creator_pid,omitempty"`
	// BaseRef is the shallow-clone boundary SHA recorded by G1 (D-PD-19).
	// Omitted for sandboxes created without a git workspace.
	BaseRef string `json:"base_ref,omitempty"`
	// MountedVolumes records named volumes attached at create time (D-PD-82).
	// Omitted for sandboxes with no attached volumes; nil on read is equivalent
	// to an empty slice. Backward-compatible: old records without this field
	// deserialise to nil, which is the correct default (no volumes).
	MountedVolumes []domain.VolumeAttachment `json:"mounted_volumes,omitempty"`
	// LiveMounts records live host-directory virtiofs shares attached at create
	// time (D-PD-53). Omitted when nil; nil on read is equivalent to no mounts.
	// Backward-compatible: old records without this field deserialise to nil.
	LiveMounts []domain.LiveMount `json:"live_mounts,omitempty"`
	// AgentName records the agent profile the sandbox was created for (TBD-PD-32).
	// Empty for plain sandboxes and for records written before the field existed.
	AgentName string `json:"agent_name,omitempty"`
	// Netns adoption fields — five values the supervisor captures from
	// StartNetnsRuntime so a replacement supervisor can call AdoptNetnsRuntime
	// without re-deriving them from ps/nsenter. All omitted when zero/empty
	// (i.e. when no netns child is running).
	NetnsChildPID       int    `json:"netns_child_pid,omitempty"`
	NetnsChildPGID      int    `json:"netns_child_pgid,omitempty"`
	NetnsChildStartTime uint64 `json:"netns_child_start_time,omitempty"`
	GuestTapName        string `json:"guest_tap_name,omitempty"`
	CHAPISocket         string `json:"ch_api_socket,omitempty"`
	NetnsControlSocket  string `json:"netns_control_socket,omitempty"`
	NetnsControlToken   string `json:"netns_control_token,omitempty"`
}

// provenanceRecord is the on-disk form of domain.Provenance. Kept separate
// from domain.Provenance so the encoding contract remains explicit and
// the domain package stays free of encoding concerns.
type provenanceRecord struct {
	ParentID       domain.SandboxID `json:"parent_id"`
	SourceSnapshot string           `json:"source_snapshot"`
}

func toRecord(sb domain.Sandbox) record {
	r := record{
		SchemaVersion:       currentSchemaVersion,
		ID:                  sb.ID,
		Name:                sb.Name,
		Project:             sb.Project,
		Labels:              sb.Labels,
		State:               sb.State,
		Envelope:            sb.Envelope,
		InstanceID:          sb.InstanceID,
		RemoveOnExit:        sb.RemoveOnExit,
		RemovalMarker:       sb.RemovalMarker,
		StopReason:          sb.StopReason,
		SupervisorPID:       sb.SupervisorPID,
		SupervisorSock:      sb.SupervisorSock,
		CreatorPID:          sb.CreatorPID,
		BaseRef:             sb.BaseRef,
		MountedVolumes:      sb.MountedVolumes,
		LiveMounts:          sb.LiveMounts,
		AgentName:           sb.AgentName,
		NetnsChildPID:       sb.NetnsChildPID,
		NetnsChildPGID:      sb.NetnsChildPGID,
		NetnsChildStartTime: sb.NetnsChildStartTime,
		GuestTapName:        sb.GuestTapName,
		CHAPISocket:         sb.CHAPISocket,
		NetnsControlSocket:  sb.NetnsControlSocket,
		NetnsControlToken:   sb.NetnsControlToken,
		// MotiveID intentionally omitted: new records never write this field.
	}
	if sb.Provenance != nil {
		r.Provenance = &provenanceRecord{
			ParentID:       sb.Provenance.ParentID,
			SourceSnapshot: sb.Provenance.SourceSnapshot,
		}
	}
	return r
}

func (r record) toDomain() domain.Sandbox {
	// Start with the stored Labels map (may be nil for old records).
	labels := r.Labels
	// Backward compat: a legacy record carries MotiveID but no Labels.
	// Migrate the value into Labels["motive"] so callers need not know about
	// the old field. If both exist (should not happen but be defensive), the
	// stored Labels["motive"] takes precedence.
	if r.MotiveID != "" {
		if labels == nil {
			labels = make(map[string]string, 1)
		}
		if labels["motive"] == "" {
			labels["motive"] = r.MotiveID
		}
	}

	sb := domain.Sandbox{
		ID:                  r.ID,
		Name:                r.Name,
		Project:             r.Project,
		Labels:              labels,
		State:               r.State,
		Envelope:            r.Envelope,
		InstanceID:          r.InstanceID,
		RemoveOnExit:        r.RemoveOnExit,
		RemovalMarker:       r.RemovalMarker,
		StopReason:          r.StopReason,
		SupervisorPID:       r.SupervisorPID,
		SupervisorSock:      r.SupervisorSock,
		CreatorPID:          r.CreatorPID,
		BaseRef:             r.BaseRef,
		MountedVolumes:      r.MountedVolumes,
		LiveMounts:          r.LiveMounts,
		AgentName:           r.AgentName,
		NetnsChildPID:       r.NetnsChildPID,
		NetnsChildPGID:      r.NetnsChildPGID,
		NetnsChildStartTime: r.NetnsChildStartTime,
		GuestTapName:        r.GuestTapName,
		CHAPISocket:         r.CHAPISocket,
		NetnsControlSocket:  r.NetnsControlSocket,
		NetnsControlToken:   r.NetnsControlToken,
	}
	if r.Provenance != nil {
		sb.Provenance = &domain.Provenance{
			ParentID:       r.Provenance.ParentID,
			SourceSnapshot: r.Provenance.SourceSnapshot,
		}
	}
	return sb
}

// ErrSchemaTooNew is returned when a stored record has a schema version higher
// than this binary supports. Reading such a record partially would silently drop
// fields, which is worse than refusing it outright. Upgrade nexus3 to read it.
type ErrSchemaTooNew struct {
	Found int
	Max   int
}

func (e *ErrSchemaTooNew) Error() string {
	return fmt.Sprintf(
		"store: record has schema version %d but this binary supports up to %d; upgrade nexus3",
		e.Found, e.Max,
	)
}

// FileStore is a filesystem-backed implementation of Store.
//
// # Layout
// Each sandbox lives in its own subdirectory under root/sandboxes/<id>/:
//
//	root/sandboxes/<id>/record.json   — the durable record (atomic temp+rename target)
//	root/sandboxes/<id>/lock          — the flock file (never renamed or replaced)
//
// One directory per sandbox means one sandbox's corruption never prevents
// reading others.
//
// # Atomic writes and durability
// Every record write creates a temporary file in the same directory, syncs the
// file data to disk, renames it over the target, then fsyncs the containing
// directory. A process kill at any point leaves either the old record or the
// new one intact (rename(2) is atomic in the VFS). A power cut is handled by
// the post-rename directory fsync: without it the directory entry may never
// reach stable storage and the rename can be silently lost even though the file
// data was synced. See writeRecord for the precise guarantee.
//
// # Concurrent access
// Per-sandbox flock(2) exclusion serialises Update calls across concurrent CLI
// invocations. The lock file is never renamed or replaced; its inode identity
// is stable for the lifetime of the sandbox. See lock.go for the kernel
// crash-release guarantee.
type FileStore struct {
	root string
}

// NewFileStore creates a FileStore rooted at root. The root directory (and its
// sandboxes subdirectory) are created if they do not exist.
func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Join(root, "sandboxes"), 0700); err != nil {
		return nil, fmt.Errorf("store: init root %s: %w", root, err)
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) sandboxDir(id domain.SandboxID) string {
	return filepath.Join(s.root, "sandboxes", id.String())
}

func (s *FileStore) recordPath(id domain.SandboxID) string {
	return filepath.Join(s.sandboxDir(id), "record.json")
}

func (s *FileStore) lockPath(id domain.SandboxID) string {
	return filepath.Join(s.sandboxDir(id), "lock")
}

// Create persists a new sandbox. Returns ErrAlreadyExists if the ID is already
// registered.
//
// The sandbox directory is created atomically via os.Mkdir. If two concurrent
// processes both call Create with the same ID, exactly one succeeds and the
// other gets ErrAlreadyExists. A directory that exists but has no record (from
// an interrupted Create) is treated as existing.
func (s *FileStore) Create(ctx context.Context, sb domain.Sandbox) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateState(sb.State); err != nil {
		return fmt.Errorf("store: create: %w", err)
	}
	dir := s.sandboxDir(sb.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrAlreadyExists, sb.ID)
		}
		return fmt.Errorf("store: create: mkdir %s: %w", dir, err)
	}
	// fsync the sandboxes directory so the new sandbox directory entry is
	// durable across a power cut.
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return fmt.Errorf("store: create: %w", err)
	}
	// Pre-create the lock file so it is available for later Update/Delete calls
	// without a creation race.
	lf, err := os.OpenFile(s.lockPath(sb.ID), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("store: create: init lock file: %w", err)
	}
	_ = lf.Close()

	if err := writeRecord(s.recordPath(sb.ID), toRecord(sb)); err != nil {
		return fmt.Errorf("store: create: %w", err)
	}
	return nil
}

// Get retrieves a sandbox by its exact ID. Returns ErrNotFound if the sandbox
// does not exist or its record cannot be decoded.
//
// Unlike List, Get fails loudly on a future-version record: a single targeted
// lookup should surface the problem so the operator knows to upgrade nexus3.
func (s *FileStore) Get(ctx context.Context, id domain.SandboxID) (domain.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return domain.Sandbox{}, err
	}
	r, err := readRecord(s.recordPath(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.Sandbox{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return domain.Sandbox{}, fmt.Errorf("store: get %s: %w", id, err)
	}
	return r.toDomain(), nil
}

// List returns all sandboxes whose records this binary can decode.
//
// A corrupt record, an interrupted Create (dir with no record), or a record
// written by a newer binary are all silently skipped: one bad entry must not
// prevent the CLI from listing the rest. An error is returned only if the
// sandboxes directory itself cannot be read.
//
// This diverges deliberately from Get, which fails loudly on future-version
// records: nexus3 ls must remain usable even when one record was written by a
// newer binary.
func (s *FileStore) List(ctx context.Context) ([]domain.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sandboxesDir := filepath.Join(s.root, "sandboxes")
	entries, err := os.ReadDir(sandboxesDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: list: read %s: %w", sandboxesDir, err)
	}
	var out []domain.Sandbox
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := readRecord(filepath.Join(sandboxesDir, e.Name(), "record.json"))
		if err != nil {
			// Skip: corrupt, missing (interrupted Create), or future-version.
			continue
		}
		out = append(out, r.toDomain())
	}
	return out, nil
}

// GetByLabels returns all sandboxes whose Labels map contains every key=value
// pair in the labels argument (AND-semantics). An empty labels argument matches
// nothing and returns an empty (non-nil) slice. An unknown label combination
// returns an empty (non-nil) slice with nil error.
//
// Legacy records carrying a MotiveID field (written before the Labels field
// existed) are transparently migrated: their MotiveID value is accessible as
// Labels["motive"] and participates in the AND-match.
func (s *FileStore) GetByLabels(ctx context.Context, labels map[string]string) ([]domain.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		return []domain.Sandbox{}, nil
	}
	all, err := s.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: get-by-labels: %w", err)
	}
	out := []domain.Sandbox{}
	for _, sb := range all {
		match := true
		for k, v := range labels {
			if sb.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, sb)
		}
	}
	return out, nil
}

// GetByMotive returns all sandboxes whose Labels["motive"] equals motiveID.
// This is a convenience wrapper over GetByLabels for the common motive=<id>
// selection pattern. Legacy records with a MotiveID field are transparently
// migrated and matched correctly.
//
// An empty motiveID matches nothing and returns an empty (non-nil) slice.
// An unknown motiveID returns an empty (non-nil) slice with nil error.
func (s *FileStore) GetByMotive(ctx context.Context, motiveID string) ([]domain.Sandbox, error) {
	if motiveID == "" {
		return []domain.Sandbox{}, nil
	}
	return s.GetByLabels(ctx, map[string]string{"motive": motiveID})
}

// Update performs a read-modify-write of a sandbox record under an exclusive
// per-sandbox flock. The callback receives a pointer to the current domain
// value and may mutate it. The result is written atomically only if the
// callback succeeds. Returns ErrNotFound if the sandbox does not exist.
func (s *FileStore) Update(ctx context.Context, id domain.SandboxID, fn func(*domain.Sandbox) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lk, err := OpenLock(s.lockPath(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return fmt.Errorf("store: update: open lock: %w", err)
	}
	defer lk.Close() //nolint:errcheck

	if err := lk.Exclusive(ctx); err != nil {
		return fmt.Errorf("store: update: acquire lock: %w", err)
	}
	defer lk.Unlock() //nolint:errcheck

	r, err := readRecord(s.recordPath(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return fmt.Errorf("store: update %s: read: %w", id, err)
	}
	sb := r.toDomain()
	if err := fn(&sb); err != nil {
		return err
	}
	if err := validateState(sb.State); err != nil {
		return fmt.Errorf("store: update: %w", err)
	}
	if err := writeRecord(s.recordPath(id), toRecord(sb)); err != nil {
		return fmt.Errorf("store: update %s: write: %w", id, err)
	}
	return nil
}

// Delete removes all persistent state for a sandbox.
//
// The sandbox directory is removed under an exclusive flock. A concurrent
// process holding the now-unlinked lock inode still holds a valid flock but
// any subsequent record reads will return ErrNotFound — which is correct.
func (s *FileStore) Delete(ctx context.Context, id domain.SandboxID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := s.sandboxDir(id)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return fmt.Errorf("store: delete %s: stat: %w", id, err)
	}
	lk, err := OpenLock(s.lockPath(id))
	if err != nil {
		return fmt.Errorf("store: delete: open lock: %w", err)
	}
	defer lk.Close() //nolint:errcheck

	if err := lk.Exclusive(ctx); err != nil {
		return fmt.Errorf("store: delete: acquire lock: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		_ = lk.Unlock()
		return fmt.Errorf("store: delete %s: remove: %w", id, err)
	}
	// fsync the sandboxes directory so the removal of the sandbox directory
	// entry is durable across a power cut.
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return fmt.Errorf("store: delete %s: %w", id, err)
	}
	// lk.Close() via defer releases the flock on the now-unlinked inode.
	return nil
}

// SetRemovalMarker sets the write-ahead removal marker. See Store.SetRemovalMarker
// for the ordering contract.
func (s *FileStore) SetRemovalMarker(ctx context.Context, id domain.SandboxID) error {
	return s.Update(ctx, id, func(sb *domain.Sandbox) error {
		sb.RemovalMarker = true
		return nil
	})
}

// ClearRemovalMarker clears the write-ahead removal marker after all destructive
// work has succeeded. See Store.ClearRemovalMarker for the crash semantics.
func (s *FileStore) ClearRemovalMarker(ctx context.Context, id domain.SandboxID) error {
	return s.Update(ctx, id, func(sb *domain.Sandbox) error {
		sb.RemovalMarker = false
		return nil
	})
}

// ResolveByPrefix finds the unique sandbox whose ID string starts with prefix.
// Propagates domain.ErrNoMatch or domain.ErrAmbiguous wrapped with %w so that
// callers can use errors.As to recover candidate lists.
func (s *FileStore) ResolveByPrefix(ctx context.Context, prefix string) (domain.Sandbox, error) {
	all, err := s.List(ctx)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("store: resolve prefix: %w", err)
	}
	ids := make([]domain.SandboxID, len(all))
	for i, sb := range all {
		ids[i] = sb.ID
	}
	id, err := domain.ResolvePrefix(prefix, ids)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("store: resolve prefix: %w", err)
	}
	return s.Get(ctx, id)
}

// ResolveByHandle finds the unique sandbox with the "<project>/<name>" handle.
// Returns ErrNotFound if no sandbox matches.
func (s *FileStore) ResolveByHandle(ctx context.Context, handle string) (domain.Sandbox, error) {
	project, name, err := domain.ParseHandle(handle)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("store: resolve handle: %w", err)
	}
	all, err := s.List(ctx)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("store: resolve handle: %w", err)
	}
	for _, sb := range all {
		if sb.Project == project && sb.Name == name {
			return sb, nil
		}
	}
	return domain.Sandbox{}, fmt.Errorf("%w: handle %q", ErrNotFound, handle)
}

// validateState returns an error if state is not one of the five valid durable
// states. Zero (the Go zero value) is not valid and would produce an unreadable
// record if persisted.
func validateState(state domain.State) error {
	if slices.Contains(domain.AllStates(), state) {
		return nil
	}
	return fmt.Errorf("invalid state %v; must be one of the five durable states (created/running/paused/stopped/error)", state)
}

// writeRecord atomically persists r to path via temp-file + fsync + rename +
// directory fsync.
//
// Failure-mode guarantees:
//   - Process kill (SIGKILL, panic): rename(2) is atomic in the VFS — at any
//     point in this function, readers see either the old record or the new one.
//     A torn or partial record is impossible.
//   - Power loss / host crash: VFS atomicity is not sufficient. The kernel may
//     never flush the updated directory entry before the crash, silently losing
//     the rename even though the file data was synced. The syncDir call after
//     rename fsyncs the containing directory so the rename is durable on disk.
//     Errors from syncDir are propagated; callers must treat them as write
//     failures (the record is not durably committed).
//
// Platform note: on macOS, (*os.File).Sync() calls fsync(2), which flushes the
// OS page cache to the drive controller but does NOT force the drive's internal
// write cache to stable storage. The stronger F_FULLFSYNC primitive (fcntl(2))
// is required for true power-loss durability on macOS. It is not implemented
// here — revisit when concrete macOS durability requirements are established.
func writeRecord(path string, r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("store: marshal record: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".record-*.tmp")
	if err != nil {
		return fmt.Errorf("store: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Remove the temp file on any error path so stale files don't accumulate.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write temp file: %w", err)
	}
	// fsync before rename so the kernel cannot reorder the rename before the
	// data write, which would leave an empty file after a crash.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: rename to %s: %w", path, err)
	}
	// fsync the containing directory so the renamed directory entry is durable
	// across a power cut. Without this the rename may be lost on reboot even
	// though the file data was synced.
	if err := syncDir(dir); err != nil {
		return err
	}
	success = true
	return nil
}

// syncDir opens the directory at dir and calls Sync() on it, committing the
// directory's updated entries (e.g. a newly renamed file) to stable storage.
//
// This is the POSIX-recommended way to make rename(2) durable after a power
// failure: fsync the file, rename, fsync the directory. Skipping the directory
// fsync means the rename can be silently lost on a host crash even if the file
// data reached disk.
//
// Platform note: on macOS, Sync() calls fsync(2), which does NOT flush the
// drive's internal write cache. F_FULLFSYNC is the correct primitive for true
// durability on macOS but is not implemented here — see writeRecord for details.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("store: open dir for sync %s: %w", dir, err)
	}
	defer d.Close() //nolint:errcheck
	if err := d.Sync(); err != nil {
		return fmt.Errorf("store: fsync dir %s: %w", dir, err)
	}
	return nil
}

// readRecord performs a two-phase decode of the record at path.
//
// Phase 1 decodes only the schema version so that a future-version record
// produces a clear "upgrade nexus3" error rather than a confusing JSON type
// error from an incompatible field. Phase 2 performs the full decode.
func readRecord(path string) (record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return record{}, err
	}
	// Phase 1: version check.
	var versionProbe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &versionProbe); err != nil {
		return record{}, fmt.Errorf("store: decode schema version from %s: %w", path, err)
	}
	if versionProbe.SchemaVersion > currentSchemaVersion {
		return record{}, &ErrSchemaTooNew{
			Found: versionProbe.SchemaVersion,
			Max:   currentSchemaVersion,
		}
	}
	// Phase 2: full decode.
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return record{}, fmt.Errorf("store: decode record from %s: %w", path, err)
	}
	return r, nil
}
