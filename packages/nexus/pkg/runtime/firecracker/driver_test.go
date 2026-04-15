package firecracker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/credsbundle"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

// fakeManager is a test double for the Manager
type fakeManager struct {
	spawnCalled      bool
	spawnSpec        SpawnSpec
	stopCalled       bool
	stopID           string
	checkpointCalled bool
	checkpointParent string
	checkpointChild  string
	checkpointID     string
	instance         *Instance
	err              error
}

func (f *fakeManager) Spawn(ctx context.Context, spec SpawnSpec) (*Instance, error) {
	f.spawnCalled = true
	f.spawnSpec = spec
	if f.err != nil {
		return nil, f.err
	}
	return f.instance, nil
}

func (f *fakeManager) Stop(ctx context.Context, workspaceID string) error {
	f.stopCalled = true
	f.stopID = workspaceID
	return f.err
}

func (f *fakeManager) Get(workspaceID string) (*Instance, error) {
	if f.instance != nil && f.instance.WorkspaceID == workspaceID {
		return f.instance, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeManager) GrowWorkspace(_ context.Context, _ string, _ int64) error {
	return f.err
}

func (f *fakeManager) CheckpointForkImage(workspaceID string, childWorkspaceID string) (string, error) {
	f.checkpointCalled = true
	f.checkpointParent = workspaceID
	f.checkpointChild = childWorkspaceID
	if f.err != nil {
		return "", f.err
	}
	if strings.TrimSpace(f.checkpointID) == "" {
		f.checkpointID = "snap-1"
	}
	return f.checkpointID, nil
}

func TestFirecrackerDriver_Backend(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))
	if d.Backend() != "firecracker" {
		t.Fatalf("expected backend firecracker, got %q", d.Backend())
	}
}

func TestFirecrackerDriver_CreateRequiresProjectRoot(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))
	err := d.Create(context.Background(), runtime.CreateRequest{WorkspaceID: "ws-1"})
	if err == nil {
		t.Fatal("expected error when project root is empty")
	}
}

func TestFirecrackerDriver_CreateRequiresManager(t *testing.T) {
	d := NewDriver(nil)
	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-1",
		ProjectRoot: "/projects/ws-1",
	})
	if err == nil {
		t.Fatal("expected error when manager is nil")
	}
}

// TestDriverCreateUsesNativeManagerNotVMCTL proves the driver create path
// does not invoke vmctl/lima shell wrapper when using native manager
func TestDriverCreateUsesNativeManagerNotVMCTL(t *testing.T) {
	fakeMgr := &fakeManager{
		instance: &Instance{
			WorkspaceID: "ws-1",
			WorkDir:     "/tmp/ws-1",
			APISocket:   "/tmp/ws-1/firecracker.sock",
			VSockPath:   "/tmp/ws-1/vsock.sock",
			CID:         1000,
		},
	}

	d := NewDriver(nil, WithManager(fakeMgr))

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-1",
		ProjectRoot: "/projects/ws-1",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Verify manager was used
	if !fakeMgr.spawnCalled {
		t.Fatal("expected manager.Spawn to be called")
	}
	if fakeMgr.spawnSpec.WorkspaceID != "ws-1" {
		t.Fatalf("expected workspace ID ws-1, got %s", fakeMgr.spawnSpec.WorkspaceID)
	}
	if fakeMgr.spawnSpec.ProjectRoot != "/projects/ws-1" {
		t.Fatalf("expected project root /projects/ws-1, got %s", fakeMgr.spawnSpec.ProjectRoot)
	}
}

// TestDriverCreateWithMemMiBOption verifies memory configuration is passed to manager
func TestDriverCreateWithMemMiBOption(t *testing.T) {
	fakeMgr := &fakeManager{
		instance: &Instance{
			WorkspaceID: "ws-1",
			WorkDir:     "/tmp/ws-1",
			APISocket:   "/tmp/ws-1/firecracker.sock",
			VSockPath:   "/tmp/ws-1/vsock.sock",
			CID:         1000,
		},
	}

	d := NewDriver(nil, WithManager(fakeMgr))

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-1",
		ProjectRoot: "/projects/ws-1",
		Options:     map[string]string{"mem_mib": "2048"},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if fakeMgr.spawnSpec.MemoryMiB != 2048 {
		t.Fatalf("expected MemoryMiB 2048, got %d", fakeMgr.spawnSpec.MemoryMiB)
	}
}

