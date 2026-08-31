package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus3/internal/core/diskname"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ResourceKind identifies the type of a host resource.
type ResourceKind string

const (
	KindDiskRaw           ResourceKind = "disk_raw"
	KindDiskWorkspace     ResourceKind = "disk_workspace"
	KindDiskShadow        ResourceKind = "disk_shadow"        // shadow disk (handle-keyed, not ULID-keyed)
	KindCreateIntent      ResourceKind = "create_intent"      // <diskDir>/<ULID>.create-intent.json
	KindShadowIntent      ResourceKind = "shadow_intent"      // <diskDir>/<safeHandle>.shadow-intent.json
	KindSocketAPI         ResourceKind = "socket_api"
	KindSocketVSock       ResourceKind = "socket_vsock"
	KindSocketIID         ResourceKind = "socket_iid"
	KindBuilderSupervisor ResourceKind = "builder_supervisor"

	// KindNetnsProcess identifies a LIVE netns-runtime child process
	// discovered by an independent /proc sweep (see reap.go:
	// sweepOrphanNetnsProcesses), not by ResourceIndex.List(). Unlike every
	// other kind, this one has no file on disk to enumerate — a process that
	// survives cleanup() has, by construction, already had its socket and
	// disk files removed (ticket 10, ch_2026-08-30). Its Path field carries
	// the CH API socket path the process reported in its own environ (for
	// identification/logging), not a file to stat or delete.
	KindNetnsProcess ResourceKind = "netns_process"
)

// HostResource is a single resource enumerated directly from the filesystem.
type HostResource struct {
	Kind    ResourceKind
	Path    string
	OwnerID domain.SandboxID // zero for KindDiskShadow (handle-keyed)
	// ShadowHandle is the B1-format safeHandle for KindDiskShadow and
	// KindShadowIntent resources:
	// the sandbox handle with "/" replaced by "_". Empty for legacy shadow
	// disks (*.shadow.ext4) and for all other resource kinds. Correlate
	// against live sandboxes via strings.ReplaceAll(sb.Handle(), "/", "_").
	ShadowHandle string
}

// IndexConfig holds the directories ResourceIndex reads from. Empty fields
// fall back to environment-derived defaults, which enables callers to override
// them in tests without touching environment variables.
type IndexConfig struct {
	StateRoot string // empty → store.DefaultRoot()
	SocketDir string // empty → $XDG_RUNTIME_DIR/nexus3 (or $TMPDIR/nexus3-<uid>)
}

// ResourceIndex enumerates host resources directly from the filesystem.
// It never reads the record store — its purpose is to surface resources that
// exist on disk regardless of whether a store record exists for them.
type ResourceIndex struct {
	cfg IndexConfig
}

// NewResourceIndex constructs a ResourceIndex with the given configuration.
func NewResourceIndex(cfg IndexConfig) *ResourceIndex {
	return &ResourceIndex{cfg: cfg}
}

// stateRoot resolves the effective state root, applying the default when the
// config field is empty.
func (x *ResourceIndex) stateRoot() (string, error) {
	if x.cfg.StateRoot != "" {
		return x.cfg.StateRoot, nil
	}
	return store.DefaultRoot()
}

// socketDir resolves the effective socket directory, applying the default when
// the config field is empty.
func (x *ResourceIndex) socketDir() (string, error) {
	if x.cfg.SocketDir != "" {
		return x.cfg.SocketDir, nil
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "nexus3"), nil
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("nexus3-%d", os.Getuid())), nil
}

