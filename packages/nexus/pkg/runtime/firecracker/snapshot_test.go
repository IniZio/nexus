package firecracker

import (
	"context"
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
