package cloudhypervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
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

// ChildExtraDiskPath returns the host path to use for the child's per-fork copy
// of an extra disk rooted at parentPath. Workspace disks (suffix
// "-workspace.ext4") are renamed to <childID>-workspace.ext4 so that
// ParseSandboxID can recover the owner from the filename and the reaper can
// classify the file as KindDiskWorkspace (D-PD-80(b)). Scratch disks (suffix
// "-scratch.ext4") are likewise renamed to <childID>-scratch.ext4 so that
// ReapDiskCopy and ResourceIndex.List can reclaim and enumerate them by owner
// (SD-AC7 fork path, w4b-fork-scratch-leak). D-SD-01 reformats scratch on
// every guest boot, so the child discards the parent's contents on first boot
// regardless — only the name matters for reclamation. All other extra disks
// keep their basename, prefixed by childID.
//
// Exported so that tests in sibling packages can verify the naming convention
// without exercising a full VM fork.
func ChildExtraDiskPath(childID domain.SandboxID, parentPath string) string {
	dir := filepath.Dir(parentPath)
	if strings.HasSuffix(parentPath, "-workspace.ext4") {
		return filepath.Join(dir, childID.String()+"-workspace.ext4")
	}
	if strings.HasSuffix(parentPath, "-scratch.ext4") {
		return filepath.Join(dir, childID.String()+"-scratch.ext4")
	}
	return filepath.Join(dir, childID.String()+"-"+filepath.Base(parentPath))
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
	// Confirm the commit marker is present and the payload length is intact.
	if _, err := d.snapshotStore.Read(snap.ID); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: fork: snapshot record: %w", err)
	}

	// Read and decode the manifest that TakeSnapshot stored as payload.
	manifest, err := d.readSnapshotManifest(snap)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: fork: %w", err)
	}

	// Derive the snapshot directory from the current store root + snapID rather
	// than trusting the path baked into manifest.Dir at snapshot time. This makes
	// the store root relocatable: if SnapshotDir changed between TakeSnapshot and
	// ForkFrom (e.g. ephemeral→durable migration), the correct path is still found.
	manifest.Dir = d.snapshotDirPath(snap.ID)

	// Verify every snapshot file is present at the expected size.
	if err := verifyManifest(manifest); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: fork: manifest verify: %w", err)
	}

	// Identify the parent's root disk and net TAP from config.json in the
	// snapshot directory.
	//   - For VMs booted via initramfs (no disk), disk isolation is skipped and
	//     parentDiskPath is left empty.
	//   - For vsock-only snapshots (no net device), net isolation is skipped and
	//     parentGuestTap is left empty.
	// Both are safe after eager restore (prefault=true): RAM state is isolated
	// per child regardless.
	configJSONBytes, err := os.ReadFile(filepath.Join(manifest.Dir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: fork: read config.json: %w", err)
	}
	parentDiskPath, err := findRootDiskPath(configJSONBytes, snap.SandboxID)
	if err != nil && !errors.Is(err, errNoDisks) {
		return nil, fmt.Errorf("cloudhypervisor: fork: find root disk: %w", err)
	}
	// errNoDisks is benign: initramfs-only VM, no disk to isolate.
	if errors.Is(err, errNoDisks) {
		parentDiskPath = ""
	}

	parentGuestTap, err := findNetTap(configJSONBytes)
	if err != nil && !errors.Is(err, errNoNet) {
		return nil, fmt.Errorf("cloudhypervisor: fork: find net tap: %w", err)
	}
	// errNoNet is benign: vsock-only snapshot, no net to isolate.
	if errors.Is(err, errNoNet) {
		parentGuestTap = ""
	}

	parentVsockPath, err := findVsockPath(configJSONBytes)
	if err != nil && !errors.Is(err, errNoVsock) {
		return nil, fmt.Errorf("cloudhypervisor: fork: find vsock path: %w", err)
	}
	// errNoVsock is benign: VM has no vsock device.
	if errors.Is(err, errNoVsock) {
		parentVsockPath = ""
	}

	var diskDir string
	if parentDiskPath != "" {
		diskDir = filepath.Dir(parentDiskPath)
	}

	// Collect extra (non-root) disk paths from config.json so they can be
	// isolated per child alongside the root disk (D-PD-54). When
	// parentDiskPath is empty the config has no disks at all (initramfs), so
	// there is nothing extra to collect.
	var parentExtraDiskPaths []string
	if parentDiskPath != "" {
		var collErr error
		parentExtraDiskPaths, collErr = collectExtraDiskPaths(configJSONBytes, parentDiskPath)
		if collErr != nil {
			return nil, fmt.Errorf("cloudhypervisor: fork: collect extra disk paths: %w", collErr)
		}
	}

	instanceIDs := make([]string, len(childIDs))
	for i, childID := range childIDs {
		var childDiskPath string
		if parentDiskPath != "" {
			childDiskPath = filepath.Join(diskDir, childID.String()+".raw")
		}
		// Build per-child disk pairs for all extra disks.
		// Workspace disks are renamed to <childID>-workspace.ext4 so the reaper
		// can classify them by owner (D-PD-80(b)).
		var extraDiskPairs []diskPair
		for _, p := range parentExtraDiskPaths {
			extraDiskPairs = append(extraDiskPairs, diskPair{
				parent: p,
				child:  ChildExtraDiskPath(childID, p),
			})
		}
		iid, err := d.spawnChildFromSnapshot(ctx, childID, manifest.Dir, parentDiskPath, childDiskPath, extraDiskPairs, parentGuestTap, parentVsockPath)
		if err != nil {
			// Return already-populated IDs and the first error; caller can
			// retry or clean up the unseen children.
			return instanceIDs, fmt.Errorf("cloudhypervisor: fork child %s: %w", childID, err)
		}
		instanceIDs[i] = iid
	}

	// Reap transient snapshots after all children are successfully forked.
	// ForkFrom uses eager restore (prefault=true), so children do not page from
	// the snapshot after ForkFrom returns. Reaping here prevents indefinite
	// accumulation in the durable SnapshotDir.
	d.reapTransientSnapshot(snap)

	return instanceIDs, nil
}

