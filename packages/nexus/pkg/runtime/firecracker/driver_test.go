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

	"github.com/inizio/nexus/packages/nexus/pkg/agentprofile"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

// fakeManager is a test double for the Manager
type fakeManager struct {
	spawnCalled bool
	spawnSpec   SpawnSpec
	stopCalled  bool
	stopID      string
	instance    *Instance
	err         error
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

func TestFirecrackerDriver_PauseNotSupported(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))

	err := d.Pause(context.Background(), "ws-1")
	if err == nil {
		t.Fatal("expected error - pause not supported")
	}
}

func TestFirecrackerDriver_ResumeNotSupported(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))

	err := d.Resume(context.Background(), "ws-1")
	if err == nil {
		t.Fatal("expected error - resume not supported")
	}
}

func TestFirecrackerDriver_ForkNotSupported(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))

	err := d.Fork(context.Background(), "ws-1", "ws-2")
	if err == nil {
		t.Fatal("expected error - fork not supported")
	}
}

func TestFirecrackerDriver_RestoreNotSupported(t *testing.T) {
	fakeMgr := &fakeManager{}
	d := NewDriver(nil, WithManager(fakeMgr))

	err := d.Restore(context.Background(), "ws-1")
	if err == nil {
		t.Fatal("expected error - restore not supported")
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

func TestBuildGuestCLIBootstrapCommandIncludesAllRegistryPackages(t *testing.T) {
	cmd := buildGuestCLIBootstrapCommand()
	for _, pkg := range agentprofile.AllInstallPkgs() {
		if !strings.Contains(cmd, pkg) {
			t.Fatalf("bootstrap command missing install package %q", pkg)
		}
	}
}

func TestBuildHostAuthBundleIncludesKnownConfigPaths(t *testing.T) {
	home := t.TempDir()
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("HOME") })

	mkdir := func(path string) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	mkdir(filepath.Join(home, ".config", "opencode"))
	mkdir(filepath.Join(home, ".config", "codex"))
	mkdir(filepath.Join(home, ".config", "github-copilot"))
	mkdir(filepath.Join(home, ".local", "share", "opencode"))
	mkdir(filepath.Join(home, ".codex"))
	mkdir(filepath.Join(home, ".claude"))
	if err := os.WriteFile(filepath.Join(home, ".config", "opencode", "session.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write opencode session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "opencode", "opencode.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write opencode config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "share", "opencode", "auth.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write opencode auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "github-copilot", "hosts.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write github-copilot hosts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write codex auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "version.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write codex version: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write claude credentials: %v", err)
	}

	bundle, err := buildHostAuthBundle()
	if err != nil {
		t.Fatalf("buildHostAuthBundle: %v", err)
	}
	if strings.TrimSpace(bundle) == "" {
		t.Fatal("expected non-empty auth bundle")
	}

	raw, err := base64.StdEncoding.DecodeString(bundle)
	if err != nil {
		t.Fatalf("decode bundle: %v", err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	names := make([]string, 0)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("read tar entry: %v", err)
		}
		names = append(names, hdr.Name)
	}

	joined := strings.Join(names, "\n")
	if !strings.Contains(joined, ".config/opencode/opencode.json") {
		t.Fatalf("expected opencode config path in archive, got %q", joined)
	}
	if !strings.Contains(joined, ".local/share/opencode/auth.json") {
		t.Fatalf("expected opencode auth path in archive, got %q", joined)
	}
	if !strings.Contains(joined, ".config/github-copilot/hosts.json") {
		t.Fatalf("expected github-copilot hosts in archive, got %q", joined)
	}
	if !strings.Contains(joined, ".codex/auth.json") {
		t.Fatalf("expected .codex auth in archive, got %q", joined)
	}
	if !strings.Contains(joined, ".codex/version.json") {
		t.Fatalf("expected .codex version in archive, got %q", joined)
	}
	if !strings.Contains(joined, ".claude") {
		t.Fatalf("expected claude path in archive, got %q", joined)
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

	encoded, err := buildHostAuthBundleFromHome(home)
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

func TestBuildGuestCLIBootstrapCommandIncludesRegistryPackages(t *testing.T) {
	cmd := buildGuestCLIBootstrapCommand()
	for _, pkg := range agentprofile.AllInstallPkgs() {
		if !strings.Contains(cmd, pkg) {
			t.Fatalf("bootstrap command missing install package %q", pkg)
		}
	}
}

