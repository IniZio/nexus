package cloudhypervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// errNoDisks is returned by findRootDiskPath when config.json has no "disks"
// field or the disks array is empty. This is expected for initramfs-boot VMs;
// callers should skip disk isolation in that case.
var errNoDisks = errors.New("no disks configured in config.json")

// reflinkCopy copies src to dst using cp --reflink=auto.
//
// On btrfs and XFS the copy is a free reflink clone (no I/O proportional to
// file size). On other filesystems cp falls back to a regular copy silently.
// dst must not exist before the call.
func reflinkCopy(src, dst string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("cp", "--reflink=auto", src, dst)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cp --reflink=auto %s → %s: %w: %s", src, dst, err, stderr.String())
	}
	return nil
}

// diskEntryPath extracts the "path" string from a raw disk-map entry as parsed
// from config.json.
func diskEntryPath(d map[string]json.RawMessage) (string, error) {
	pathRaw, ok := d["path"]
	if !ok {
		return "", fmt.Errorf("disk entry has no \"path\" field")
	}
	var p string
	if err := json.Unmarshal(pathRaw, &p); err != nil {
		return "", fmt.Errorf("decode disk path: %w", err)
	}
	return p, nil
}

// findRootDiskPath parses a CH config.json blob and returns the path of the
// root (boot) disk.
//
// Identification strategy:
//  1. Exact basename match: the first disk whose basename is
//     parentID.String()+".raw" is the root disk.
//  2. Single-disk fallback: if no basename match and there is exactly one
//     disk entry, that entry is returned — it must be the root disk.
//  3. If neither condition holds, an error is returned.
//
// Returns errNoDisks when config.json has no "disks" field or the array is
// empty — the caller should skip disk isolation for initramfs-boot VMs.
func findRootDiskPath(configJSON []byte, parentID domain.SandboxID) (string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &top); err != nil {
		return "", fmt.Errorf("unmarshal config.json: %w", err)
	}

	disksRaw, ok := top["disks"]
	if !ok {
		return "", errNoDisks
	}

	var disks []map[string]json.RawMessage
	if err := json.Unmarshal(disksRaw, &disks); err != nil {
		return "", fmt.Errorf("unmarshal disks array: %w", err)
	}
	if len(disks) == 0 {
		return "", errNoDisks
	}

	targetBase := parentID.String() + ".raw"

	// First pass: exact basename match.
	for _, d := range disks {
		p, err := diskEntryPath(d)
		if err != nil {
			continue
		}
		if filepath.Base(p) == targetBase {
			return p, nil
		}
	}

	// Fallback: exactly one disk present — it must be the root disk.
	if len(disks) == 1 {
		p, err := diskEntryPath(disks[0])
		if err != nil {
			return "", fmt.Errorf("decode single disk path: %w", err)
		}
		return p, nil
	}

	return "", fmt.Errorf(
		"config.json: cannot identify root disk (expected basename %q, %d entries)",
		targetBase, len(disks),
	)
}

// rewriteConfigDiskPath returns a rewritten copy of configJSON in which the
// first disk entry whose path basename matches filepath.Base(oldDiskPath) has
// its "path" replaced by newDiskPath. All other top-level fields and all other
// disk fields are preserved verbatim via a map[string]json.RawMessage
// round-trip — unknown fields are never dropped.
//
// Rewrite strategy: match by basename so that the check is independent of the
// directory layout at snapshot time vs. restore time.
//
// Returns an error if no matching disk entry is found.
func rewriteConfigDiskPath(configJSON []byte, oldDiskPath, newDiskPath string) ([]byte, error) {
	// Preserve the top-level config byte-for-byte except for "disks".
	var top map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &top); err != nil {
		return nil, fmt.Errorf("unmarshal config.json: %w", err)
	}

	disksRaw, ok := top["disks"]
	if !ok {
		return nil, fmt.Errorf("config.json has no \"disks\" field")
	}

	// Preserve each disk entry byte-for-byte except for the matched "path".
	var disks []map[string]json.RawMessage
	if err := json.Unmarshal(disksRaw, &disks); err != nil {
		return nil, fmt.Errorf("unmarshal disks array: %w", err)
	}

	oldBase := filepath.Base(oldDiskPath)
	rewritten := false
	for i, disk := range disks {
		p, err := diskEntryPath(disk)
		if err != nil {
			continue
		}
		if filepath.Base(p) == oldBase {
			newPathRaw, merr := json.Marshal(newDiskPath)
			if merr != nil {
				return nil, fmt.Errorf("marshal new disk path: %w", merr)
			}
			disks[i]["path"] = newPathRaw
			rewritten = true
			break // rewrite only the first match; leave sibling disks unchanged
		}
	}
	if !rewritten {
		return nil, fmt.Errorf("config.json: no disk entry with basename %q", oldBase)
	}

	newDisksRaw, err := json.Marshal(disks)
	if err != nil {
		return nil, fmt.Errorf("re-encode disks: %w", err)
	}
	top["disks"] = newDisksRaw

	out, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("re-encode config.json: %w", err)
	}
	return out, nil
}

