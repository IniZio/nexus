// Package volumestore manages named volumes for the nexus3 sandbox system.
//
// Volumes live at <stateRoot>/volumes/<name>/ — a sibling directory to the
// sandbox disks directory.  ResourceIndex.List() in internal/core/service never
// scans the volumes directory; the separation is the structural reaper
// non-interference guarantee (D-PD-87).
//
// Ordering invariant (D-PD-89): meta.json is written BEFORE the backing file
// (disk.ext4 or data/) is materialised.  No flock lease is used because the
// reaper never reaches this directory; crash recovery is handled by
// `volume prune` (implemented in prune.go, owned by SD2-4-CLI).
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
// sizeBytes applies to kind=disk only; pass ≤0 for DefaultDiskSizeBytes.
// hostPath applies to kind=dir only; pass "" for a managed data/ directory.
func (s *VolumeStore) Create(ctx context.Context, name string, kind VolumeKind, sizeBytes int64, hostPath string) (*VolumeRecord, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	if kind != KindDisk && kind != KindDir {
		return nil, fmt.Errorf("volume %s: unknown kind %q", name, kind)
	}

	// Idempotency check: read any existing record.
	existing, err := s.readRecord(name)
	if err == nil {
		// Volume already exists.
		if existing.Kind != kind {
			return nil, fmt.Errorf("volume %s: kind conflict: existing kind=%s, requested kind=%s",
				name, existing.Kind, kind)
		}
		return existing, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("volume %s: check existing: %w", name, err)
	}

	// Volume does not exist — create it.
	if sizeBytes <= 0 {
		sizeBytes = DefaultDiskSizeBytes
	}

	dir := s.volDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("volume %s: mkdir: %w", name, err)
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
	// A crash after this point leaves a stub record that prune.go can clean.
	if err := s.writeRecord(rec); err != nil {
		_ = os.Remove(dir) // best-effort; may fail if dir has content
		return nil, err
	}

	// testHookAfterMetaWrite allows tests to simulate a crash here and verify
	// that meta.json exists while the backing resource does not yet.
	if s.testHookAfterMetaWrite != nil {
		if hookErr := s.testHookAfterMetaWrite(); hookErr != nil {
			return nil, hookErr
		}
	}

	// Materialise the backing resource.
	if err := s.materialise(ctx, rec); err != nil {
		// meta.json stays on disk intentionally: prune handles the stub.
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
func (s *VolumeStore) Rm(name string) error {
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

	// Remove meta.json then the volume directory.
	if err := os.Remove(s.metaPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("volume %s: remove meta.json: %w", name, err)
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("volume %s: remove volume dir: %w", name, err)
	}
	return nil
}

// Attach records sandboxID in the volume's attachment list.
// It is idempotent: a second call for the same sandboxID is a no-op.
func (s *VolumeStore) Attach(name, sandboxID string) error {
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

// Detach removes sandboxID from the volume's attachment list.
// It is a no-op if sandboxID is not present or the volume no longer exists.
func (s *VolumeStore) Detach(name, sandboxID string) error {
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
