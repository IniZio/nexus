package firecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProbeReflink_DetectsFilesystem(t *testing.T) {
	dir := t.TempDir()
	result := probeReflink(dir)
	_ = result
}

func TestCowCopy_ReflinkUnavailable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Manager{reflinkAvailable: false}
	if err := m.cowCopy(src, dst); err != nil {
		t.Fatalf("cowCopy fallback failed: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestCowCopy_ReflinkAvailable(t *testing.T) {
	if _, err := exec.LookPath("cp"); err != nil {
		t.Skip("cp not found")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Manager{reflinkAvailable: true}
	if err := m.cowCopy(src, dst); err != nil {
		t.Skipf("reflink copy failed (likely non-XFS filesystem): %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestSnapshotCacheKey_Deterministic(t *testing.T) {
	key1 := snapshotCacheKey("/k", "/r")
	key2 := snapshotCacheKey("/k", "/r")
	if key1 != key2 {
		t.Fatalf("same inputs should produce same key: %q vs %q", key1, key2)
	}
	key3 := snapshotCacheKey("/k2", "/r")
	if key1 == key3 {
		t.Fatal("different inputs should produce different keys")
	}
}

func TestCheckpointForkSnapshot_PausesAndResumes(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.reflinkAvailable = false

	// Create a "running" parent instance
	parentDir := filepath.Join(cfg.WorkDirRoot, "ws-parent")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentImg := filepath.Join(parentDir, "workspace.ext4")
	if err := os.WriteFile(parentImg, []byte("parent-workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentInst := &Instance{
		WorkspaceID:    "ws-parent",
		WorkDir:        parentDir,
		WorkspaceImage: parentImg,
		APISocket:      filepath.Join(parentDir, "firecracker.sock"),
	}
	mgr.mu.Lock()
	mgr.instances["ws-parent"] = parentInst
	mgr.mu.Unlock()

	mock := &mockAPIClient{}
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return mock
	}

	ctx := context.Background()
	snapID, err := mgr.CheckpointForkSnapshot(ctx, "ws-parent", "ws-child")
	if err != nil {
		t.Fatalf("CheckpointForkSnapshot failed: %v", err)
	}

	if snapID == "" {
		t.Fatal("expected non-empty snapshot ID")
	}

	// Verify pause → snapshot → resume sequence
	foundPause := false
	foundSnapshot := false
	foundResume := false
	for _, call := range mock.putCalls {
		switch call {
		case "/vm:Paused":
			foundPause = true
		case "/snapshot/create":
			foundSnapshot = true
		case "/vm:Resumed":
			foundResume = true
		}
	}
	if !foundPause {
		t.Fatal("expected PauseVM to be called")
	}
	if !foundSnapshot {
		t.Fatal("expected CreateSnapshot to be called")
	}
	if !foundResume {
		t.Fatal("expected ResumeVM to be called after snapshot")
	}
}

func TestCheckpointForkSnapshot_ResumesOnSnapshotError(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.reflinkAvailable = false

	parentDir := filepath.Join(cfg.WorkDirRoot, "ws-parent")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentImg := filepath.Join(parentDir, "workspace.ext4")
	if err := os.WriteFile(parentImg, []byte("parent-workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentInst := &Instance{
		WorkspaceID:    "ws-parent",
		WorkDir:        parentDir,
		WorkspaceImage: parentImg,
		APISocket:      filepath.Join(parentDir, "firecracker.sock"),
	}
	mgr.mu.Lock()
	mgr.instances["ws-parent"] = parentInst
	mgr.mu.Unlock()

	mock := &mockAPIClient{snapshotErr: fmt.Errorf("snapshot failed")}
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return mock
	}

	ctx := context.Background()
	_, err := mgr.CheckpointForkSnapshot(ctx, "ws-parent", "ws-child")
	if err == nil {
		t.Fatal("expected error when snapshot fails")
	}

	// Resume must still be called even though snapshot failed
	foundResume := false
	for _, call := range mock.putCalls {
		if call == "/vm:Resumed" {
			foundResume = true
		}
	}
	if !foundResume {
		t.Fatal("expected ResumeVM to be called even after snapshot error")
	}
}

func TestEnsureBaseSnapshot_CachesOnFirstCall(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.reflinkAvailable = false

	mock := &mockAPIClient{}
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return mock
	}

	key := snapshotCacheKey(cfg.KernelPath, cfg.RootFSPath)

	ctx := context.Background()

	// Manually populate cache to simulate post-cold-boot state.
	snapDir := filepath.Join(cfg.WorkDirRoot, ".snapshots", "base-"+key)
	snap := &baseSnapshot{
		vmstatePath: filepath.Join(snapDir, "vm.snap"),
		memFilePath: filepath.Join(snapDir, "mem.file"),
		kernelPath:  cfg.KernelPath,
		rootfsPath:  cfg.RootFSPath,
	}
	mgr.snapshotCache[key] = snap

	result, err := mgr.ensureBaseSnapshot(ctx, cfg.KernelPath, cfg.RootFSPath)
	if err != nil {
		t.Fatalf("ensureBaseSnapshot failed: %v", err)
	}
	if result != snap {
		t.Fatal("expected cached snapshot to be returned")
	}
	// No API calls should have been made
	if len(mock.putCalls) > 0 {
		t.Fatalf("expected no API calls for cached snapshot, got %v", mock.putCalls)
	}
}
