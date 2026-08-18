package volumestore

import (
	"fmt"
	"path/filepath"
	"time"
)

// LockPath returns the advisory lock file path for the named volume.
//
// The file is created once and never renamed or replaced — its inode identity
// is the invariant that makes flock-based cross-process exclusion correct
// (internal/core/store/lock.go:33-38). Callers open it with store.OpenLock
// and acquire store.Lock.Exclusive before any read-modify-write of meta.json.
func (s *VolumeStore) LockPath(name string) string {
	return filepath.Join(s.volDir(name), "lock")
}

// AttachAndPrune records sandboxID in the volume's attachment list and removes
// every ID in prune. This is a low-level write primitive used by the service
// layer's concurrency guard (SD2-6-MOUNT §4.1).
//
// The caller MUST hold the per-volume flock (LockPath) across the full
// check-then-write sequence. Calling without the lock races against other
// concurrent attach/detach callers in other processes.
//
// If sandboxID is already present the append is skipped (idempotent).
// If the volume record does not exist, an error wrapping os.ErrNotExist
// is returned.
func (s *VolumeStore) AttachAndPrune(name, sandboxID string, prune []string) error {
	rec, err := s.readRecord(name)
	if err != nil {
		return fmt.Errorf("volume %s: read for attach-and-prune: %w", name, err)
	}
	pruneSet := make(map[string]struct{}, len(prune))
	for _, id := range prune {
		pruneSet[id] = struct{}{}
	}
	kept := rec.Attachments[:0]
	for _, a := range rec.Attachments {
		if _, rm := pruneSet[a.SandboxID]; !rm {
			kept = append(kept, a)
		}
	}
	rec.Attachments = kept
	found := false
	for _, a := range rec.Attachments {
		if a.SandboxID == sandboxID {
			found = true
			break
		}
	}
	if !found {
		rec.Attachments = append(rec.Attachments, VolumeAttachment{
			SandboxID:  sandboxID,
			AttachedAt: time.Now().UTC(),
		})
	}
	return s.writeRecord(rec)
}
