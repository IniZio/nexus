package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

type baseSnapshot struct {
	vmstatePath string
	memFilePath string
	kernelPath  string
	rootfsPath  string
}

func probeReflink(workDirRoot string) bool {
	tmp1 := filepath.Join(workDirRoot, ".reflink-probe-tmp1")
	tmp2 := filepath.Join(workDirRoot, ".reflink-probe-tmp2")

	if err := os.WriteFile(tmp1, []byte("probe"), 0o644); err != nil {
		log.Printf("[firecracker] reflink probe: failed to write temp file: %v", err)
		return false
	}
	defer os.Remove(tmp1)
	defer os.Remove(tmp2)

	cmd := exec.Command("cp", "--reflink=always", tmp1, tmp2)
	if err := cmd.Run(); err != nil {
		log.Printf("[firecracker] XFS reflink not available on %s; fork will use full copy. Format WorkDirRoot as XFS for better performance.", workDirRoot)
		return false
	}

	log.Printf("[firecracker] XFS reflink available on %s, using CoW fork", workDirRoot)
	return true
}

func (m *Manager) cowCopy(src, dst string) error {
	if m.reflinkAvailable {
		cmd := exec.Command("cp", "--reflink=always", src, dst)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("reflink copy %s → %s: %w: %s", src, dst, err, string(out))
		}
		return nil
	}
	return copyFile(src, dst)
}

// snapshotCacheKey returns a deterministic key for a (kernelPath, rootfsPath) pair.
func snapshotCacheKey(kernelPath, rootfsPath string) string {
	h := sha256.Sum256([]byte(kernelPath + "|" + rootfsPath))
	return hex.EncodeToString(h[:])[:16]
}

// ensureBaseSnapshot returns the cached base snapshot for the given kernel+rootfs pair.
// If no snapshot exists yet in the cache, it creates a placeholder entry. The actual
// VM state files are written when a VM cold-boots (see Spawn). Callers must check
// whether snap.vmstatePath exists on disk before using snapshot restore.
func (m *Manager) ensureBaseSnapshot(ctx context.Context, kernelPath, rootfsPath string) (*baseSnapshot, error) {
	key := snapshotCacheKey(kernelPath, rootfsPath)

	m.snapshotMu.RLock()
	snap, ok := m.snapshotCache[key]
	m.snapshotMu.RUnlock()
	if ok {
		return snap, nil
	}

	snapshotsDir := filepath.Join(m.config.WorkDirRoot, ".snapshots")
	baseDir := filepath.Join(snapshotsDir, "base-"+key)
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create base snapshot dir: %w", err)
	}

	snap = &baseSnapshot{
		vmstatePath: filepath.Join(baseDir, "vm.snap"),
		memFilePath: filepath.Join(baseDir, "mem.file"),
		kernelPath:  kernelPath,
		rootfsPath:  rootfsPath,
	}

	m.snapshotMu.Lock()
	m.snapshotCache[key] = snap
	m.snapshotMu.Unlock()

	return snap, nil
}
