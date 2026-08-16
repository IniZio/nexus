package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// intentFileSyncer is the seam used to fsync the intent file after writing.
// It is a package-level variable so tests can inject a recording spy to verify
// the sync call without relying on filesystem inspection (which would succeed
// from page cache regardless of whether Sync was called).
// Production code must never replace this outside of tests.
var intentFileSyncer = func(f *os.File) error { return f.Sync() }

// intentDirSyncer is the seam used to fsync the directory containing the
// intent file after the file is closed. Syncing only the file guarantees data
// durability but NOT directory-entry durability: after a power loss the inode
// may survive while the directory entry does not, making the file invisible to
// the reaper's directory scan. Both syncs together close this gap.
// Production code must never replace this outside of tests.
var intentDirSyncer = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	_ = d.Close()
	return syncErr
}

// createIntent is the durable marker written to <diskDir>/<id>.create-intent.json
// BEFORE any host resource is materialized by CreateAndBoot. Its purpose is to
// make the disk resources visible to the R1 reaper if the creating process dies
// between resource materialisation and the durable store.Create call.
//
// The reaper identifies orphaned intents by checking whether a corresponding
// sandbox record exists: an intent without a record means the create did not
// complete, and the listed disk paths are safe to reclaim.
//
// On a clean exit (error or success) CreateAndBoot removes the intent file in
// its deferred cleanup, so intents only survive after unclean termination.
type createIntent struct {
	// ID is the sandbox ULID. Stored as a string so the file is self-describing.
	ID string `json:"id"`

	// DiskCopyPath is the absolute path of the per-sandbox CoW disk copy
	// (<diskDir>/<id>.raw). Empty when the sandbox was created with --rootfs
	// (RootfsPath mode) and therefore has no per-sandbox disk copy.
	DiskCopyPath string `json:"disk_copy_path,omitempty"`

	// WorkspaceDiskPath is the absolute path of the workspace ext4 disk
	// (<diskDir>/<id>-workspace.ext4). Empty when no Workspace was specified.
	WorkspaceDiskPath string `json:"workspace_disk_path,omitempty"`
}

// writeCreateIntent writes a create-intent file for id into diskDir durably.
// It creates diskDir if it does not exist. Returns the path of the written
// intent file.
//
// Durability contract: the function performs a write→Sync→close→dir-Sync
// sequence before returning. Both syncs are required for true power-loss
// durability:
//
//   - f.Sync() makes the file data durable (survives ext4 journal replay).
//   - dir.Sync() makes the directory entry durable so the reaper can discover
//     the file by scanning diskDir. Without the directory sync the inode may
//     survive a power loss while the directory entry does not, leaving the
//     reaper unable to find the intent and the orphaned disk invisible.
//
// The intent must be fully durable before cowExt4 materialises the .raw disk,
// or the failure window this function is designed to close simply moves.
// Callers (create.go) invoke writeCreateIntent before cowExt4; together the
// write+sync sequence inside this function and that call-site ordering make the
// contract hold.
//
// Residual limit: fsync guarantees are only as strong as the storage hardware
// and driver stack honour them. True power-loss behaviour is not tested here;
// see docs/site/operations/resource-lifecycle.md for the full durability contract
// and its unverified residuals.
//
// diskCopyPath and workspaceDiskPath are the planned disk paths; either may be
// empty if the corresponding resource will not be created.
func writeCreateIntent(diskDir string, id domain.SandboxID, diskCopyPath, workspaceDiskPath string) (string, error) {
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		return "", fmt.Errorf("create intent: mkdir %s: %w", diskDir, err)
	}
	intentPath := filepath.Join(diskDir, id.String()+".create-intent.json")
	data, err := json.Marshal(createIntent{
		ID:                id.String(),
		DiskCopyPath:      diskCopyPath,
		WorkspaceDiskPath: workspaceDiskPath,
	})
	if err != nil {
		return "", fmt.Errorf("create intent: marshal: %w", err)
	}

	// Open explicitly (not os.WriteFile) so we can Sync before Close.
	f, err := os.OpenFile(intentPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create intent: open %s: %w", intentPath, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("create intent: write %s: %w", intentPath, err)
	}
	// Sync file data before closing. intentFileSyncer is a package-level seam
	// so tests can assert this call without relying on page-cache reads.
	if err := intentFileSyncer(f); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("create intent: sync %s: %w", intentPath, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("create intent: close %s: %w", intentPath, err)
	}
	// Sync the directory entry. intentDirSyncer is a seam for the same reason.
	if err := intentDirSyncer(diskDir); err != nil {
		return "", fmt.Errorf("create intent: sync dir %s: %w", diskDir, err)
	}
	return intentPath, nil
}

// readCreateIntent reads and parses the intent file at intentPath.
// Returns ErrNotExist (via os package) if the file is absent.
func readCreateIntent(intentPath string) (createIntent, error) {
	data, err := os.ReadFile(intentPath)
	if err != nil {
		return createIntent{}, err
	}
	var ci createIntent
	if err := json.Unmarshal(data, &ci); err != nil {
		return createIntent{}, fmt.Errorf("create intent: parse %s: %w", intentPath, err)
	}
	return ci, nil
}

// IntentPath returns the canonical path of the create-intent file for id in diskDir.
// Exported so the R1 reaper can scan diskDir for orphaned intents without re-deriving
// the naming convention.
func IntentPath(diskDir string, id domain.SandboxID) string {
	return filepath.Join(diskDir, id.String()+".create-intent.json")
}
