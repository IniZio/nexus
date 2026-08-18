package volumestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// SandboxLister is a narrow read-only interface for accessing sandbox records.
// *store.FileStore satisfies this interface; it is defined here so that
// volumestore imports neither internal/core/service nor internal/core/store,
// keeping the package free of upward dependencies.
type SandboxLister interface {
	List(ctx context.Context) ([]domain.Sandbox, error)
}

// PruneOptions controls what prune deletes.
type PruneOptions struct {
	// Apply, when true, performs the deletions. When false the report is
	// produced but nothing is modified (dry-run).
	Apply bool

	// IncludeDetached, when true and Apply is also true, deletes volumes that
	// are detached — no live sandbox references them from either source.
	// Without this flag detached volumes are only reported as candidates.
	IncludeDetached bool
}

// PruneResult summarises the outcome of a Prune call.
type PruneResult struct {
	// StubsDeleted lists volume names whose stub meta.json record was deleted
	// (case a: meta.json present, backing file absent).
	StubsDeleted []string

	// OrphanedFilesDeleted lists file paths of orphaned backing resources that
	// were deleted (case b: backing file present, no meta.json).
	OrphanedFilesDeleted []string

	// DetachedCandidates lists volume names that are detached but were NOT
	// deleted because --include-detached was not set or Apply was false.
	DetachedCandidates []string

	// DetachedDeleted lists volume names deleted because they were detached and
	// --include-detached --apply was both set.
	DetachedDeleted []string
}

// Prune sweeps the volume store and removes orphaned or detached state
// according to opts.
//
// Dual-source liveness rule (§3.2(c)): a volume is live if any sandbox
// references it from EITHER (1) the volume's meta.json Attachments list OR
// (2) the sandbox store's Sandbox.MountedVolumes field. Sandbox records win on
// conflict — a live sandbox record with MountedVolumes containing the volume
// name always blocks deletion, even if the volume's attachment list is stale.
func (s *VolumeStore) Prune(ctx context.Context, sandboxes SandboxLister, opts PruneOptions) (*PruneResult, error) {
	result := &PruneResult{}

	// Load all sandbox records for the dual-source liveness check.
	sbs, err := sandboxes.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("prune: list sandboxes: %w", err)
	}

	// Build a set of all sandbox IDs that currently exist.
	liveIDs := make(map[string]bool, len(sbs))
	for _, sb := range sbs {
		liveIDs[sb.ID.String()] = true
	}

	// Build recordRefs: volume name → set of sandbox IDs that reference it via
	// the sandbox record's MountedVolumes field (the "records" source).
	recordRefs := make(map[string]map[string]bool)
	for _, sb := range sbs {
		for _, va := range sb.MountedVolumes {
			if recordRefs[va.Name] == nil {
				recordRefs[va.Name] = make(map[string]bool)
			}
			recordRefs[va.Name][sb.ID.String()] = true
		}
	}

	// Walk the volumes root. A missing root means nothing to prune.
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, fmt.Errorf("prune: read volumes dir: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		volDir := filepath.Join(s.root, name)

		metaPath := filepath.Join(volDir, metaFile)
		diskPath := filepath.Join(volDir, diskFile)
		dataPath := filepath.Join(volDir, dataDirName)

		metaOK := isRegularFile(metaPath)
		diskOK := isRegularFile(diskPath)
		dataOK := isDirPath(dataPath)
		backingOK := diskOK || dataOK

		switch {
		case metaOK && !backingOK:
			// Case (a): stub record — meta.json without a backing file.
			// Cause: create crashed between writing meta.json and materialising
			// the backing resource (D-PD-89 ordering).
			if opts.Apply {
				if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
					return nil, fmt.Errorf("prune: remove stub meta %s: %w", name, err)
				}
				_ = os.Remove(volDir) // best-effort: may fail if dir is non-empty
			}
			result.StubsDeleted = append(result.StubsDeleted, name)

		case !metaOK && backingOK:
			// Case (b): orphaned backing file — backing resource without meta.json.
			// Cause: filesystem inconsistency or an interrupted delete.
			var orphans []string
			if diskOK {
				orphans = append(orphans, diskPath)
			}
			if dataOK {
				orphans = append(orphans, dataPath)
			}
			if opts.Apply {
				for _, p := range orphans {
					if err := os.RemoveAll(p); err != nil {
						return nil, fmt.Errorf("prune: remove orphaned backing %s: %w", p, err)
					}
				}
				_ = os.Remove(volDir)
			}
			result.OrphanedFilesDeleted = append(result.OrphanedFilesDeleted, orphans...)

		case metaOK && backingOK:
			// Case (c): volume record is intact — check liveness via both sources.
			rec, err := s.readRecord(name)
			if err != nil {
				// Unreadable meta.json — skip; do not touch unknown state.
				continue
			}

			if isLive(rec, liveIDs, recordRefs) {
				continue // at least one live sandbox references this volume
			}

			// Volume is detached.
			if opts.IncludeDetached && opts.Apply {
				if err := os.RemoveAll(volDir); err != nil {
					return nil, fmt.Errorf("prune: remove detached volume %s: %w", name, err)
				}
				result.DetachedDeleted = append(result.DetachedDeleted, name)
			} else {
				result.DetachedCandidates = append(result.DetachedCandidates, name)
			}
		}
	}

	return result, nil
}

// isLive returns true if any live sandbox references rec from either source:
//  1. The sandbox record's MountedVolumes field (records source — wins on conflict).
//  2. The volume's meta.json Attachments list (meta source).
func isLive(rec *VolumeRecord, liveIDs map[string]bool, recordRefs map[string]map[string]bool) bool {
	// Source 1: sandbox records. A live sandbox record with MountedVolumes
	// containing this volume always wins, regardless of meta.json state.
	for sandboxID := range recordRefs[rec.Name] {
		if liveIDs[sandboxID] {
			return true
		}
	}

	// Source 2: meta.json Attachments. A listed sandbox that still exists
	// makes the volume live.
	for _, att := range rec.Attachments {
		if liveIDs[att.SandboxID] {
			return true
		}
	}

	return false
}

func isRegularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func isDirPath(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