func TestDriverCreateWithVCPUsOption(t *testing.T) {
	fakeMgr := &fakeManager{
		instance: &Instance{
			WorkspaceID: "ws-1",
			WorkDir:     "/tmp/ws-1",
			APISocket:   "/tmp/ws-1/firecracker.sock",
			VSockPath:   "/tmp/ws-1/vsock.sock",
			CID:         1000,
		},
	}

	d := NewDriver(nil, WithManager(fakeMgr))
	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-1",
		ProjectRoot: "/projects/ws-1",
		Options: map[string]string{
			"vcpus": "3",
		},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if fakeMgr.spawnSpec.VCPUs != 3 {
		t.Fatalf("expected VCPUs 3, got %d", fakeMgr.spawnSpec.VCPUs)
	}
}

func TestDriverCreatePassesLineageSnapshotToSpawnSpec(t *testing.T) {
	fakeMgr := &fakeManager{
		instance: &Instance{
			WorkspaceID: "ws-1",
			WorkDir:     "/tmp/ws-1",
			APISocket:   "/tmp/ws-1/firecracker.sock",
			VSockPath:   "/tmp/ws-1/vsock.sock",
			CID:         1000,
		},
	}
	d := NewDriver(nil, WithManager(fakeMgr))

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-1",
		ProjectRoot: "/projects/ws-1",
		Options: map[string]string{
			"lineage_snapshot_id": "snap-42",
		},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if fakeMgr.spawnSpec.SnapshotID != "snap-42" {
		t.Fatalf("expected spawn snapshot id %q, got %q", "snap-42", fakeMgr.spawnSpec.SnapshotID)
	}
}

// TestDriverCreatePassesSpawnError verifies errors from manager are propagated
func TestDriverCreatePassesSpawnError(t *testing.T) {
	fakeMgr := &fakeManager{
		err: errors.New("spawn failed"),
	}

	d := NewDriver(nil, WithManager(fakeMgr))

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-1",
		ProjectRoot: "/projects/ws-1",
	})
	if err == nil {
		t.Fatal("expected error from manager spawn")
	}
	if err.Error() != "spawn failed" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestDriverStopUsesNativeManager verifies Stop delegates to manager
func TestDriverStopUsesNativeManager(t *testing.T) {
	fakeMgr := &fakeManager{}

	d := NewDriver(nil, WithManager(fakeMgr))

	// First create a workspace entry
	d.mu.Lock()
	d.projectRoots["ws-1"] = "/projects/ws-1"
	d.mu.Unlock()

	err := d.Stop(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	// Verify manager was used
	if !fakeMgr.stopCalled {
		t.Fatal("expected manager.Stop to be called")
	}
	if fakeMgr.stopID != "ws-1" {
		t.Fatalf("expected workspace ID ws-1, got %s", fakeMgr.stopID)
	}
}

func TestFirecrackerDriver_StopRequiresManager(t *testing.T) {
	d := NewDriver(nil)

	d.mu.Lock()
	d.projectRoots["ws-1"] = "/projects/ws-1"
	d.mu.Unlock()

	err := d.Stop(context.Background(), "ws-1")
	if err == nil {
		t.Fatal("expected error when manager is nil")
	}
}

func TestFirecrackerDriver_StartIsNoOp(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))

	err := d.Start(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("start should be no-op, got error: %v", err)
	}
}

func TestFirecrackerDriver_PauseDelegatesToStop(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))

	d.mu.Lock()
	d.projectRoots["ws-1"] = "/projects/ws-1"
	d.mu.Unlock()

	err := d.Pause(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("pause should delegate to stop: %v", err)
	}
	if !fakeMgr.stopCalled {
		t.Fatal("expected manager.Stop to be called by pause")
	}
}

