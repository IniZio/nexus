package cloudhypervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// vmSnapshotRequest is the JSON body for PUT /api/v1/vm.snapshot.
type vmSnapshotRequest struct {
	DestinationURL string `json:"destination_url"`
}

// VMSnapshot sends PUT /api/v1/vm.snapshot, directing Cloud Hypervisor to
// write the VM state to destinationURL. The VM must be paused before calling.
// Returns nil on 204 No Content.
//
// destinationURL must be a "file://" URL pointing to an existing directory.
// CH writes its snapshot files (e.g. memory.snapshot, config.json) into that
// directory and returns 204 only after completing the write.
func (c *client) VMSnapshot(ctx context.Context, destinationURL string) error {
	resp, err := c.do(ctx, http.MethodPut, "/vm.snapshot", vmSnapshotRequest{
		DestinationURL: destinationURL,
	})
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.snapshot: %w", err)
	}
	drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cloudhypervisor: vm.snapshot: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// snapshotDirPath returns the directory CH writes snapshot files to for snapID.
// TakeSnapshot creates this directory and passes it as destination_url.
// ForkFrom reconstructs the same path from snap.ID via the manifest payload.
func (d *CHDriver) snapshotDirPath(snapID artifact.SnapshotID) string {
	return filepath.Join(d.cfg.SnapshotDir, string(snapID))
}

// RemoveSnapshot removes the artifact-store record AND the Cloud Hypervisor
// memory-image directory for snapID. It mirrors reapTransientSnapshot but is
// user-facing (errors are returned rather than swallowed) and applies to any
// SnapshotKind. It is idempotent: a non-existent record or directory is not
// an error.
//
// Implements driver.SnapshotRemover.
func (d *CHDriver) RemoveSnapshot(id artifact.SnapshotID) error {
	if err := d.snapshotStore.Remove(id); err != nil {
		return fmt.Errorf("cloudhypervisor: remove snapshot %s: store: %w", id, err)
	}
	if err := os.RemoveAll(d.snapshotDirPath(id)); err != nil {
		return fmt.Errorf("cloudhypervisor: remove snapshot %s: dir: %w", id, err)
	}
	return nil
}

// newSnapshotID generates a random 32-hex-character SnapshotID.
func newSnapshotID() (artifact.SnapshotID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cloudhypervisor: generate snapshot ID: %w", err)
	}
	return artifact.SnapshotID(hex.EncodeToString(b[:])), nil
}

// snapshotManifestEntry records the relative path and byte size of one file
// in a CH snapshot directory.
type snapshotManifestEntry struct {
	Path string `json:"path"` // relative to Dir
	Size int64  `json:"size"`
}

// snapshotManifest is the JSON payload stored by the artifact.Store for a CH
// snapshot. It records the snapshot directory path and one entry per regular
// file CH wrote. ForkFrom verifies this manifest before restoring, confirming
// that every file is present on disk with the expected size.
type snapshotManifest struct {
	// Dir is informational — the absolute path where TakeSnapshot wrote CH's
	// files at snapshot time. ForkFrom derives the actual directory from the
	// current SnapshotDir + snapID (d.snapshotDirPath) rather than trusting
	// this stored value, making the store root relocatable.
	Dir   string                  `json:"dir"`
	Files []snapshotManifestEntry `json:"files"` // sorted by Path
}

