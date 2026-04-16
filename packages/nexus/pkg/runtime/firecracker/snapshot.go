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
	"strings"
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

// CheckpointForkSnapshot pauses the parent VM, creates a VM state snapshot plus
// a CoW copy of the workspace image for the child, then resumes the parent.
// Returns a snapshotID that can be used by restoreFromSnapshot to spawn the child.
// ResumeVM is always called, even if snapshot creation fails.
func (m *Manager) CheckpointForkSnapshot(ctx context.Context, workspaceID, childWorkspaceID string) (string, error) {
	m.mu.RLock()
	parent, exists := m.instances[workspaceID]
	m.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("workspace not found: %s", workspaceID)
	}
	if strings.TrimSpace(parent.WorkspaceImage) == "" {
		return "", fmt.Errorf("workspace image missing for %s", workspaceID)
	}

	client := m.apiClientFactory(parent.APISocket)

	if err := client.PauseVM(ctx); err != nil {
		return "", fmt.Errorf("pause parent VM: %w", err)
	}

	forkDirName := "fork-" + workspaceID + "-" + childWorkspaceID
	forkDir := filepath.Join(m.config.WorkDirRoot, ".snapshots", forkDirName)
	if err := os.MkdirAll(forkDir, 0o755); err != nil {
		_ = client.ResumeVM(ctx)
		return "", fmt.Errorf("create fork dir: %w", err)
	}

	vmstatePath := filepath.Join(forkDir, "vm.snap")
	memFilePath := filepath.Join(forkDir, "mem.file")

	snapErr := client.CreateSnapshot(ctx, vmstatePath, memFilePath)

	resumeErr := client.ResumeVM(ctx)
	if snapErr != nil {
		if resumeErr != nil {
			log.Printf("[firecracker] WARNING: resume failed after snapshot error for %s: %v", workspaceID, resumeErr)
		}
		return "", fmt.Errorf("create fork snapshot: %w", snapErr)
	}
	if resumeErr != nil {
		return "", fmt.Errorf("resume parent VM after fork snapshot: %w", resumeErr)
	}

	childImg := filepath.Join(forkDir, "workspace.ext4")
	if err := m.cowCopy(parent.WorkspaceImage, childImg); err != nil {
		return "", fmt.Errorf("cowCopy workspace image for fork: %w", err)
	}

	snapshotID := forkDirName
	return snapshotID, nil
}
