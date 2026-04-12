package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/selection"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace/create"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func setupRepoWithWorkspaceConfig(t *testing.T, workspaceConfig string) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	repo := filepath.Join("repos", fmt.Sprintf("%s-repo", strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))))
	if err := os.MkdirAll(filepath.Join(repo, ".nexus"), 0o755); err != nil {
		t.Fatalf("mkdir repo .nexus: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nexus", "workspace.json"), []byte(workspaceConfig), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}

	return "./" + repo
}

func TestHandleWorkspaceCreate(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil || result.Workspace.ID == "" {
		t.Fatalf("expected workspace with id, got %#v", result)
	}
}

func TestHandleWorkspaceCreate_WithFactory(t *testing.T) {
	t.Cleanup(selection.ResetRuntimeSetupRunnerForTest)

	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	// Create workspace config with runtime.required
	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace, got %#v", result)
	}
	if result.Workspace.Backend == "" {
		t.Fatalf("expected backend to be set, got empty string")
	}
}

func TestHandleWorkspaceCreate_ConfigRequiredBackendHonored(t *testing.T) {
	t.Cleanup(selection.ResetRuntimeSetupRunnerForTest)

	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace, got %#v", result)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected backend 'firecracker' from config required, got %q", result.Workspace.Backend)
	}
}

func TestHandleWorkspaceCreate_FactoryWithUnavailableCapability(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: false},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}

	_, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr == nil {
		t.Fatalf("expected rpc error for unavailable capability, got nil")
	}
}

func TestHandleWorkspaceCreate_MissingRuntimeRequiredUsesDefaultLinux(t *testing.T) {
	t.Cleanup(selection.ResetRuntimeSetupRunnerForTest)

	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	// Create workspace config WITHOUT runtime.required
	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("expected success when runtime config is missing, got %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace, got %#v", result)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected backend firecracker from default linux requirement, got %q", result.Workspace.Backend)
	}
}

func TestHandleWorkspaceCreate_MissingRuntimeRequiredDoesNotUseSpecBackend(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.local", Available: true},
	}, map[string]runtime.Driver{
		"local": &mockDriver{backend: "local"},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
			Backend:       "local",
		},
	}

	_, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr == nil {
		t.Fatal("expected rpc error when runtime.required is missing")
	}
}

func TestHandleWorkspaceCreate_MissingRuntimeRequiredDoesNotFallbackToLocal(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.local", Available: true},
	}, map[string]runtime.Driver{
		"local": &mockDriver{backend: "local"},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}

	_, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr == nil {
		t.Fatal("expected rpc error when runtime.required is missing")
	}
}

type mockDriver struct {
	backend  string
	createFn func(ctx context.Context, req runtime.CreateRequest) error
}

func (d *mockDriver) Backend() string { return d.backend }
func (d *mockDriver) Create(ctx context.Context, req runtime.CreateRequest) error {
	if d.createFn != nil {
		return d.createFn(ctx, req)
	}
	return nil
}
func (d *mockDriver) Start(ctx context.Context, workspaceID string) error   { return nil }
func (d *mockDriver) Stop(ctx context.Context, workspaceID string) error    { return nil }
func (d *mockDriver) Restore(ctx context.Context, workspaceID string) error { return nil }
func (d *mockDriver) Pause(ctx context.Context, workspaceID string) error   { return nil }
func (d *mockDriver) Resume(ctx context.Context, workspaceID string) error  { return nil }
func (d *mockDriver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	return nil
}
func (d *mockDriver) Destroy(ctx context.Context, workspaceID string) error { return nil }

func TestHandleWorkspaceOpen_NotFound(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	params := WorkspaceOpenParams{ID: "missing"}

	result, rpcErr := HandleWorkspaceOpen(context.Background(), params, mgr)
	if result != nil {
		t.Fatalf("expected nil result, got %#v", result)
	}
	if rpcErr == nil {
		t.Fatal("expected workspace not found error")
	}
}

func TestHandleWorkspaceListAndRemove(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}

	created, rpcErr := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)
	if rpcErr != nil {
		t.Fatalf("create failed: %+v", rpcErr)
	}

	list, rpcErr := HandleWorkspaceList(context.Background(), WorkspaceListParams{}, mgr)
	if rpcErr != nil {
		t.Fatalf("list failed: %+v", rpcErr)
	}
	if len(list.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(list.Workspaces))
	}

	removeParams := WorkspaceRemoveParams{ID: created.Workspace.ID}
	removed, rpcErr := HandleWorkspaceRemove(context.Background(), removeParams, mgr, nil)
	if rpcErr != nil {
		t.Fatalf("remove failed: %+v", rpcErr)
	}
	if !removed.Removed {
		t.Fatal("expected removed=true")
	}
}

