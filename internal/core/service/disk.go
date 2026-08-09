package service

import (
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// ReapDiskCopy removes the per-sandbox ext4 disk copy at <diskDir>/<id>.raw.
// If diskDir is empty, the default disk directory is resolved via
// defaultDiskDir. Idempotent: a missing file is not an error, mirroring the
// semantics of Service.Remove.
func ReapDiskCopy(diskDir string, id domain.SandboxID) error {
	if diskDir == "" {
		if d, err := defaultDiskDir(); err == nil {
			diskDir = d
		}
		// If defaultDiskDir fails, there is nothing to reap — the record is
		// already deleted and the caller sees a successful remove. A future
		// gc/prune pass can clean up any orphan in ~/.local/state/nexus3/disks.
	}
	if diskDir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(diskDir, id.String()+".raw"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
