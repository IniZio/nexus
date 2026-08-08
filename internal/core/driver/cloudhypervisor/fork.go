package cloudhypervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// vmRestoreRequest is the JSON body for PUT /api/v1/vm.restore.
type vmRestoreRequest struct {
	SourceURL string `json:"source_url"`
	Prefault  bool   `json:"prefault"`
}

// VMRestore sends PUT /api/v1/vm.restore, restoring VM state from sourceURL.
// sourceURL must be a "file://" URL pointing to a directory previously
// written by VMSnapshot. The call creates the VM and brings it to Running
// state in one step — no separate vm.create or vm.boot is needed.
//
// Prefault=true eagerly faults all guest memory pages into host RAM (eager
// restore). UFFD lazy-restore is explicitly deferred and not implemented here.
//
// Returns nil on 204 No Content.
func (c *client) VMRestore(ctx context.Context, sourceURL string) error {
	resp, err := c.do(ctx, http.MethodPut, "/vm.restore", vmRestoreRequest{
		SourceURL: sourceURL,
		Prefault:  true, // eager restore; UFFD lazy-restore is deferred
	})
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.restore: %w", err)
	}
	drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cloudhypervisor: vm.restore: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// readSnapshotManifest reads and unmarshals the snapshotManifest payload that
// TakeSnapshot wrote to the artifact Store.
//
// The payload file lives at <SnapshotDir>/<snapID>.payload following the
// Store's documented naming convention (artifact/store.go).
// Store.Read(snap.ID) must succeed before calling this — it confirms the
// commit marker is present and the payload length matches snap.Size, so the
// file is known to be intact. readSnapshotManifest then loads the raw bytes
// and decodes the JSON manifest.
func (d *CHDriver) readSnapshotManifest(snap artifact.Snapshot) (snapshotManifest, error) {
	payloadPath := filepath.Join(d.cfg.SnapshotDir, string(snap.ID)+".payload")
	b, err := os.ReadFile(payloadPath)
	if err != nil {
		return snapshotManifest{}, fmt.Errorf("read payload %s: %w", snap.ID, err)
	}
	var m snapshotManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return snapshotManifest{}, fmt.Errorf("unmarshal manifest %s: %w", snap.ID, err)
	}
	return m, nil
}

// verifyManifest checks that every file listed in m exists on disk with the
// recorded byte size. It returns an error on the first mismatch, providing a
// concrete integrity check before issuing vm.restore.
//
// This check detects:
//   - Files deleted after TakeSnapshot committed.
//   - Files truncated or partially overwritten.
//   - Manifest corruption (wrong size field → immediate mismatch).
//
// Note: verifyManifest checks existence and size, not content checksums. A
// bit-flip inside a file would pass this check. Checksum verification would
// require reading every byte of potentially large memory snapshot files and is
// out of scope for this implementation.
func verifyManifest(m snapshotManifest) error {
	for _, entry := range m.Files {
		full := filepath.Join(m.Dir, entry.Path)
		info, err := os.Stat(full)
		if err != nil {
			return fmt.Errorf("file %q: %w", entry.Path, err)
		}
		if info.Size() != entry.Size {
			return fmt.Errorf("file %q: size %d on disk, manifest recorded %d",
				entry.Path, info.Size(), entry.Size)
		}
	}
	return nil
}

// ForkFrom spawns one child VM per entry in childIDs, each restored from snap.
//
// # Pre-restore integrity check
//
// Before any restore, ForkFrom:
//  1. Reads the artifact.Store record to confirm the commit marker is present
//     (Store.Read rejects absent or size-mismatched commits).
//  2. Reads and decodes the snapshotManifest from the Store payload file.
//  3. Verifies each file in the manifest exists on disk at the recorded size
//     via verifyManifest — confirming the CH snapshot files are intact.
//
// Only after the manifest passes verification does ForkFrom proceed to spawn
// child VMs. The sourceURL is taken from manifest.Dir (the directory TakeSnapshot
// wrote), not recomputed independently — so the Store record is the canonical
// source of truth for the snapshot location.
//
// # Sequence (per child)
//
//  1. Spawn a fresh cloud-hypervisor VMM process bound to the child's socket.
//  2. Issue PUT /api/v1/vm.restore with source_url = "file://<manifest.Dir>".
//     This creates the VM and starts it from the snapshot in one API call
//     (equivalent to vm.create + vm.boot but from a snapshot, not a kernel).
//  3. Mint a fresh instance ID and persist the child's process handle.
//
// # Notes
//
//   - UFFD lazy-restore is deferred; eager restore (prefault=true) is used.
//   - The parent sandbox (snap.SandboxID) is not modified; fork is pure
//     child-creation (spec edge 5: ∅→running, parent unaffected).
//   - vm.restore uses ctx directly (not callCtx) because restoring large VMs
//     with prefault=true can take O(seconds) — longer than CallTimeout.
//   - On the first child failure the function returns the IDs already filled
//     (empty string for each child that was not yet attempted) plus the error.
//
// Implements driver.Forker.
func (d *CHDriver) ForkFrom(ctx context.Context, snap artifact.Snapshot, childIDs []domain.SandboxID) ([]string, error) {
	// Step 1: confirm the commit marker is present and the payload length is intact.
	if _, err := d.snapshotStore.Read(snap.ID); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: fork: snapshot record: %w", err)
	}

	// Step 2: read and decode the manifest that TakeSnapshot stored as payload.
	manifest, err := d.readSnapshotManifest(snap)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: fork: %w", err)
	}

	// Step 3: verify every snapshot file is present at the expected size.
	if err := verifyManifest(manifest); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: fork: manifest verify: %w", err)
	}

	sourceURL := "file://" + manifest.Dir

	instanceIDs := make([]string, len(childIDs))
	for i, childID := range childIDs {
		iid, err := d.spawnChildFromSnapshot(ctx, childID, sourceURL)
		if err != nil {
			// Return already-populated IDs and the first error; caller can
			// retry or clean up the unseen children.
			return instanceIDs, fmt.Errorf("cloudhypervisor: fork child %s: %w", childID, err)
		}
		instanceIDs[i] = iid
	}
	return instanceIDs, nil
}

// spawnChildFromSnapshot spawns a new VMM for childID and restores it from
// sourceURL. On success it records the child's process handle and instance ID.
func (d *CHDriver) spawnChildFromSnapshot(ctx context.Context, childID domain.SandboxID, sourceURL string) (string, error) {
	socketPath := d.socketPath(childID)

	// Spawn a fresh VMM process for the child VM.
	proc, err := spawnVMM(ctx, d.cfg, socketPath)
	if err != nil {
		return "", fmt.Errorf("spawn VMM: %w", err)
	}

	// Restore from snapshot. Uses caller ctx (not callCtx): prefault=true
	// reads all guest memory from disk, which can take O(seconds) for large VMs.
	if err := newClient(socketPath).VMRestore(ctx, sourceURL); err != nil {
		proc.kill()
		_ = os.Remove(socketPath)
		return "", fmt.Errorf("vm.restore: %w", err)
	}

	// Mint a fresh instance ID for this child.
	iid, err := newInstanceID()
	if err != nil {
		proc.kill()
		_ = os.Remove(socketPath)
		return "", fmt.Errorf("generate instance ID: %w", err)
	}

	d.mu.Lock()
	d.procs[childID] = proc
	d.mu.Unlock()

	// Persist the IID sidecar so Observe can reconstruct InstanceID after a
	// nexus3 restart. Non-fatal: a write failure leaves the IID empty on
	// restart but does not affect the running VM.
	_ = d.writeInstanceID(childID, iid)

	return iid, nil
}