func TestFirecrackerDriver_ResumeCreatesVMFromProjectRoot(t *testing.T) {
	fakeMgr := &fakeManager{
		instance: &Instance{
			WorkspaceID: "ws-1",
			WorkDir:     "/tmp/ws-1",
			APISocket:   "/tmp/ws-1/firecracker.sock",
			VSockPath:   "/tmp/ws-1/vsock.sock",
			CID:         1000,
		},
	}
	d := NewDriver(nil, WithManager(fakeMgr))
	d.mu.Lock()
	d.projectRoots["ws-1"] = "/projects/ws-1"
	d.mu.Unlock()

	err := d.Resume(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if !fakeMgr.spawnCalled {
		t.Fatal("expected manager.Spawn to be called by resume")
	}
	if fakeMgr.spawnSpec.ProjectRoot != "/projects/ws-1" {
		t.Fatalf("expected resume to use saved project root, got %q", fakeMgr.spawnSpec.ProjectRoot)
	}
}

func TestFirecrackerDriver_ForkCopiesParentProjectRoot(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))

	d.mu.Lock()
	d.projectRoots["ws-1"] = "/projects/ws-1"
	d.mu.Unlock()

	if err := d.Fork(context.Background(), "ws-1", "ws-2"); err != nil {
		t.Fatalf("fork failed: %v", err)
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	if got := d.projectRoots["ws-2"]; got != "/projects/ws-1" {
		t.Fatalf("expected child project root to inherit parent root, got %q", got)
	}
}

func TestFirecrackerDriver_CheckpointForkUsesManagerSnapshotter(t *testing.T) {
	fakeMgr := &fakeManager{
		checkpointID: "snap-fork-42",
	}
	d := NewDriver(nil, WithManager(fakeMgr))

	snapshotID, err := d.CheckpointFork(context.Background(), "ws-1", "ws-2")
	if err != nil {
		t.Fatalf("checkpoint fork failed: %v", err)
	}
	if snapshotID != "snap-fork-42" {
		t.Fatalf("expected snapshot id %q, got %q", "snap-fork-42", snapshotID)
	}
	if !fakeMgr.checkpointCalled {
		t.Fatal("expected manager checkpoint to be called")
	}
	if fakeMgr.checkpointParent != "ws-1" || fakeMgr.checkpointChild != "ws-2" {
		t.Fatalf("unexpected checkpoint args: parent=%q child=%q", fakeMgr.checkpointParent, fakeMgr.checkpointChild)
	}
}

func TestFirecrackerDriver_RestoreDelegatesToResume(t *testing.T) {
	fakeMgr := &fakeManager{
		instance: &Instance{
			WorkspaceID: "ws-1",
			WorkDir:     "/tmp/ws-1",
			APISocket:   "/tmp/ws-1/firecracker.sock",
			VSockPath:   "/tmp/ws-1/vsock.sock",
			CID:         1000,
		},
	}
	d := NewDriver(nil, WithManager(fakeMgr))
	d.mu.Lock()
	d.projectRoots["ws-1"] = "/projects/ws-1"
	d.mu.Unlock()

	err := d.Restore(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("restore should delegate to resume: %v", err)
	}
}

func TestFirecrackerDriver_Destroy(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))

	// Setup workspace
	d.mu.Lock()
	d.projectRoots["ws-1"] = "/projects/ws-1"
	d.mu.Unlock()

	err := d.Destroy(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("destroy failed: %v", err)
	}

	// Verify workspace was removed
	d.mu.RLock()
	_, exists := d.projectRoots["ws-1"]
	d.mu.RUnlock()
	if exists {
		t.Fatal("expected workspace to be removed from projectRoots")
	}
}

func TestFirecrackerDriver_DestroyUnknownWorkspaceNoOp(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))
	if err := d.Destroy(context.Background(), "missing"); err != nil {
		t.Fatalf("destroy unknown should be no-op, got %v", err)
	}
}

func TestFirecrackerDriver_DestroyWithoutManager(t *testing.T) {
	d := NewDriver(nil)

	// Setup workspace
	d.mu.Lock()
	d.projectRoots["ws-1"] = "/projects/ws-1"
	d.mu.Unlock()

	// Should not panic or error even without manager
	err := d.Destroy(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("destroy should work without manager: %v", err)
	}
}

func TestBuildHostAuthBundleIncludesRegistryPaths(t *testing.T) {
	home := t.TempDir()

	credFile := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(credFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credFile, []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	encoded, err := credsbundle.BuildFromHome(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encoded == "" {
		t.Fatal("expected non-empty bundle when cred files exist")
	}

	raw, decErr := base64.StdEncoding.DecodeString(encoded)
	if decErr != nil {
		t.Fatalf("bundle is not valid base64: %v", decErr)
	}

	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("bundle is not gzip: %v", err)
	}
	tr := tar.NewReader(gr)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		if strings.HasSuffix(hdr.Name, ".credentials.json") {
			found = true
		}
	}
	if !found {
		t.Fatal("bundle does not contain .credentials.json from registry")
	}
}