func TestHandleWorkspaceStop(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams := WorkspaceStopParams{ID: created.Workspace.ID}
	result, rpcErr := HandleWorkspaceStop(context.Background(), stopParams, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !result.Stopped {
		t.Fatal("expected stopped=true")
	}
}

func TestHandleWorkspaceStop_NotFound(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	stopParams := WorkspaceStopParams{ID: "missing"}
	_, rpcErr := HandleWorkspaceStop(context.Background(), stopParams, mgr)
	if rpcErr == nil {
		t.Fatal("expected workspace not found error")
	}
}

func TestHandleWorkspaceStart(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	startParams := WorkspaceStartParams{ID: created.Workspace.ID}
	result, rpcErr := HandleWorkspaceStart(context.Background(), startParams, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result.Workspace == nil || result.Workspace.ID != created.Workspace.ID {
		t.Fatalf("expected workspace record for started id, got %+v", result.Workspace)
	}
}

func TestHandleWorkspaceStart_NotFound(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	startParams := WorkspaceStartParams{ID: "missing"}
	_, rpcErr := HandleWorkspaceStart(context.Background(), startParams, mgr)
	if rpcErr == nil {
		t.Fatal("expected workspace not found error")
	}
}

func TestHandleWorkspaceRestore(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams := WorkspaceStopParams{ID: created.Workspace.ID}
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams := WorkspaceRestoreParams{ID: created.Workspace.ID}
	result, rpcErr := HandleWorkspaceRestore(context.Background(), restoreParams, mgr, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !result.Restored {
		t.Fatal("expected restored=true")
	}
	if result.Workspace == nil {
		t.Fatal("expected workspace in result")
	}
}

func TestHandleWorkspaceRestore_NotFound(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	restoreParams := WorkspaceRestoreParams{ID: "missing"}
	_, rpcErr := HandleWorkspaceRestore(context.Background(), restoreParams, mgr, nil)
	if rpcErr == nil {
		t.Fatal("expected workspace not found error")
	}
}

func TestHandleWorkspaceRestore_WithFactory(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	// Create workspace config with runtime.required
	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams := WorkspaceStopParams{ID: created.Workspace.ID}
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams := WorkspaceRestoreParams{ID: created.Workspace.ID}
	result, rpcErr := HandleWorkspaceRestore(context.Background(), restoreParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !result.Restored {
		t.Fatal("expected restored=true")
	}
	if result.Workspace == nil {
		t.Fatal("expected workspace in result")
	}
	if result.Workspace.Backend == "" {
		t.Fatal("expected backend to be set when factory is provided")
	}
}

func TestHandleWorkspaceRestore_WithFactory_PersistsBackendSelection(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	// Create workspace config with runtime.required
	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams := WorkspaceStopParams{ID: created.Workspace.ID}
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams := WorkspaceRestoreParams{ID: created.Workspace.ID}
	result, rpcErr := HandleWorkspaceRestore(context.Background(), restoreParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil || result.Workspace.Backend == "" {
		t.Fatalf("expected restored workspace backend to be set, got %#v", result)
	}

	persisted, ok := mgr.Get(created.Workspace.ID)
	if !ok {
		t.Fatal("expected workspace to exist")
	}
	if persisted.Backend != result.Workspace.Backend {
		t.Fatalf("expected persisted backend %q, got %q", result.Workspace.Backend, persisted.Backend)
	}

	reloaded := workspacemgr.NewManager(mgrRoot)
	reloadedWS, ok := reloaded.Get(created.Workspace.ID)
	if !ok {
		t.Fatal("expected workspace to reload from record")
	}
	if reloadedWS.Backend != result.Workspace.Backend {
		t.Fatalf("expected reloaded backend %q, got %q", result.Workspace.Backend, reloadedWS.Backend)
	}
}

func TestHandleWorkspaceRestore_FactoryWithUnavailableCapability(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: false},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams := WorkspaceStopParams{ID: created.Workspace.ID}
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams := WorkspaceRestoreParams{ID: created.Workspace.ID}
	_, rpcErr := HandleWorkspaceRestore(context.Background(), restoreParams, mgr, factory)
	if rpcErr == nil {
		t.Fatal("expected rpc error for unavailable capability, got nil")
	}

	ws, ok := mgr.Get(created.Workspace.ID)
	if !ok {
		t.Fatal("workspace should still exist after failed restore")
	}
	if ws.State == workspacemgr.StateRestored {
		t.Fatalf("workspace state should be %q after failed restore, got %q", workspacemgr.StateStopped, ws.State)
	}
}

func TestHandleWorkspaceRestore_ConfigRequiredBackendHonored(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams := WorkspaceStopParams{ID: created.Workspace.ID}
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams := WorkspaceRestoreParams{ID: created.Workspace.ID}
	result, rpcErr := HandleWorkspaceRestore(context.Background(), restoreParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace, got %#v", result)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected backend 'firecracker' from config required, got %q", result.Workspace.Backend)
	}
}

func TestHandleWorkspacePause(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)
	_ = mgr.Start(created.Workspace.ID)

	pauseParams := WorkspacePauseParams{ID: created.Workspace.ID}
	result, rpcErr := HandleWorkspacePause(context.Background(), pauseParams, mgr, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !result.Paused {
		t.Fatal("expected paused=true")
	}
}

func TestHandleWorkspaceResume(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)
	_ = mgr.Start(created.Workspace.ID)
	_ = mgr.Pause(created.Workspace.ID)

	resumeParams := WorkspaceResumeParams{ID: created.Workspace.ID}
	result, rpcErr := HandleWorkspaceResume(context.Background(), resumeParams, mgr, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !result.Resumed {
		t.Fatal("expected resumed=true")
	}
}

func TestHandleWorkspaceFork(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
			Backend:       "firecracker",
		},
	}
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	forkParams := WorkspaceForkParams{ID: created.Workspace.ID, ChildWorkspaceName: "alpha-child", ChildRef: "alpha-child"}
	result, rpcErr := HandleWorkspaceFork(context.Background(), forkParams, mgr, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result.Workspace == nil {
		t.Fatal("expected child workspace in fork result")
	}
	if result.Workspace.ParentWorkspaceID != created.Workspace.ID {
		t.Fatalf("expected child parent %q, got %q", created.Workspace.ID, result.Workspace.ParentWorkspaceID)
	}
}

func TestHandleWorkspaceFork_WithFactoryLinuxRequiresFirecracker(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: false},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	createParams := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}
	_, rpcErr := HandleWorkspaceCreate(context.Background(), createParams, mgr, factory)
	if rpcErr == nil {
		t.Fatal("expected create failure when runtime.required=linux and firecracker unavailable")
	}
}

func TestHandleWorkspaceFork_WithFactoryLinuxBackendAfterRestartLikeState(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	lxcDriver := &mockDriver{backend: "firecracker"}
	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": lxcDriver,
	})

	parent, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          repo,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("seed workspace failed: %v", err)
	}

	forkParams := WorkspaceForkParams{ID: parent.ID, ChildWorkspaceName: "alpha-child", ChildRef: "alpha-child"}
	result, rpcErr := HandleWorkspaceFork(context.Background(), forkParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("fork failed: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected forked workspace, got %#v", result)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected child backend 'firecracker', got %q", result.Workspace.Backend)
	}
}

