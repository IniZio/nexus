package handlers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestHandleWorkspaceCreate(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())

	params, err := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
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
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)

	// Create workspace config with runtime.required
	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1,"runtime":{"required":["linux"],"selection":"prefer-first"}}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	params, err := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
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
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1,"runtime":{"required":["linux"],"selection":"prefer-first"}}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	params, err := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
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

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: false},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	params, err := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	_, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr == nil {
		t.Fatalf("expected rpc error for unavailable capability, got nil")
	}
}

func TestHandleWorkspaceCreate_MissingRuntimeRequiredFallsBack(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)

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

	params, err := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace, got %#v", result)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected backend 'firecracker' fallback, got %q", result.Workspace.Backend)
	}
}

func TestHandleWorkspaceCreate_MissingRuntimeRequiredRespectsSpecBackend(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)

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

	params, err := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
			Backend:       "local",
		},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace, got %#v", result)
	}
	if result.Workspace.Backend != "local" {
		t.Fatalf("expected backend 'local' from spec fallback priority, got %q", result.Workspace.Backend)
	}
}

type mockDriver struct {
	backend string
}

func (d *mockDriver) Backend() string                                             { return d.backend }
func (d *mockDriver) Create(ctx context.Context, req runtime.CreateRequest) error { return nil }
func (d *mockDriver) Start(ctx context.Context, workspaceID string) error         { return nil }
func (d *mockDriver) Stop(ctx context.Context, workspaceID string) error          { return nil }
func (d *mockDriver) Restore(ctx context.Context, workspaceID string) error       { return nil }
func (d *mockDriver) Pause(ctx context.Context, workspaceID string) error         { return nil }
func (d *mockDriver) Resume(ctx context.Context, workspaceID string) error        { return nil }
func (d *mockDriver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	return nil
}
func (d *mockDriver) Destroy(ctx context.Context, workspaceID string) error { return nil }

func TestHandleWorkspaceOpen_NotFound(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	params, _ := json.Marshal(WorkspaceOpenParams{ID: "missing"})

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
	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})

	created, rpcErr := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)
	if rpcErr != nil {
		t.Fatalf("create failed: %+v", rpcErr)
	}

	list, rpcErr := HandleWorkspaceList(context.Background(), nil, mgr)
	if rpcErr != nil {
		t.Fatalf("list failed: %+v", rpcErr)
	}
	if len(list.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(list.Workspaces))
	}

	removeParams, _ := json.Marshal(WorkspaceRemoveParams{ID: created.Workspace.ID})
	removed, rpcErr := HandleWorkspaceRemove(context.Background(), removeParams, mgr)
	if rpcErr != nil {
		t.Fatalf("remove failed: %+v", rpcErr)
	}
	if !removed.Removed {
		t.Fatal("expected removed=true")
	}
}

func TestHandleWorkspaceStop(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams, _ := json.Marshal(WorkspaceStopParams{ID: created.Workspace.ID})
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
	stopParams, _ := json.Marshal(WorkspaceStopParams{ID: "missing"})
	_, rpcErr := HandleWorkspaceStop(context.Background(), stopParams, mgr)
	if rpcErr == nil {
		t.Fatal("expected workspace not found error")
	}
}

