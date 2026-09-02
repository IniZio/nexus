package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// ReapDiskCopy removes all per-sandbox disk resources for id from diskDir:
//   - <id>.raw          — the CoW sandbox disk copy (S-COW)
//   - <id>-workspace.ext4 — the workspace capture disk (gap-3 fix: was leaked
//     by service.Remove because ReapDiskCopy only removed .raw)
//   - <id>.create-intent.json — the create-intent marker, if any
//
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
	// Remove sandbox disk copy (.raw). Idempotent — ErrNotExist is not an error.
	if err := os.Remove(filepath.Join(diskDir, id.String()+".raw")); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Remove workspace disk (-workspace.ext4). Gap-3 fix: service.Remove
	// previously leaked this file because only .raw was reaped.
	if err := os.Remove(filepath.Join(diskDir, id.String()+"-workspace.ext4")); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Remove create-intent marker if one survives from a partial create (it
	// normally does not reach here — the intent is removed by CreateAndBoot's
	// deferred cleanup — but a reap call after a crash may encounter one).
	if err := os.Remove(filepath.Join(diskDir, id.String()+".create-intent.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Remove scratch disk (-scratch.ext4). Workspace sandboxes get a per-sandbox
	// sparse image that the guest reformats as /tmp on every boot (D-SD-01,
	// RES-R-005/006). Non-workspace sandboxes never create it; os.Remove on a
	// missing path is a no-op via the IsNotExist guard below.
	if err := os.Remove(filepath.Join(diskDir, id.String()+"-scratch.ext4")); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Remove the A-MOUNT agent-config staging dir (<id>-agentcfg-lower). The CLI
	// stages a curated, secret-free copy of the user's agent config here before
	// boot and mounts it RO at /run/nexus3/agentcfg-lower; it is ID-keyed under
	// the disk dir precisely so this reap reclaims it. RemoveAll (not Remove):
	// the staging path is a directory tree. A missing dir is not an error.
	if err := os.RemoveAll(filepath.Join(diskDir, id.String()+"-agentcfg-lower")); err != nil {
		return err
	}
	return nil
}

// ReapShadowDisks removes all shadow disk images owned by sandboxHandle from
// diskDir. Shadow disks follow the B1 naming scheme:
//
//	<safeHandle>.shadow.<safeDirName>.ext4
//
// where safeHandle = sandboxHandle with "/" replaced by "_". A glob over
// <safeHandle>.shadow.*.ext4 removes exactly the shadow disks for this handle
// without touching those of any other sandbox.
//
// Idempotent: missing files are not errors, matching the semantics of
// ReapDiskCopy. Called immediately after ReapDiskCopy in Service.Remove so
// that both ULID-keyed and handle-keyed disk resources share one reclamation
// contract.
//
// If diskDir is empty the default disk directory is resolved via defaultDiskDir,
// matching the behaviour of ReapDiskCopy.
func ReapShadowDisks(diskDir, sandboxHandle string) error {
	if diskDir == "" {
		if d, err := defaultDiskDir(); err == nil {
			diskDir = d
		}
		if diskDir == "" {
			return nil
		}
	}
	safeHandle := strings.ReplaceAll(sandboxHandle, "/", "_")
	pattern := filepath.Join(diskDir, safeHandle+".shadow.*.ext4")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		// Glob returns an error only for a syntactically malformed pattern;
		// treat it as nothing to reap rather than propagating.
		return nil
	}
	for _, p := range matches {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("reap shadow disks %q: remove %s: %w", sandboxHandle, filepath.Base(p), err)
		}
	}
	return nil
}
