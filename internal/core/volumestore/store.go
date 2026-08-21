// Package volumestore manages named volumes for the nexus3 sandbox system.
//
// Volumes live at <stateRoot>/volumes/<name>/ — a sibling directory to the
// sandbox disks directory.  ResourceIndex.List() in internal/core/service never
// scans the volumes directory; the separation is the structural reaper
// non-interference guarantee (D-PD-87).
//
// Ordering invariant (D-PD-89): meta.json is written BEFORE the backing file
// (disk.ext4 or data/) is materialised.
//
// Locking (VOL-LOCK): every mutating method — Create, Rm, Attach, Detach, and
// Prune — holds the per-volume advisory flock (LockPath) across its full
// check-then-write sequence.  The lock closes three TOCTOU windows:
//
//   - D1: volume prune deletes a stub record while Create is still materialising
//     the backing file.  Create holds the lock across the meta-write + materialise
//     window; Prune probes the lock before classifying each entry.
//   - D2: volume prune deletes a volume while a sandbox create has already
//     attached it but has not yet committed the sandbox record.  The service layer
//     holds the lock from checkRWAttach until store.Create commits.
//   - D3: Rm races with a concurrent Attach and deletes the volume directory
//     while Attach is writing an attachment.
package volumestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/store"
)

const (
	metaFile    = "meta.json"
	diskFile    = "disk.ext4"
	dataDirName = "data"

	// DefaultDiskSizeBytes is the size used for kind=disk volumes when no
	// explicit size is provided.
	DefaultDiskSizeBytes int64 = 10 * 1024 * 1024 * 1024 // 10 GiB
)