func TestHandleWorkspacePause_WithFactoryLinuxBackendAfterRestartLikeState(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	lxcDriver := &mockDriver{backend: "firecracker"}
	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": lxcDriver,
	})

	ws, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          repo,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("seed workspace failed: %v", err)
	}

	_ = mgr.Start(ws.ID)

	pauseParams := WorkspacePauseParams{ID: ws.ID}
	result, rpcErr := HandleWorkspacePause(context.Background(), pauseParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("pause failed: %+v", rpcErr)
	}
	if result == nil || !result.Paused {
		t.Fatalf("expected paused=true, got %#v", result)
	}
}

func TestHandleWorkspaceCreate_WithFactoryFirecrackerBootstrapsRuntime(t *testing.T) {
	t.Cleanup(selection.ResetRuntimeSetupRunnerForTest)

	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	calledCreate := false
	factory := runtime.NewFactory([]runtime.Capability{{Name: "runtime.linux", Available: true}, {Name: "runtime.firecracker", Available: true}}, map[string]runtime.Driver{
		"firecracker": &mockDriver{
			backend: "firecracker",
			createFn: func(ctx context.Context, req runtime.CreateRequest) error {
				calledCreate = true
				if req.WorkspaceID == "" {
					t.Fatal("expected workspace id in runtime create request")
				}
				return nil
			},
		},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace, got %#v", result)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected backend firecracker, got %q", result.Workspace.Backend)
	}
	if !calledCreate {
		t.Fatal("expected runtime create to be called for firecracker backend")
	}
}

func TestHandleWorkspaceCreate_PassesHostAuthBundleToRuntime(t *testing.T) {
	t.Cleanup(selection.ResetRuntimeSetupRunnerForTest)

	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	var gotConfigBundle string
	factory := runtime.NewFactory([]runtime.Capability{{Name: "runtime.linux", Available: true}, {Name: "runtime.firecracker", Available: true}}, map[string]runtime.Driver{
		"firecracker": &mockDriver{
			backend: "firecracker",
			createFn: func(ctx context.Context, req runtime.CreateRequest) error {
				gotConfigBundle = req.ConfigBundle
				return nil
			},
		},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
			ConfigBundle:  "e30=",
		},
	}

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace, got %#v", result)
	}
	if gotConfigBundle != "e30=" {
		t.Fatalf("expected configBundle in create request, got %q", gotConfigBundle)
	}
}

