package firecracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type mockAPIClient struct {
	putErr error
}

func (m *mockAPIClient) put(ctx context.Context, path string, body any) error {
	return m.putErr
}

func TestManagerSpawnConfiguresAndStartsVM(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	spec := SpawnSpec{
		WorkspaceID: "ws-test-1",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   1024,
		VCPUs:       1,
	}
	
	inst, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	
	if inst.WorkspaceID != spec.WorkspaceID {
		t.Errorf("expected workspace ID %q, got %q", spec.WorkspaceID, inst.WorkspaceID)
	}
	
	if inst.APISocket == "" {
		t.Error("expected API socket path to be set")
	}
	
	if inst.VSockPath == "" {
		t.Error("expected vsock path to be set")
	}
	
	if inst.CID == 0 {
		t.Error("expected CID to be set")
	}
	
	if inst.Process == nil {
		t.Error("expected process to be set")
	}
	
	if _, err := os.Stat(inst.APISocket); os.IsNotExist(err) {
		t.Errorf("API socket file does not exist: %s", inst.APISocket)
	}
}

func TestManagerSpawnBinaryNotFound(t *testing.T) {
	cfg := testManagerConfig(t)
	cfg.FirecrackerBin = "/nonexistent/firecracker"
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}
	
	ctx := context.Background()
	spec := SpawnSpec{
		WorkspaceID: "ws-notfound",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   1024,
		VCPUs:       1,
	}
	
	start := time.Now()
	_, err := mgr.Spawn(ctx, spec)
	elapsed := time.Since(start)
	
	if err == nil {
		t.Fatal("expected error")
	}
	
	if elapsed > 500*time.Millisecond {
		t.Errorf("spawn took too long (%v), should fail fast when binary not found", elapsed)
	}
}

func TestManagerSpawnDuplicateWorkspaceID(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}
	
	ctx := context.Background()
	spec := SpawnSpec{
		WorkspaceID: "ws-dup",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}
	
	_, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("first spawn failed: %v", err)
	}
	
	_, err = mgr.Spawn(ctx, spec)
	if err == nil {
		t.Fatal("expected error for duplicate workspace ID")
	}
	
	if err.Error() != "workspace already exists: ws-dup" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManagerStop(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	spec := SpawnSpec{
		WorkspaceID: "ws-stop",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}
	
	inst, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	
	err = mgr.Stop(ctx, inst.WorkspaceID)
	if err != nil {
		t.Errorf("stop failed: %v", err)
	}
	
	err = mgr.Stop(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent workspace")
	}
}

func TestManagerGet(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{}
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	spec := SpawnSpec{
		WorkspaceID: "ws-get",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}
	
	_, err := mgr.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	
	inst, err := mgr.Get(spec.WorkspaceID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	
	if inst.WorkspaceID != spec.WorkspaceID {
		t.Errorf("expected workspace ID %q, got %q", spec.WorkspaceID, inst.WorkspaceID)
	}
	
	_, err = mgr.Get("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent workspace")
	}
}

func TestManagerSpawnAPIError(t *testing.T) {
	cfg := testManagerConfig(t)
	mgr := newManager(cfg)
	mgr.apiClientFactory = func(sockPath string) apiClientInterface {
		return &mockAPIClient{putErr: errors.New("api error")}
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	spec := SpawnSpec{
		WorkspaceID: "ws-api-error",
		ProjectRoot: t.TempDir(),
		MemoryMiB:   512,
		VCPUs:       1,
	}
	
	_, err := mgr.Spawn(ctx, spec)
	if err == nil {
		t.Fatal("expected error from API failure")
	}
	
	if !errors.Is(err, errors.New("api error")) && err.Error() != "failed to configure machine: api error" {
		t.Errorf("expected API error, got: %v", err)
	}
}

func testManagerConfig(t *testing.T) ManagerConfig {
	tmpDir := t.TempDir()
	kernelPath := filepath.Join(tmpDir, "vmlinux")
	rootfsPath := filepath.Join(tmpDir, "rootfs.ext4")
	fakeFcPath := filepath.Join(tmpDir, "fake-firecracker")
	
	if err := os.WriteFile(kernelPath, []byte("fake kernel"), 0644); err != nil {
		t.Fatalf("failed to create fake kernel: %v", err)
	}
	
	if err := os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644); err != nil {
		t.Fatalf("failed to create fake rootfs: %v", err)
	}
	
	fakeFcScript := `#!/bin/bash
# Fake firecracker that creates the API socket and responds to requests
API_SOCK=""
for ((i=1; i<=$#; i++)); do
    if [ "${!i}" = "--api-sock" ]; then
        j=$((i+1))
        API_SOCK="${!j}"
    fi
done

if [ -n "$API_SOCK" ]; then
    # Create socket file to signal startup
    touch "$API_SOCK"
    
    # Keep process running
    while true; do
        sleep 1
    done
fi
`
	if err := os.WriteFile(fakeFcPath, []byte(fakeFcScript), 0755); err != nil {
		t.Fatalf("failed to create fake firecracker: %v", err)
	}
	
	return ManagerConfig{
		FirecrackerBin: fakeFcPath,
		KernelPath:     kernelPath,
		RootFSPath:     rootfsPath,
		WorkDirRoot:    tmpDir,
	}
}