// prepareChildRestoreDir creates a per-child snapshot restore directory at
// snapDir+"-restore-"+childID.String(). It:
//
//  1. Hardlinks every regular file in snapDir except config.json into the new
//     directory (sharing the large read-only snapshot blobs — e.g. memory.snapshot
//     — without copying them). Falls back to reflinkCopy on cross-device error.
//  2. Writes a new config.json with the requested rewrites applied:
//     - When parentDiskPath is non-empty, rewrites the root-disk path to childDiskPath.
//     - When parentGuestTap is non-empty, rewrites the first net[].tap to childGuestTap.
//     - When parentVsockPath is non-empty, rewrites the vsock.socket path to childVsockPath.
//     At least one rewrite is expected; any combination may be applied together.
//
// Returns (restoreDir, nil) on success. On failure the caller should call
// os.RemoveAll on restoreDir (if non-empty) to clean up partial state.
func prepareChildRestoreDir(
	snapDir string,
	childID domain.SandboxID,
	parentDiskPath, childDiskPath string,
	parentGuestTap, childGuestTap string,
	parentVsockPath, childVsockPath string,
) (restoreDir string, err error) {
	restoreDir = snapDir + "-restore-" + childID.String()
	if err := os.MkdirAll(restoreDir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir restore dir: %w", err)
	}

	// Read config.json and apply the requested rewrites.
	configJSON, err := os.ReadFile(filepath.Join(snapDir, "config.json"))
	if err != nil {
		return restoreDir, fmt.Errorf("read config.json: %w", err)
	}
	rewrittenJSON := configJSON
	if parentDiskPath != "" {
		rewrittenJSON, err = rewriteConfigDiskPath(rewrittenJSON, parentDiskPath, childDiskPath)
		if err != nil {
			return restoreDir, fmt.Errorf("rewrite config.json disk path: %w", err)
		}
	}
	if parentGuestTap != "" {
		rewrittenJSON, err = rewriteConfigNetTap(rewrittenJSON, parentGuestTap, childGuestTap)
		if err != nil {
			return restoreDir, fmt.Errorf("rewrite config.json net tap: %w", err)
		}
	}
	if parentVsockPath != "" {
		rewrittenJSON, err = rewriteConfigVsockPath(rewrittenJSON, parentVsockPath, childVsockPath)
		if err != nil {
			return restoreDir, fmt.Errorf("rewrite config.json vsock path: %w", err)
		}
	}
	newConfigJSON := rewrittenJSON

	// Hardlink all other files (memory.snapshot, state files, …).
	entries, err := os.ReadDir(snapDir)
	if err != nil {
		return restoreDir, fmt.Errorf("readdir snapshot dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "config.json" {
			continue
		}
		src := filepath.Join(snapDir, e.Name())
		dst := filepath.Join(restoreDir, e.Name())
		if linkErr := os.Link(src, dst); linkErr != nil {
			// Cross-device or unsupported — fall back to reflink copy.
			if cpErr := reflinkCopy(src, dst); cpErr != nil {
				return restoreDir, fmt.Errorf("hardlink/copy %q: %w", e.Name(), cpErr)
			}
		}
	}

	// Write the rewritten config.json last (so the dir is consistent on failure).
	if err := os.WriteFile(filepath.Join(restoreDir, "config.json"), newConfigJSON, 0o600); err != nil {
		return restoreDir, fmt.Errorf("write new config.json: %w", err)
	}

	return restoreDir, nil
}