// nameRE enforces the volume name grammar: [a-z0-9][a-z0-9._-]*
// (same as Docker volume names, per D-PD-84).
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._\-]*$`)

// VolumeKind distinguishes how a volume is surfaced inside the guest.
type VolumeKind string

const (
	// KindDisk is a raw ext4 image attached as a virtio-blk block device.
	KindDisk VolumeKind = "disk"
	// KindDir is a host directory served over virtiofs.
	KindDir VolumeKind = "dir"
)

// VolumeAttachment records that a sandbox has this volume mounted.
type VolumeAttachment struct {
	SandboxID  string    `json:"sandbox_id"`
	AttachedAt time.Time `json:"attached_at"`
}

// VolumeRecord is the canonical state persisted in meta.json.
//
// Fields used by prune.go (SD2-4-CLI):
//   - Attachments: prune must check both meta.json and sandbox MountedVolumes
//     before removing a volume claimed to be detached.
//   - Kind: determines which backing resource prune looks for (disk.ext4 vs
//     data/ directory).
type VolumeRecord struct {
	Name        string             `json:"name"`
	Kind        VolumeKind         `json:"kind"`
	SizeBytes   int64              `json:"size_bytes,omitempty"`  // kind=disk only
	HostPath    string             `json:"host_path,omitempty"`   // kind=dir --path pin; "" = managed data/
	Attachments []VolumeAttachment `json:"attachments,omitempty"` // currently-attached sandboxes
	CreatedAt   time.Time          `json:"created_at"`
}

// VolumeStore manages named volumes in a dedicated directory tree.
// Construct with New; root should be <stateRoot>/volumes.
type VolumeStore struct {
	root string

	// testHookAfterMetaWrite is called after meta.json is written and before
	// the backing resource is materialised.  Set only in tests that need to
	// simulate a crash between the two steps (ordering proof for D-PD-89).
	// Nil in production.
	testHookAfterMetaWrite func() error

	// testHookAfterRmRead is called inside Rm after reading the volume record
	// (and verifying no attachments) but before deleting any files.  Set only
	// in tests that need to simulate a concurrent Attach racing with an Rm to
	// prove D3 TOCTOU protection.  Nil in production.
	testHookAfterRmRead func() error
}

// New returns a VolumeStore rooted at root.
// root is typically ~/.local/state/nexus3/volumes.
func New(root string) *VolumeStore {
	return &VolumeStore{root: root}
}

// Root returns the store's root directory.  Exposed so that callers (e.g.
// CLI, tests) can assert that the path is outside the sandbox disks directory.
func (s *VolumeStore) Root() string {
	return s.root
}

// DiskPath returns the expected path for a kind=disk volume's backing file.
// Exposed for prune.go.
func (s *VolumeStore) DiskPath(name string) string {
	return filepath.Join(s.volDir(name), diskFile)
}

// DataPath returns the expected managed data directory for a kind=dir volume
// without a pinned host path.  Exposed for prune.go.
func (s *VolumeStore) DataPath(name string) string {
	return filepath.Join(s.volDir(name), dataDirName)
}

func (s *VolumeStore) volDir(name string) string {
	return filepath.Join(s.root, name)
}

func (s *VolumeStore) metaPath(name string) string {
	return filepath.Join(s.volDir(name), metaFile)
}

// validateName returns an error if name does not match the allowed grammar.
func validateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("volume name %q: must match [a-z0-9][a-z0-9._-]*", name)
	}
	return nil
}

// readRecord reads and decodes the meta.json for name.
// Returns a wrapped os.ErrNotExist if the file is absent.
func (s *VolumeStore) readRecord(name string) (*VolumeRecord, error) {
	data, err := os.ReadFile(s.metaPath(name))
	if err != nil {
		return nil, err
	}
	var r VolumeRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("volume %s: decode meta.json: %w", name, err)
	}
	return &r, nil
}

// writeRecord persists rec to meta.json via an atomic write-then-rename.
func (s *VolumeStore) writeRecord(rec *VolumeRecord) error {
	dir := s.volDir(rec.Name)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("volume %s: marshal meta.json: %w", rec.Name, err)
	}
	tmp := filepath.Join(dir, ".meta.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("volume %s: write meta.json tmp: %w", rec.Name, err)
	}
	dest := filepath.Join(dir, metaFile)
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("volume %s: rename meta.json: %w", rec.Name, err)
	}
	return nil
}

// Create creates the named volume and returns its record.
//
// Idempotency (D-PD-84):
//   - Same kind as existing: returns the existing record with no error.
//   - Different kind from existing: returns a kind-conflict error.
//
// Ordering (D-PD-89): meta.json is written before the backing resource is
// materialised.  If the process crashes between those two steps, a stub record
// (meta.json without a backing file) is left on disk; prune.go handles it.
//
// Locking (D1): the per-volume advisory flock is held from before writeRecord
// until after materialise completes.  This prevents Prune from treating the
// transient "meta.json present, backing absent" state as a crash stub while
// the create is still in progress.  Crash recovery: the kernel releases the
// flock automatically on process death, so a genuinely crashed create leaves
// the stub claimable by the next prune run.
//
// sizeBytes applies to kind=disk only; pass ≤0 for DefaultDiskSizeBytes.
// hostPath applies to kind=dir only; pass "" for a managed data/ directory.
func (s *VolumeStore) Create(ctx context.Context, name string, kind VolumeKind, sizeBytes int64, hostPath string) (*VolumeRecord, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	if kind != KindDisk && kind != KindDir {
		return nil, fmt.Errorf("volume %s: unknown kind %q", name, kind)
	}

	// Fast-path idempotency check without the lock (avoids creating the vol dir
	// on the common re-create path).
	existing, err := s.readRecord(name)
	if err == nil {
		if existing.Kind != kind {
			return nil, fmt.Errorf("volume %s: kind conflict: existing kind=%s, requested kind=%s",
				name, existing.Kind, kind)
		}
		return existing, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("volume %s: check existing: %w", name, err)
	}

	// Volume does not exist (at fast-path check time). Create the directory and
	// take the per-volume lock before writing anything, so Prune cannot observe
	// the transient stub state while we are alive (D1).
	dir := s.volDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("volume %s: mkdir: %w", name, err)
	}

	lk, err := store.OpenLock(s.LockPath(name))
	if err != nil {
		return nil, fmt.Errorf("volume %s: open lock: %w", name, err)
	}
	defer lk.Close() //nolint:errcheck
	// Bound the acquisition so a hung lock-holder surfaces as an error, not a
	// hung CLI (TBD-PD-42).  Create is not a cleanup path — a cancelled parent
	// (Ctrl-C) must propagate promptly, so WithoutCancel is intentionally
	// absent here.  10 s matches the neighbouring guardCtx in service/create.go.
	lockCtx, lockCancel := context.WithTimeout(ctx, 10*time.Second)
	defer lockCancel()
	if err := lk.TryExclusive(lockCtx); err != nil {
		return nil, fmt.Errorf("volume %s: acquire lock: %w", name, err)
	}
	defer lk.Unlock() //nolint:errcheck

	// Authoritative idempotency check under the lock (another Create may have
	// committed between the fast-path check above and now).
	existing, err = s.readRecord(name)
	if err == nil {
		if existing.Kind != kind {
			return nil, fmt.Errorf("volume %s: kind conflict: existing kind=%s, requested kind=%s",
				name, existing.Kind, kind)
		}
		return existing, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("volume %s: check existing (locked): %w", name, err)
	}

	if sizeBytes <= 0 {
		sizeBytes = DefaultDiskSizeBytes
	}

	rec := &VolumeRecord{
		Name:      name,
		Kind:      kind,
		CreatedAt: time.Now().UTC(),
	}
	if kind == KindDisk {
		rec.SizeBytes = sizeBytes
	}
	if kind == KindDir && hostPath != "" {
		rec.HostPath = hostPath
	}

	// D-PD-89: write meta.json BEFORE materialising the backing resource.
	// The lock is held across both steps (D1): Prune cannot observe the
	// transient stub state while this process is alive.
	if err := s.writeRecord(rec); err != nil {
		return nil, err
	}

	// testHookAfterMetaWrite allows tests to call Prune from within the Create
	// window (while the lock is held) and verify that Prune KEEPs the volume.
	if s.testHookAfterMetaWrite != nil {
		if hookErr := s.testHookAfterMetaWrite(); hookErr != nil {
			return nil, hookErr
		}
	}

	// Materialise the backing resource.
	if err := s.materialise(ctx, rec); err != nil {
		// meta.json stays on disk intentionally: prune handles the stub after
		// the lock is released (the kernel releases it on process death too).
		return nil, fmt.Errorf("volume %s: materialise backing: %w", name, err)
	}

	return rec, nil
}

// materialise creates the backing file or directory for rec.
func (s *VolumeStore) materialise(ctx context.Context, rec *VolumeRecord) error {
	dir := s.volDir(rec.Name)
	switch rec.Kind {
	case KindDisk:
		diskPath := filepath.Join(dir, diskFile)
		if err := preallocateFile(diskPath, rec.SizeBytes); err != nil {
			return fmt.Errorf("preallocate disk: %w", err)
		}
		if err := formatExt4(ctx, diskPath); err != nil {
			_ = os.Remove(diskPath)
			return fmt.Errorf("format ext4: %w", err)
		}
	case KindDir:
		target := rec.HostPath
		if target == "" {
			target = filepath.Join(dir, dataDirName)
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("mkdir dir volume: %w", err)
		}
	}
	return nil
}

// Get returns the VolumeRecord for name, or an error if it does not exist.
func (s *VolumeStore) Get(name string) (*VolumeRecord, error) {
	rec, err := s.readRecord(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("volume %s: not found", name)
		}
		return nil, err
	}
	return rec, nil
}

// List returns all VolumeRecords in the store.
// Volumes with unreadable or missing meta.json are silently skipped.
func (s *VolumeStore) List() ([]*VolumeRecord, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("volume store list: %w", err)
	}
	var records []*VolumeRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rec, err := s.readRecord(e.Name())
		if err != nil {
			continue // skip stub / unreadable entries
		}
		records = append(records, rec)
	}
	return records, nil
}

// Rm removes the named volume and its backing resource.
// It refuses if the volume has any recorded attachments.
//
// Locking (D3): the per-volume advisory flock is held across the full
// read-check-delete sequence so that a concurrent Attach cannot write a new
// attachment between Rm's read (which sees zero attachments) and Rm's delete.
func (s *VolumeStore) Rm(ctx context.Context, name string) error {
	// Open the lock file for the volume. OpenLock creates the file if absent
	// (migration compat: pre-fix volumes have no lock file). ENOENT on the
	// volume directory itself surfaces as "not found".
	lk, err := store.OpenLock(s.LockPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("volume %s: not found", name)
		}
		return fmt.Errorf("volume %s: open lock for rm: %w", name, err)
	}
	defer lk.Close() //nolint:errcheck
	if err := lk.TryExclusive(ctx); err != nil {
		return fmt.Errorf("volume %s: acquire lock for rm: %w", name, err)
	}
	defer lk.Unlock() //nolint:errcheck

	rec, err := s.readRecord(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("volume %s: not found", name)
		}
		return err
	}
	if len(rec.Attachments) > 0 {
		ids := make([]string, len(rec.Attachments))
		for i, a := range rec.Attachments {
			ids[i] = a.SandboxID
		}
		return fmt.Errorf("volume %s: volume in use: attached to %s", name, strings.Join(ids, ", "))
	}

	// testHookAfterRmRead lets tests inject a concurrent Attach to verify that
	// the flock prevents the TOCTOU race (D3).  In production this is nil.
	if s.testHookAfterRmRead != nil {
		if hookErr := s.testHookAfterRmRead(); hookErr != nil {
			return hookErr
		}
	}

	dir := s.volDir(name)

	// Remove backing resources.
	diskPath := filepath.Join(dir, diskFile)
	if err := os.Remove(diskPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("volume %s: remove disk: %w", name, err)
	}
	dataPath := filepath.Join(dir, dataDirName)
	if err := os.RemoveAll(dataPath); err != nil {
		return fmt.Errorf("volume %s: remove data dir: %w", name, err)
	}
	// If the volume uses a pinned host path, we do NOT delete it —
	// it is user-owned and was only borrowed, not managed by us.

	// Remove meta.json, then the lock file (so the directory is empty), then
	// the directory itself. The lock file inode remains valid (and the flock
	// on the fd stays held) until lk.Close() runs in the defer — any other
	// process that had opened the lock file before this Remove will receive
	// ENOENT on its next open, surfacing as "not found".
	if err := os.Remove(s.metaPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("volume %s: remove meta.json: %w", name, err)
	}
	if err := os.Remove(s.LockPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("volume %s: remove lock: %w", name, err)
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("volume %s: remove volume dir: %w", name, err)
	}
	return nil
}

// Attach records sandboxID in the volume's attachment list.
// It is idempotent: a second call for the same sandboxID is a no-op.
//
// Locking (D3): the per-volume advisory flock is held across the full
// read-append-write sequence so that a concurrent Rm cannot delete the
// volume directory between Attach's read and Attach's write.
func (s *VolumeStore) Attach(ctx context.Context, name, sandboxID string) error {
	lk, err := store.OpenLock(s.LockPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("volume %s: attach: %w", name, os.ErrNotExist)
		}
		return fmt.Errorf("volume %s: attach: open lock: %w", name, err)
	}
	defer lk.Close() //nolint:errcheck
	if err := lk.TryExclusive(ctx); err != nil {
		return fmt.Errorf("volume %s: attach: acquire lock: %w", name, err)
	}
	defer lk.Unlock() //nolint:errcheck

	rec, err := s.readRecord(name)
	if err != nil {
		return fmt.Errorf("volume %s: attach: %w", name, err)
	}
	for _, a := range rec.Attachments {
		if a.SandboxID == sandboxID {
			return nil // already recorded
		}
	}
	rec.Attachments = append(rec.Attachments, VolumeAttachment{
		SandboxID:  sandboxID,
		AttachedAt: time.Now().UTC(),
	})
	return s.writeRecord(rec)
}

// AttachLocked records sandboxID in the volume's attachment list and returns
// the per-volume exclusive flock still held. The caller MUST release the lock
// by calling lk.Unlock()+lk.Close() after committing the sandbox record to the
// store (D2 fix for kind=dir and ro kind=disk). Holding the lock across
// store.Create prevents Prune from treating the volume as "detached" in the
// window between meta.json write and sandbox-record commit.
//
// ctx MUST carry a deadline (RISK-SD2-1): TryExclusive retries with a 5 ms
// backoff and surfaces a context-deadline error when the lock is held longer
// than the deadline, so a wedged peer does not hang the CLI.
//
// On error the lock is always released before returning; the caller receives
// (nil, err) and must not attempt a release.
//
// Do NOT call Attach and then separately acquire the lock on the same inode —
// that would open a second fd on the same inode and conflict under flock.
// This method is the only correct way to hold the lock across store.Create for
// the else-branch path.
func (s *VolumeStore) AttachLocked(ctx context.Context, name, sandboxID string) (*store.Lock, error) {
	lk, err := store.OpenLock(s.LockPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("volume %s: attach: %w", name, os.ErrNotExist)
		}
		return nil, fmt.Errorf("volume %s: attach: open lock: %w", name, err)
	}
	if err := lk.TryExclusive(ctx); err != nil {
		_ = lk.Close()
		return nil, fmt.Errorf("volume %s: attach: acquire lock: %w", name, err)
	}

	rec, err := s.readRecord(name)
	if err != nil {
		_ = lk.Unlock()
		_ = lk.Close()
		return nil, fmt.Errorf("volume %s: attach: %w", name, err)
	}
	for _, a := range rec.Attachments {
		if a.SandboxID == sandboxID {
			// already recorded — return held lock so caller's D2 window is still covered
			return lk, nil
		}
	}
	rec.Attachments = append(rec.Attachments, VolumeAttachment{
		SandboxID:  sandboxID,
		AttachedAt: time.Now().UTC(),
	})
	if err := s.writeRecord(rec); err != nil {
		_ = lk.Unlock()
		_ = lk.Close()
		return nil, fmt.Errorf("volume %s: attach: write: %w", name, err)
	}
	// Return with lock held — caller releases after sandbox record commits (D2).
	return lk, nil
}

// Detach removes sandboxID from the volume's attachment list.
// It is a no-op if sandboxID is not present or the volume no longer exists.
//
// Locking: the per-volume advisory flock is held across the read-filter-write
// sequence, serialising Detach with concurrent Attach and Prune calls.
func (s *VolumeStore) Detach(ctx context.Context, name, sandboxID string) error {
	lk, err := store.OpenLock(s.LockPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // volume gone — nothing to detach
		}
		return fmt.Errorf("volume %s: detach: open lock: %w", name, err)
	}
	defer lk.Close() //nolint:errcheck
	if err := lk.TryExclusive(ctx); err != nil {
		return fmt.Errorf("volume %s: detach: acquire lock: %w", name, err)
	}
	defer lk.Unlock() //nolint:errcheck

	rec, err := s.readRecord(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // volume gone — nothing to detach
		}
		return fmt.Errorf("volume %s: detach: %w", name, err)
	}
	filtered := rec.Attachments[:0]
	for _, a := range rec.Attachments {
		if a.SandboxID != sandboxID {
			filtered = append(filtered, a)
		}
	}
	rec.Attachments = filtered
	return s.writeRecord(rec)
}