// reapTransientSnapshot removes the store record and CH file directory for
// snap when snap.Kind == artifact.KindTransient. Called after a successful
// ForkFrom so that one-shot fork snapshots do not accumulate in the durable
// SnapshotDir. For KindRetained snapshots this is a no-op.
func (d *CHDriver) reapTransientSnapshot(snap artifact.Snapshot) {
	if snap.Kind != artifact.KindTransient {
		return
	}
	_ = d.snapshotStore.Remove(snap.ID)
	_ = os.RemoveAll(d.snapshotDirPath(snap.ID))
}

// spawnChildFromSnapshot spawns a new VMM for childID and restores it from
// snapDir.
//
// Isolation:
//   - Disk: when parentDiskPath and childDiskPath are non-empty, the parent
//     disk is reflink-copied to a child-unique path so siblings share no block
//     device.
//   - Net: when parentGuestTap is non-empty (snapshot from a networked VM), the
//     child runs in an isolated user+network namespace (via StartNetnsRuntime)
//     with a fresh TAP bridge created using tapIfNames(childID). config.json in
//     the per-child restore dir has net[].tap rewritten to the child's guest TAP
//     name, so CH can open it at vm.restore time. The re-exec'd child issues
//     vm.restore itself (restore mode); the parent polls VMInfo until Running.
//   - Vsock-only snapshots (errNoNet): no netns is launched; the parent spawns
//     a plain VMM and calls vm.restore directly (original path, unchanged).
//
// On success it records the child's net or proc handle and instance ID.
// On any failure it cleans up the child disk, restore dir, and netns child.
func (d *CHDriver) spawnChildFromSnapshot(
	ctx context.Context,
	childID domain.SandboxID,
	snapDir, parentDiskPath, childDiskPath string,
	extraDiskPairs []diskPair,
	parentGuestTap string,
	parentVsockPath string,
) (string, error) {
	socketPath := d.socketPath(childID)

	// sourceURL points at the snapshot directory CH will restore from.
	// For disk-bearing or networked VMs this is overwritten with the per-child dir.
	sourceURL := "file://" + snapDir
	var restoreDir string

	// Compute the child's guest TAP name (deterministic from childID).
	// Only non-empty when parentGuestTap is non-empty.
	childGuestTap := ""
	if parentGuestTap != "" {
		childGuestTap, _, _ = tapIfNames(childID)
	}

	// Compute the child's vsock socket path (per-sandbox, derived from childID).
	// Only non-empty when parentVsockPath is non-empty.
	childVsockPath := ""
	if parentVsockPath != "" {
		childVsockPath = d.vsockPath(childID)
	}

	// Collect ALL disk pairs that require per-child isolation: root disk first
	// (if present), then every extra disk (shadow + workspace, D-PD-54).
	// Preserving insertion order guarantees that device indices (/dev/vdb, …)
	// in the child match those in the parent.
	var allDiskPairs []diskPair
	if parentDiskPath != "" && childDiskPath != "" {
		allDiskPairs = append(allDiskPairs, diskPair{parentDiskPath, childDiskPath})
	}
	allDiskPairs = append(allDiskPairs, extraDiskPairs...)

	// Prepare a per-child restore directory when disk, net, or vsock isolation
	// is needed. The directory hardlinks the large snapshot blobs and carries a
	// rewritten config.json with child-specific paths.
	needsChildDir := len(allDiskPairs) > 0 || parentGuestTap != "" || parentVsockPath != ""
	if needsChildDir {
		// Reflink-copy every disk so siblings have independent block
		// devices. On failure, roll back all copies made so far.
		for i, dp := range allDiskPairs {
			if err := reflinkCopy(dp.parent, dp.child); err != nil {
				_ = os.Remove(dp.child) // remove partial destination; dst must not exist per reflinkCopy contract
				for _, done := range allDiskPairs[:i] {
					_ = os.Remove(done.child)
				}
				return "", fmt.Errorf("copy child disk %s: %w", filepath.Base(dp.parent), err)
			}
		}

		// Build the full disk-path rewrite map: parent path → child path
		// for every disk. rewriteAllConfigDiskPaths rewrites ALL entries in one
		// pass so the child config.json references no parent-owned file.
		diskRewrites := make(map[string]string, len(allDiskPairs))
		for _, dp := range allDiskPairs {
			diskRewrites[dp.parent] = dp.child
		}

		// Create the per-child restore dir with all rewrites applied to
		// config.json (all disk paths, net tap if present, vsock path if present).
		var prepErr error
		restoreDir, prepErr = prepareChildRestoreDir(
			snapDir, childID,
			diskRewrites,
			parentGuestTap, childGuestTap,
			parentVsockPath, childVsockPath,
		)
		if prepErr != nil {
			for _, dp := range allDiskPairs {
				_ = os.Remove(dp.child)
			}
			if restoreDir != "" {
				_ = os.RemoveAll(restoreDir)
			}
			return "", fmt.Errorf("prepare restore dir: %w", prepErr)
		}
		sourceURL = "file://" + restoreDir
	}

	// cleanupDisk removes all child disk copies, the restore dir, and vsock
	// socket on failure paths.
	cleanupDisk := func() {
		if restoreDir != "" {
			_ = os.RemoveAll(restoreDir)
		}
		for _, dp := range allDiskPairs {
			_ = os.Remove(dp.child)
		}
		if childVsockPath != "" {
			_ = os.Remove(childVsockPath)
		}
	}

	// ── Netns path (snapshot has a net device) ──────────────────────────────
	// Launch the child VMM inside an isolated user+network namespace with the
	// child's own TAP bridge. The re-exec'd child (RunNetnsChild) detects the
	// NEXUS3_NETNS_RESTORE_URL env var and calls vm.restore before tapPump,
	// so the VM reaches Running inside the netns without any parent API call.
	if parentGuestTap != "" {
		rt, err := StartNetnsRuntime(ctx, d.cfg, childID, socketPath, sourceURL)
		if err != nil {
			cleanupDisk()
			return "", fmt.Errorf("spawn netns child: %w", err)
		}

		// Register netState immediately so clearState → teardownSandboxNet →
		// rt.Stop() reaches the child on any subsequent failure path.
		d.mu.Lock()
		d.nets[childID] = &netState{rt: rt, perimConn: rt.PerimConn}
		d.mu.Unlock()

		// cleanupNetns kills the netns child, removes per-sandbox files, and
		// removes the child disk. It is idempotent via rt.Stop()'s stopOnce.
		cleanupNetns := func() {
			d.clearState(childID) // → teardownSandboxNet → rt.Stop()
			cleanupDisk()
		}

		// Poll VMInfo until the child's vm.restore + vm.resume complete and
		// the VM is Running. The netns child issues vm.restore then vm.resume
		// before starting the tapPump; the parent does not call vm.create or
		// vm.boot.
		//
		// CH state machine for restore:
		//   vm.restore → Paused → vm.resume → Running
		//
		// Poll states:
		//   - err != nil && isAbsent: CH socket not yet bound (child still
		//     running createTapBridge + spawnVMM) — retry.
		//   - (Paused, nil): VMRestore done; vm.resume in progress — retry.
		//   - (Running, nil): vm.resume complete; VM is executing — done.
		startTimeout := d.cfg.StartTimeout
		if startTimeout <= 0 {
			startTimeout = 10 * time.Second
		}
		pollCtx, pollCancel := context.WithTimeout(ctx, startTimeout)
		defer pollCancel()
		c := newClient(socketPath)
		for {
			if err := pollCtx.Err(); err != nil {
				tail := rt.ChildStderr()
				cleanupNetns()
				if tail != "" {
					return "", fmt.Errorf("child restore not complete within %s: %w\nVMM stderr:\n%s",
						startTimeout, err, tail)
				}
				return "", fmt.Errorf("child restore not complete within %s: %w", startTimeout, err)
			}
			state, vmInfoErr := c.VMInfo(pollCtx)
			if vmInfoErr == nil && state == driver.Running {
				break
			}
			select {
			case <-pollCtx.Done():
			case <-time.After(50 * time.Millisecond):
			}
		}

		// Restore dir served its purpose; remove to avoid accumulation.
		if restoreDir != "" {
			_ = os.RemoveAll(restoreDir)
		}

		// Mint a fresh instance ID for this child.
		iid, err := newInstanceID()
		if err != nil {
			d.clearState(childID) // kills netns child; also cleans nets map entry
			for _, dp := range allDiskPairs {
				_ = os.Remove(dp.child)
			}
			return "", fmt.Errorf("generate instance ID: %w", err)
		}

		// Note: d.procs[childID] is NOT set on the netns path (same as Start).
		// Kill ownership is via d.nets[childID].rt → rt.Stop().
		_ = d.writeInstanceID(childID, iid)
		return iid, nil
	}

	// ── Plain VMM path (vsock-only snapshot, no net device) ─────────────────
	// Spawn a fresh VMM process and restore from snapshot. The parent issues
	// vm.restore directly; no netns or TAP setup is needed.
	proc, err := spawnVMM(ctx, d.cfg, socketPath)
	if err != nil {
		cleanupDisk()
		return "", fmt.Errorf("spawn VMM: %w", err)
	}

	// Restore from snapshot. Uses caller ctx (not callCtx): prefault=true
	// reads all guest memory from disk, which can take O(seconds) for large VMs.
	chc := newClient(socketPath)
	if err := chc.VMRestore(ctx, sourceURL); err != nil {
		proc.kill()
		_ = os.Remove(socketPath)
		cleanupDisk()
		return "", fmt.Errorf("vm.restore: %w", err)
	}
	// vm.restore leaves the VM in Paused state; vm.resume brings it to Running.
	if err := chc.VMResume(ctx); err != nil {
		proc.kill()
		_ = os.Remove(socketPath)
		cleanupDisk()
		return "", fmt.Errorf("vm.resume after restore: %w", err)
	}

	// Restore is complete (eager / prefault=true). The restore dir is only
	// needed during vm.restore; remove it now to avoid accumulation.
	if restoreDir != "" {
		_ = os.RemoveAll(restoreDir)
	}

	// Mint a fresh instance ID for this child.
	iid, err := newInstanceID()
	if err != nil {
		proc.kill()
		_ = os.Remove(socketPath)
		for _, dp := range allDiskPairs {
			_ = os.Remove(dp.child)
		}
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