func TestHandleWorkspaceCreate_InstallableMissingRetriesSetupOnce(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	selection.SetPreflightSequenceForTest([]runtime.FirecrackerPreflightResult{
		{Status: runtime.PreflightInstallableMissing, Checks: []runtime.PreflightCheck{{Name: "lima", OK: false, Installable: true}}},
		{Status: runtime.PreflightPass},
	})
	setupCalls := 0
	selection.SetRuntimeSetupRunnerForTest(func(_ context.Context, _ string, _ string) error {
		setupCalls++
		return nil
	})
	t.Cleanup(func() {
		selection.ResetRuntimeSetupRunnerForTest()
		selection.ResetPreflightRunnerForTest()
	})

	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)

	params := WorkspaceCreateParams{Spec: workspacemgr.CreateSpec{Repo: repo, WorkspaceName: "alpha", AgentProfile: "default"}}
	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected firecracker backend, got %q", result.Workspace.Backend)
	}
	if setupCalls != 1 {
		t.Fatalf("expected one setup attempt, got %d", setupCalls)
	}
}

func TestHandleWorkspaceCreate_InstallableMissingSetupFailureReturnsPreflightError(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	preflightCalls := 0
	selection.SetFirecrackerPreflightRunnerForTest(func(_ string, _ runtime.PreflightOptions) runtime.FirecrackerPreflightResult {
		preflightCalls++
		return runtime.FirecrackerPreflightResult{
			Status: runtime.PreflightInstallableMissing,
			Checks: []runtime.PreflightCheck{{Name: "lima", OK: false, Installable: true}},
		}
	})
	setupCalls := 0
	selection.SetRuntimeSetupRunnerForTest(func(_ context.Context, _ string, _ string) error {
		setupCalls++
		return fmt.Errorf("bootstrap failed")
	})
	t.Cleanup(func() {
		selection.ResetRuntimeSetupRunnerForTest()
		selection.ResetPreflightRunnerForTest()
	})

	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)

	params := WorkspaceCreateParams{Spec: workspacemgr.CreateSpec{Repo: repo, WorkspaceName: "alpha", AgentProfile: "default"}}
	_, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr == nil {
		t.Fatal("expected rpc error")
	}
	if setupCalls != 1 {
		t.Fatalf("expected one setup attempt, got %d", setupCalls)
	}
	if preflightCalls != 1 {
		t.Fatalf("expected exactly one preflight call, got %d", preflightCalls)
	}
	if !strings.Contains(rpcErr.Message, string(runtime.PreflightInstallableMissing)) {
		t.Fatalf("expected installable_missing status, got %q", rpcErr.Message)
	}
	if !strings.Contains(rpcErr.Message, "bootstrap failed") {
		t.Fatalf("expected setup failure details, got %q", rpcErr.Message)
	}
}