// fsyncSnapDir fsyncs every regular file under snapDir (recursively) and then
// the directories themselves. This ensures CH's snapshot bytes are durably
// written to disk before the artifact commit marker is written.
// On Linux, fsyncing a directory flushes its directory-entry metadata.
func fsyncSnapDir(snapDir string) error {
	if err := filepath.WalkDir(snapDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		syncErr := f.Sync()
		f.Close()
		if syncErr != nil {
			return fmt.Errorf("sync %s: %w", path, syncErr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("fsync files in %s: %w", snapDir, err)
	}

	// Fsync the directory itself to flush directory-entry metadata.
	dir, err := os.Open(snapDir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", snapDir, err)
	}
	syncErr := dir.Sync()
	dir.Close()
	if syncErr != nil {
		return fmt.Errorf("sync dir %s: %w", snapDir, syncErr)
	}
	return nil
}

// buildManifest walks snapDir and returns a snapshotManifest listing every
// regular file and its byte size, sorted by relative path.
func buildManifest(snapDir string) (snapshotManifest, error) {
	var entries []snapshotManifestEntry
	err := filepath.WalkDir(snapDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		rel, err := filepath.Rel(snapDir, path)
		if err != nil {
			return err
		}
		entries = append(entries, snapshotManifestEntry{Path: rel, Size: info.Size()})
		return nil
	})
	if err != nil {
		return snapshotManifest{}, fmt.Errorf("walk %s: %w", snapDir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return snapshotManifest{Dir: snapDir, Files: entries}, nil
}

// TakeSnapshot captures the current state of the VM identified by id.
//
// # Sequence
//
//  1. Pause the VM via PUT /api/v1/vm.pause (CH requires Paused state).
//  2. Issue PUT /api/v1/vm.snapshot with destination_url = "file://<snapDir>".
//     CH writes its snapshot files into snapDir and returns 204 when done.
//  3. Resume the VM via PUT /api/v1/vm.resume (always attempted, even on error).
//  4. Fsync every file in snapDir and the directory itself, ensuring CH's
//     bytes are durably written before the commit marker is written.
//  5. Build a snapshotManifest (file names + sizes), JSON-encode it as the
//     payload, and call Store.Write. The Store fsyncs the manifest payload
//     then writes the commit marker — ensuring the marker exists only after
//     both CH's files and the manifest are durable.
//
// # Integrity
//
// The Store's commit-marker pattern certifies:
//   - If a crash occurs before step 5: commit file absent → torn write → rejected.
//   - If a crash occurs between manifest fsync and commit write: absent commit → rejected.
//   - On success: commit present + payload length matches → ForkFrom reads manifest,
//     verifies each file exists at the recorded size, then restores.
//
// # Timeouts
//
// The vm.snapshot call uses ctx directly (not callCtx) because writing VM
// memory to disk is proportional to VM size — larger than CallTimeout allows.
// Pause and Resume use callCtx (quick state transitions).
//
// Implements driver.Snapshotter.
func (d *CHDriver) TakeSnapshot(ctx context.Context, id domain.SandboxID, kind artifact.SnapshotKind) (artifact.Snapshot, error) {
	snapID, err := newSnapshotID()
	if err != nil {
		return artifact.Snapshot{}, fmt.Errorf("cloudhypervisor: snapshot %s: %w", id, err)
	}
	snapDir := d.snapshotDirPath(snapID)
	if err := os.MkdirAll(snapDir, 0o700); err != nil {
		return artifact.Snapshot{}, fmt.Errorf("cloudhypervisor: snapshot %s: mkdir %q: %w", id, snapDir, err)
	}

	c := newClient(d.socketPath(id))

	// Step 1: pause — CH requires the VM to be in Paused state for vm.snapshot.
	pauseCtx, pauseCancel := d.callCtx(ctx)
	pauseErr := c.VMPause(pauseCtx)
	pauseCancel()
	if pauseErr != nil {
		_ = os.RemoveAll(snapDir)
		return artifact.Snapshot{}, fmt.Errorf("cloudhypervisor: snapshot %s: pause: %w", id, pauseErr)
	}

	// Step 2: snapshot. CH writes its files to snapDir and returns 204 only
	// when done. Use caller's ctx (not callCtx) — large VMs take > CallTimeout.
	snapErr := c.VMSnapshot(ctx, "file://"+snapDir)

	// Step 3: resume — always attempted regardless of snapshot outcome.
	resumeCtx, resumeCancel := d.callCtx(ctx)
	resumeErr := c.VMResume(resumeCtx)
	resumeCancel()

	if snapErr != nil {
		_ = os.RemoveAll(snapDir)
		return artifact.Snapshot{}, fmt.Errorf("cloudhypervisor: snapshot %s: vm.snapshot: %w", id, snapErr)
	}
	if resumeErr != nil {
		// Snapshot files are on disk but VM remains paused. Do not remove
		// snapDir — the data is valid and the caller may resume separately.
		return artifact.Snapshot{}, fmt.Errorf(
			"cloudhypervisor: snapshot %s: snapshot committed but vm.resume failed: %w", id, resumeErr)
	}

	// Step 4: fsync CH's snapshot files before writing the commit marker.
	if err := fsyncSnapDir(snapDir); err != nil {
		_ = os.RemoveAll(snapDir)
		return artifact.Snapshot{}, fmt.Errorf("cloudhypervisor: snapshot %s: fsync: %w", id, err)
	}

	// Step 5: build manifest, encode as JSON, write commit-marker record.
	// The payload (manifest JSON) is what Store.Write fsyncs and guards with
	// its commit-marker pattern. The commit marker is written only after both
	// CH's files (step 4) and the manifest payload are durably on disk.
	manifest, err := buildManifest(snapDir)
	if err != nil {
		_ = os.RemoveAll(snapDir)
		return artifact.Snapshot{}, fmt.Errorf("cloudhypervisor: snapshot %s: build manifest: %w", id, err)
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		_ = os.RemoveAll(snapDir)
		return artifact.Snapshot{}, fmt.Errorf("cloudhypervisor: snapshot %s: marshal manifest: %w", id, err)
	}

	snap := artifact.Snapshot{
		ID:           snapID,
		SandboxID:    id,
		Kind:         kind,
		Size:         int64(len(payload)),
		CommitMarker: "committed",
		CreatedAt:    time.Now(),
	}
	if err := d.snapshotStore.Write(snap, payload); err != nil {
		_ = os.RemoveAll(snapDir)
		return artifact.Snapshot{}, fmt.Errorf("cloudhypervisor: snapshot %s: write artifact record: %w", id, err)
	}

	return snap, nil
}