func TestHandleWorkspaceStart(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	startParams, _ := json.Marshal(WorkspaceStartParams{ID: created.Workspace.ID})
	result, rpcErr := HandleWorkspaceStart(context.Background(), startParams, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !result.Started {
		t.Fatal("expected started=true")
	}
}

func TestHandleWorkspaceStart_NotFound(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	startParams, _ := json.Marshal(WorkspaceStartParams{ID: "missing"})
	_, rpcErr := HandleWorkspaceStart(context.Background(), startParams, mgr)
	if rpcErr == nil {
		t.Fatal("expected workspace not found error")
	}
}

func TestHandleWorkspaceRestore(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams, _ := json.Marshal(WorkspaceStopParams{ID: created.Workspace.ID})
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams, _ := json.Marshal(WorkspaceRestoreParams{ID: created.Workspace.ID})
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
	restoreParams, _ := json.Marshal(WorkspaceRestoreParams{ID: "missing"})
	_, rpcErr := HandleWorkspaceRestore(context.Background(), restoreParams, mgr, nil)
	if rpcErr == nil {
		t.Fatal("expected workspace not found error")
	}
}

func TestHandleWorkspaceRestore_WithFactory(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)

	// Create workspace config with runtime.required
	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1,"runtime":{"required":["linux"],"selection":"prefer-first"}}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams, _ := json.Marshal(WorkspaceStopParams{ID: created.Workspace.ID})
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams, _ := json.Marshal(WorkspaceRestoreParams{ID: created.Workspace.ID})
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

	// Create workspace config with runtime.required
	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1,"runtime":{"required":["linux"],"selection":"prefer-first"}}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams, _ := json.Marshal(WorkspaceStopParams{ID: created.Workspace.ID})
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams, _ := json.Marshal(WorkspaceRestoreParams{ID: created.Workspace.ID})
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

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: false},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams, _ := json.Marshal(WorkspaceStopParams{ID: created.Workspace.ID})
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams, _ := json.Marshal(WorkspaceRestoreParams{ID: created.Workspace.ID})
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

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1,"runtime":{"required":["linux"],"selection":"prefer-first"}}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
	}, map[string]runtime.Driver{
		"firecracker": &mockDriver{backend: "firecracker"},
	})

	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	stopParams, _ := json.Marshal(WorkspaceStopParams{ID: created.Workspace.ID})
	HandleWorkspaceStop(context.Background(), stopParams, mgr)

	restoreParams, _ := json.Marshal(WorkspaceRestoreParams{ID: created.Workspace.ID})
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
	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)
	_ = mgr.Start(created.Workspace.ID)

	pauseParams, _ := json.Marshal(WorkspacePauseParams{ID: created.Workspace.ID})
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
	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)
	_ = mgr.Start(created.Workspace.ID)
	_ = mgr.Pause(created.Workspace.ID)

	resumeParams, _ := json.Marshal(WorkspaceResumeParams{ID: created.Workspace.ID})
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
	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
			Backend:       "firecracker",
		},
	})
	created, _ := HandleWorkspaceCreate(context.Background(), createParams, mgr, nil)

	forkParams, _ := json.Marshal(WorkspaceForkParams{ID: created.Workspace.ID, ChildWorkspaceName: "alpha-child", ChildRef: "alpha-child"})
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

func TestHandleWorkspaceFork_WithFactoryLinuxFallbackBackend(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	configData := []byte(`{"version":1,"runtime":{"required":["linux"],"selection":"prefer-first"}}`)
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.lxc", Available: true},
	}, map[string]runtime.Driver{
		"lxc": &mockDriver{backend: "lxc"},
	})

	createParams, _ := json.Marshal(WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	})
	created, rpcErr := HandleWorkspaceCreate(context.Background(), createParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("create failed: %+v", rpcErr)
	}

	forkParams, _ := json.Marshal(WorkspaceForkParams{ID: created.Workspace.ID, ChildWorkspaceName: "alpha-child", ChildRef: "alpha-child"})
	result, rpcErr := HandleWorkspaceFork(context.Background(), forkParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("fork failed: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected forked workspace, got %#v", result)
	}
	if result.Workspace.Backend != "lxc" {
		t.Fatalf("expected child backend 'lxc', got %q", result.Workspace.Backend)
	}
	if result.Workspace.ParentWorkspaceID != created.Workspace.ID {
		t.Fatalf("expected parent id %q, got %q", created.Workspace.ID, result.Workspace.ParentWorkspaceID)
	}
}

func TestHandleWorkspaceFork_WithFactoryLinuxBackendAfterRestartLikeState(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), []byte(`{"version":1,"runtime":{"required":["linux"],"selection":"prefer-first"}}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	lxcDriver := &mockDriver{backend: "lxc"}
	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.lxc", Available: true},
	}, map[string]runtime.Driver{
		"lxc": lxcDriver,
	})

	parent, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "lxc",
	})
	if err != nil {
		t.Fatalf("seed workspace failed: %v", err)
	}

	forkParams, _ := json.Marshal(WorkspaceForkParams{ID: parent.ID, ChildWorkspaceName: "alpha-child", ChildRef: "alpha-child"})
	result, rpcErr := HandleWorkspaceFork(context.Background(), forkParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("fork failed: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected forked workspace, got %#v", result)
	}
	if result.Workspace.Backend != "lxc" {
		t.Fatalf("expected child backend 'lxc', got %q", result.Workspace.Backend)
	}
}

func TestHandleWorkspacePause_WithFactoryLinuxBackendAfterRestartLikeState(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)

	if err := os.MkdirAll(filepath.Join(mgrRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mgrRoot, ".nexus", "workspace.json"), []byte(`{"version":1,"runtime":{"required":["linux"],"selection":"prefer-first"}}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	lxcDriver := &mockDriver{backend: "lxc"}
	factory := runtime.NewFactory([]runtime.Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.lxc", Available: true},
	}, map[string]runtime.Driver{
		"lxc": lxcDriver,
	})

	ws, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "lxc",
	})
	if err != nil {
		t.Fatalf("seed workspace failed: %v", err)
	}

	_ = mgr.Start(ws.ID)

	pauseParams, _ := json.Marshal(WorkspacePauseParams{ID: ws.ID})
	result, rpcErr := HandleWorkspacePause(context.Background(), pauseParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("pause failed: %+v", rpcErr)
	}
	if result == nil || !result.Paused {
		t.Fatalf("expected paused=true, got %#v", result)
	}
}