func TestHandleWorkspaceCreate_UnsupportedNestedVirtFallsBackToSeatbelt(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	selection.SetPreflightSequenceForTest([]runtime.FirecrackerPreflightResult{{Status: runtime.PreflightUnsupportedNested}})
	setupCalls := 0
	selection.SetRuntimeSetupRunnerForTest(func(_ context.Context, _ string, _ string) error {
		setupCalls++
		return nil
	})
	t.Cleanup(func() {
		selection.ResetRuntimeSetupRunnerForTest()
		selection.ResetPreflightRunnerForTest()
	})

	factory := runtime.NewFactory(
		[]runtime.Capability{
			{Name: "runtime.seatbelt", Available: true},
			{Name: "runtime.firecracker", Available: true},
		},
		map[string]runtime.Driver{
			"seatbelt":    &mockDriver{backend: "seatbelt"},
			"firecracker": &mockDriver{backend: "firecracker"},
		},
	)

	params := WorkspaceCreateParams{Spec: workspacemgr.CreateSpec{Repo: repo, WorkspaceName: "alpha", AgentProfile: "default"}}
	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result.Workspace.Backend != "seatbelt" {
		t.Fatalf("expected seatbelt backend, got %q", result.Workspace.Backend)
	}
	if setupCalls != 0 {
		t.Fatalf("expected zero setup attempts, got %d", setupCalls)
	}
}

func TestHandleWorkspaceCreate_HardFailReturnsStructuredPreflightError(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	selection.SetPreflightSequenceForTest([]runtime.FirecrackerPreflightResult{{
		Status: runtime.PreflightHardFail,
		Checks: []runtime.PreflightCheck{{Name: "kvm", OK: false, Message: "kvm unavailable"}},
	}})
	t.Cleanup(func() {
		selection.ResetRuntimeSetupRunnerForTest()
		selection.ResetPreflightRunnerForTest()
	})

	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)

	params := WorkspaceCreateParams{Spec: workspacemgr.CreateSpec{Repo: repo, WorkspaceName: "alpha", AgentProfile: "default"}}
	_, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr == nil {
		t.Fatal("expected rpc error")
	}
	if !strings.Contains(rpcErr.Message, "runtime preflight failed") {
		t.Fatalf("expected preflight failure message, got %q", rpcErr.Message)
	}
	if !strings.Contains(rpcErr.Message, string(runtime.PreflightHardFail)) {
		t.Fatalf("expected hard_fail status in message, got %q", rpcErr.Message)
	}
}

func TestHandleWorkspaceCreate_UsesInternalPreflightOverrideWhenEnabled(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	t.Setenv("NEXUS_INTERNAL_ENABLE_PREFLIGHT_OVERRIDE", "1")
	t.Setenv("NEXUS_INTERNAL_PREFLIGHT_OVERRIDE", "unsupported_nested_virt")

	factory := runtime.NewFactory(
		[]runtime.Capability{
			{Name: "runtime.seatbelt", Available: true},
			{Name: "runtime.firecracker", Available: true},
		},
		map[string]runtime.Driver{
			"seatbelt":    &mockDriver{backend: "seatbelt"},
			"firecracker": &mockDriver{backend: "firecracker"},
		},
	)

	params := WorkspaceCreateParams{Spec: workspacemgr.CreateSpec{Repo: repo, WorkspaceName: "alpha", AgentProfile: "default"}}
	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result.Workspace.Backend != "seatbelt" {
		t.Fatalf("expected seatbelt backend from override, got %q", result.Workspace.Backend)
	}
}

func TestHandleWorkspaceCreate_IgnoresInternalPreflightOverrideWhenDisabled(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	t.Setenv("NEXUS_INTERNAL_ENABLE_PREFLIGHT_OVERRIDE", "0")
	t.Setenv("NEXUS_INTERNAL_PREFLIGHT_OVERRIDE", "hard_fail")

	t.Cleanup(selection.ResetRuntimeSetupRunnerForTest)

	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)

	params := WorkspaceCreateParams{Spec: workspacemgr.CreateSpec{Repo: repo, WorkspaceName: "alpha", AgentProfile: "default"}}
	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected firecracker backend when override disabled, got %q", result.Workspace.Backend)
	}
}

func TestDefaultPlatformHints(t *testing.T) {
	required, caps := create.DefaultPlatformHints()
	if len(required) != 2 || required[0] != "darwin" || required[1] != "linux" {
		t.Fatalf("expected [darwin linux], got %v", required)
	}
	if len(caps) != 0 {
		t.Fatalf("expected no required capabilities, got %v", caps)
	}
}