// List enumerates all known host resources by scanning the filesystem
// directly. It never consults the record store.
//
// Resources whose filenames do not parse as a SandboxID are silently skipped.
// Directories that do not exist are treated as empty (no error).
func (x *ResourceIndex) List() ([]HostResource, error) {
	stateRoot, err := x.stateRoot()
	if err != nil {
		return nil, fmt.Errorf("resource index: resolve state root: %w", err)
	}
	socketDir, err := x.socketDir()
	if err != nil {
		return nil, fmt.Errorf("resource index: resolve socket dir: %w", err)
	}

	var resources []HostResource

	// ── disks/ ──────────────────────────────────────────────────────────────
	disksDir := filepath.Join(stateRoot, "disks")
	diskEntries, err := os.ReadDir(disksDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("resource index: read disks dir %s: %w", disksDir, err)
	}
	for _, e := range diskEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(disksDir, name)

		switch {
		case strings.HasSuffix(name, ".raw"):
			stem := strings.TrimSuffix(name, ".raw")
			id, err := domain.ParseSandboxID(stem)
			if err != nil {
				continue // not a sandbox disk
			}
			resources = append(resources, HostResource{Kind: KindDiskRaw, Path: path, OwnerID: id})

		case strings.HasSuffix(name, "-workspace.ext4"):
			stem := strings.TrimSuffix(name, "-workspace.ext4")
			id, err := domain.ParseSandboxID(stem)
			if err != nil {
				continue
			}
			resources = append(resources, HostResource{Kind: KindDiskWorkspace, Path: path, OwnerID: id})

		case strings.HasSuffix(name, shadowIntentSuffix):
			// Shadow intent: published before any shadow disk for this handle
			// is materialised, deleted on clean completion. A surviving intent
			// means the create died mid-flight. Enumerated so Reap can probe
			// its lease and keep the in-flight handle's disks (TBD-PD-25).
			resources = append(resources, HostResource{
				Kind:         KindShadowIntent,
				Path:         path,
				ShadowHandle: strings.TrimSuffix(name, shadowIntentSuffix),
			})

		case diskname.IsShadowDisk(name):
			// Shadow disk (handle-keyed, §4.4 supplementary correlation).
			// Enumeration is record-free: we only extract the embedded safeHandle
			// (if any). Correlation against live sandboxes happens in Reap().
			safeHandle, _ := diskname.ShadowDiskSafeHandle(name)
			resources = append(resources, HostResource{
				Kind:         KindDiskShadow,
				Path:         path,
				ShadowHandle: safeHandle, // "" for legacy format
			})

		case strings.HasSuffix(name, ".create-intent.json"):
			// Create-intent journal: written before materialisation, deleted on
			// clean completion. A surviving intent file means the creating process
			// died before committing the store record. Must be enumerated so the
			// reaper can reclaim both the intent and any partially-created disks.
			stem := strings.TrimSuffix(name, ".create-intent.json")
			id, err := domain.ParseSandboxID(stem)
			if err != nil {
				continue
			}
			resources = append(resources, HostResource{Kind: KindCreateIntent, Path: path, OwnerID: id})
		}
	}

	// ── socket dir ──────────────────────────────────────────────────────────
	sockEntries, err := os.ReadDir(socketDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("resource index: read socket dir %s: %w", socketDir, err)
	}
	for _, e := range sockEntries {
		if e.IsDir() {
			continue // skip snapshots/ and any other subdirs
		}
		name := e.Name()
		path := filepath.Join(socketDir, name)

		var kind ResourceKind
		var stem string
		switch {
		case strings.HasSuffix(name, ".sock"):
			kind = KindSocketAPI
			stem = strings.TrimSuffix(name, ".sock")
		case strings.HasSuffix(name, ".vsock"):
			kind = KindSocketVSock
			stem = strings.TrimSuffix(name, ".vsock")
		case strings.HasSuffix(name, ".iid"):
			kind = KindSocketIID
			stem = strings.TrimSuffix(name, ".iid")
		default:
			continue
		}

		id, err := domain.ParseSandboxID(stem)
		if err != nil {
			continue
		}
		resources = append(resources, HostResource{Kind: kind, Path: path, OwnerID: id})
	}

	// ── builder-supervisors/ ─────────────────────────────────────────────────
	bsDir := filepath.Join(stateRoot, "builder-supervisors")
	bsEntries, err := os.ReadDir(bsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("resource index: read builder-supervisors dir %s: %w", bsDir, err)
	}
	for _, e := range bsEntries {
		if !e.IsDir() {
			continue
		}
		id, err := domain.ParseSandboxID(e.Name())
		if err != nil {
			continue
		}
		resources = append(resources, HostResource{
			Kind:    KindBuilderSupervisor,
			Path:    filepath.Join(bsDir, e.Name()),
			OwnerID: id,
		})
	}

	return resources, nil
}
