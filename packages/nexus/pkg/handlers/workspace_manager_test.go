package handlers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
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

func TestHandleWorkspaceCreateWithProjects_UsesProjectFirstPayload(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	projMgr := projectmgr.NewManager(mgrRoot, mgr.ProjectRepository())
	mgr.SetProjectManager(projMgr)

	project, err := projMgr.GetOrCreateForRepo("git@example/repo.git", "repo-test")
	if err != nil {
		t.Fatalf("seed project failed: %v", err)
	}
	rootWS, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "root",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("create root ws: %v", err)
	}
	if err := mgr.UpdateProjectID(rootWS.ID, project.ID); err != nil {
		t.Fatalf("update root project id: %v", err)
	}

	params := WorkspaceCreateParams{
		ProjectID:     project.ID,
		TargetBranch:  "feature-proj",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	}
	result, rpcErr := HandleWorkspaceCreateWithProjects(context.Background(), params, mgr, projMgr, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace, got %#v", result)
	}
	if result.Workspace.Repo != "git@example/repo.git" {
		t.Fatalf("expected repo from project, got %q", result.Workspace.Repo)
	}
	if result.Workspace.Ref != "feature-proj" {
		t.Fatalf("expected target branch as ref, got %q", result.Workspace.Ref)
	}
	if result.FreshApplied {
		t.Fatal("expected non-fresh create path")
	}
	if result.Workspace.ParentWorkspaceID != rootWS.ID {
		t.Fatalf("expected parent workspace %q, got %q", rootWS.ID, result.Workspace.ParentWorkspaceID)
	}
}

func TestHandleWorkspaceCreateWithProjects_RequiresProjectRootForForkedCreate(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	projMgr := projectmgr.NewManager(mgrRoot, mgr.ProjectRepository())
	mgr.SetProjectManager(projMgr)

	project, err := projMgr.GetOrCreateForRepo("git@example/repo.git", "repo-test")
	if err != nil {
		t.Fatalf("seed project failed: %v", err)
	}

	params := WorkspaceCreateParams{
		ProjectID:     project.ID,
		TargetBranch:  "feature-proj",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Fresh:         false,
	}
	result, rpcErr := HandleWorkspaceCreateWithProjects(context.Background(), params, mgr, projMgr, nil)
	if result != nil {
		t.Fatalf("expected nil result, got %#v", result)
	}
	if rpcErr == nil {
		t.Fatal("expected rpc error for missing project root")
	}
	if rpcErr.Code != rpckit.ErrInvalidParams.Code {
		t.Fatalf("expected invalid params code %d, got %+v", rpckit.ErrInvalidParams.Code, rpcErr)
	}
	if !strings.Contains(strings.ToLower(rpcErr.Message), "project root sandbox is missing") {
		t.Fatalf("expected missing project root message, got %+v", rpcErr)
	}
}

func TestShouldUseProjectRootPathForBase(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	req := WorkspaceCreateParams{
		ProjectID: "proj-1",
		Fresh:     true,
	}
	spec := workspacemgr.CreateSpec{Repo: "git@example/repo.git"}
	if !shouldUseProjectRootPathForBase(req, spec, mgr) {
		t.Fatal("expected project base create to use project root host path")
	}
}

func TestShouldUseProjectRootPathForBase_FalseWhenRootExists(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	rootWS, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "base",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create root workspace: %v", err)
	}
	if err := mgr.UpdateProjectID(rootWS.ID, "proj-1"); err != nil {
		t.Fatalf("set project id: %v", err)
	}

	req := WorkspaceCreateParams{
		ProjectID: "proj-1",
		Fresh:     true,
	}
	spec := workspacemgr.CreateSpec{Repo: "git@example/repo.git"}
	if shouldUseProjectRootPathForBase(req, spec, mgr) {
		t.Fatal("expected false when project root workspace already exists")
	}
}

func TestHandleWorkspaceCreateWithProjects_UsesExplicitSourceBranchSnapshot(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	projMgr := projectmgr.NewManager(mgrRoot, mgr.ProjectRepository())
	mgr.SetProjectManager(projMgr)

	project, err := projMgr.GetOrCreateForRepo("git@example/repo.git", "repo-test")
	if err != nil {
		t.Fatalf("seed project failed: %v", err)
	}
	mainWS, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "main-base",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("create main ws: %v", err)
	}
	relWS, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "release",
		WorkspaceName: "release-base",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("create release ws: %v", err)
	}
	if err := mgr.UpdateProjectID(mainWS.ID, project.ID); err != nil {
		t.Fatalf("update main project id: %v", err)
	}
	if err := mgr.UpdateProjectID(relWS.ID, project.ID); err != nil {
		t.Fatalf("update release project id: %v", err)
	}
	if err := mgr.SetLineageSnapshot(mainWS.ID, "snap-main"); err != nil {
		t.Fatalf("set main snapshot: %v", err)
	}
	if err := mgr.SetLineageSnapshot(relWS.ID, "snap-release"); err != nil {
		t.Fatalf("set release snapshot: %v", err)
	}

	var gotSnapshot string
	factory := runtime.NewFactory([]runtime.Capability{{Name: "runtime.linux", Available: true}, {Name: "runtime.firecracker", Available: true}}, map[string]runtime.Driver{
		"firecracker": &mockDriver{
			backend: "firecracker",
			checkpointForkFn: func(_ context.Context, workspaceID, childWorkspaceID string) (string, error) {
				if workspaceID != relWS.ID {
					t.Fatalf("expected checkpoint parent %q, got %q", relWS.ID, workspaceID)
				}
				if strings.TrimSpace(childWorkspaceID) == "" {
					t.Fatal("expected non-empty child workspace id for checkpoint")
				}
				return "snap-release-latest", nil
			},
			createFn: func(_ context.Context, req runtime.CreateRequest) error {
				gotSnapshot = strings.TrimSpace(req.Options["lineage_snapshot_id"])
				return nil
			},
		},
	})

	params := WorkspaceCreateParams{
		ProjectID:     project.ID,
		TargetBranch:  "feature-x",
		SourceBranch:  "release",
		WorkspaceName: "child",
		AgentProfile:  "default",
	}
	result, rpcErr := HandleWorkspaceCreateWithProjects(context.Background(), params, mgr, projMgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace result, got %#v", result)
	}
	if gotSnapshot != "snap-release-latest" {
		t.Fatalf("expected fresh release snapshot, got %q", gotSnapshot)
	}
	if result.EffectiveSourceBranch != "release" {
		t.Fatalf("expected effective source branch %q, got %q", "release", result.EffectiveSourceBranch)
	}
	if result.SourceWorkspaceID != relWS.ID {
		t.Fatalf("expected source workspace %q, got %q", relWS.ID, result.SourceWorkspaceID)
	}
	if result.UsedLineageSnapshotID != "snap-release-latest" {
		t.Fatalf("expected used snapshot %q, got %q", "snap-release-latest", result.UsedLineageSnapshotID)
	}
	if result.Workspace.DerivedFromRef != "release" {
		t.Fatalf("expected derivedFromRef %q, got %q", "release", result.Workspace.DerivedFromRef)
	}
	if result.Workspace.ParentWorkspaceID != relWS.ID {
		t.Fatalf("expected parent workspace %q, got %q", relWS.ID, result.Workspace.ParentWorkspaceID)
	}
}

func TestHandleWorkspaceCreateWithProjects_FreshSkipsSourceSnapshot(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	projMgr := projectmgr.NewManager(mgrRoot, mgr.ProjectRepository())
	mgr.SetProjectManager(projMgr)

	project, err := projMgr.GetOrCreateForRepo("git@example/repo.git", "repo-test")
	if err != nil {
		t.Fatalf("seed project failed: %v", err)
	}
	baseWS, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "main-base",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("create base ws: %v", err)
	}
	if err := mgr.UpdateProjectID(baseWS.ID, project.ID); err != nil {
		t.Fatalf("update project id: %v", err)
	}
	if err := mgr.SetLineageSnapshot(baseWS.ID, "snap-main"); err != nil {
		t.Fatalf("set snapshot: %v", err)
	}

	var gotSnapshot string
	factory := runtime.NewFactory([]runtime.Capability{{Name: "runtime.linux", Available: true}, {Name: "runtime.firecracker", Available: true}}, map[string]runtime.Driver{
		"firecracker": &mockDriver{
			backend: "firecracker",
			checkpointForkFn: func(_ context.Context, workspaceID, childWorkspaceID string) (string, error) {
				if workspaceID != baseWS.ID {
					t.Fatalf("expected checkpoint parent %q, got %q", baseWS.ID, workspaceID)
				}
				if strings.TrimSpace(childWorkspaceID) == "" {
					t.Fatal("expected non-empty child workspace id for checkpoint")
				}
				return "snap-source-latest", nil
			},
			createFn: func(_ context.Context, req runtime.CreateRequest) error {
				gotSnapshot = strings.TrimSpace(req.Options["lineage_snapshot_id"])
				return nil
			},
		},
	})

	params := WorkspaceCreateParams{
		ProjectID:     project.ID,
		TargetBranch:  "feature-y",
		WorkspaceName: "fresh-child",
		AgentProfile:  "default",
		Fresh:         true,
	}
	result, rpcErr := HandleWorkspaceCreateWithProjects(context.Background(), params, mgr, projMgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace result, got %#v", result)
	}
	if gotSnapshot != "" {
		t.Fatalf("expected no lineage snapshot in runtime create for fresh mode, got %q", gotSnapshot)
	}
	if !result.FreshApplied {
		t.Fatal("expected freshApplied true")
	}
	if result.UsedLineageSnapshotID != "" {
		t.Fatalf("expected no used snapshot for fresh mode, got %q", result.UsedLineageSnapshotID)
	}
	if strings.TrimSpace(result.Workspace.DerivedFromRef) != "" {
		t.Fatalf("expected empty derivedFromRef for fresh mode, got %q", result.Workspace.DerivedFromRef)
	}
}

func TestHandleWorkspaceCreateWithProjects_UsesSourceWorkspaceID(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	projMgr := projectmgr.NewManager(mgrRoot, mgr.ProjectRepository())
	mgr.SetProjectManager(projMgr)

	project, err := projMgr.GetOrCreateForRepo("git@example/repo.git", "repo-test")
	if err != nil {
		t.Fatalf("seed project failed: %v", err)
	}
	sourceWS, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "release",
		WorkspaceName: "release-base",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("create source ws: %v", err)
	}
	if err := mgr.UpdateProjectID(sourceWS.ID, project.ID); err != nil {
		t.Fatalf("update project id: %v", err)
	}
	if err := mgr.SetLineageSnapshot(sourceWS.ID, "snap-source-ws"); err != nil {
		t.Fatalf("set snapshot: %v", err)
	}

	var gotSnapshot string
	factory := runtime.NewFactory([]runtime.Capability{{Name: "runtime.linux", Available: true}, {Name: "runtime.firecracker", Available: true}}, map[string]runtime.Driver{
		"firecracker": &mockDriver{
			backend: "firecracker",
			checkpointForkFn: func(_ context.Context, workspaceID, childWorkspaceID string) (string, error) {
				if workspaceID != sourceWS.ID {
					t.Fatalf("expected checkpoint parent %q, got %q", sourceWS.ID, workspaceID)
				}
				if strings.TrimSpace(childWorkspaceID) == "" {
					t.Fatal("expected non-empty child workspace id for checkpoint")
				}
				return "snap-source-latest", nil
			},
			createFn: func(_ context.Context, req runtime.CreateRequest) error {
				gotSnapshot = strings.TrimSpace(req.Options["lineage_snapshot_id"])
				return nil
			},
		},
	})

	params := WorkspaceCreateParams{
		ProjectID:         project.ID,
		TargetBranch:      "feature-z",
		SourceWorkspaceID: sourceWS.ID,
		WorkspaceName:     "child",
		AgentProfile:      "default",
	}
	result, rpcErr := HandleWorkspaceCreateWithProjects(context.Background(), params, mgr, projMgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace result, got %#v", result)
	}
	if gotSnapshot != "snap-source-latest" {
		t.Fatalf("expected source workspace checkpoint snapshot, got %q", gotSnapshot)
	}
	if result.SourceWorkspaceID != sourceWS.ID {
		t.Fatalf("expected source workspace id %q, got %q", sourceWS.ID, result.SourceWorkspaceID)
	}
	if result.Workspace.ParentWorkspaceID != sourceWS.ID {
		t.Fatalf("expected parent workspace id %q, got %q", sourceWS.ID, result.Workspace.ParentWorkspaceID)
	}
}

func TestHandleWorkspaceCreateWithProjects_CopiesDirtyStateFromSourceWorkspace(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	projMgr := projectmgr.NewManager(mgrRoot, mgr.ProjectRepository())
	mgr.SetProjectManager(projMgr)

	repoRoot := initGitRepoForCheckoutHandlerTests(t)
	project, err := projMgr.GetOrCreateForRepo(repoRoot, "repo-local")
	if err != nil {
		t.Fatalf("seed project failed: %v", err)
	}
	sourceWS, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          repoRoot,
		Ref:           "main",
		WorkspaceName: "base",
		AgentProfile:  "default",
		Backend:       "seatbelt",
	})
	if err != nil {
		t.Fatalf("create source ws: %v", err)
	}
	if err := mgr.UpdateProjectID(sourceWS.ID, project.ID); err != nil {
		t.Fatalf("update project id: %v", err)
	}

	if err := os.WriteFile(filepath.Join(sourceWS.LocalWorktreePath, "README.md"), []byte("# YOLO\n"), 0o644); err != nil {
		t.Fatalf("write tracked dirty file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceWS.LocalWorktreePath, "YOLO"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	params := WorkspaceCreateParams{
		ProjectID:         project.ID,
		TargetBranch:      "feat-4",
		SourceWorkspaceID: sourceWS.ID,
		WorkspaceName:     "feat-4",
		AgentProfile:      "default",
	}
	result, rpcErr := HandleWorkspaceCreateWithProjects(context.Background(), params, mgr, projMgr, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace result, got %#v", result)
	}

	trackedData, err := os.ReadFile(filepath.Join(result.Workspace.LocalWorktreePath, "README.md"))
	if err != nil {
		t.Fatalf("read child tracked file: %v", err)
	}
	if string(trackedData) != "# YOLO\n" {
		t.Fatalf("expected tracked dirty content in child, got %q", string(trackedData))
	}
	untrackedData, err := os.ReadFile(filepath.Join(result.Workspace.LocalWorktreePath, "YOLO"))
	if err != nil {
		t.Fatalf("read child untracked file: %v", err)
	}
	if string(untrackedData) != "untracked\n" {
		t.Fatalf("expected untracked file in child, got %q", string(untrackedData))
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

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error with registered backend driver: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace create result, got %#v", result)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected backend firecracker, got %q", result.Workspace.Backend)
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
		{Name: "runtime.process", Available: true},
	}, map[string]runtime.Driver{
		"process": &mockDriver{backend: "process"},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
			Backend:       "process",
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
		{Name: "runtime.process", Available: true},
	}, map[string]runtime.Driver{
		"process": &mockDriver{backend: "process"},
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
	backend          string
	createFn         func(ctx context.Context, req runtime.CreateRequest) error
	checkpointForkFn func(ctx context.Context, workspaceID, childWorkspaceID string) (string, error)
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
func (d *mockDriver) CheckpointFork(ctx context.Context, workspaceID, childWorkspaceID string) (string, error) {
	if d.checkpointForkFn != nil {
		return d.checkpointForkFn(ctx, workspaceID, childWorkspaceID)
	}
	return "", nil
}

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

func TestHandleWorkspaceRemove_RejectsDeleteHostPathForProjectRootSandbox(t *testing.T) {
	root := t.TempDir()
	mgr := workspacemgr.NewManager(root)
	projMgr := projectmgr.NewManager(root, mgr.ProjectRepository())
	mgr.SetProjectManager(projMgr)

	created, rpcErr := HandleWorkspaceCreate(context.Background(), WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          "git@example/repo.git",
			Ref:           "main",
			WorkspaceName: "root",
			AgentProfile:  "default",
		},
	}, mgr, nil)
	if rpcErr != nil {
		t.Fatalf("create failed: %+v", rpcErr)
	}
	if created.Workspace == nil {
		t.Fatal("expected created workspace")
	}
	if strings.TrimSpace(created.Workspace.ParentWorkspaceID) != "" {
		t.Fatalf("expected root sandbox with empty parent workspace id, got %q", created.Workspace.ParentWorkspaceID)
	}
	if strings.TrimSpace(created.Workspace.ProjectID) == "" {
		t.Fatal("expected project-scoped workspace")
	}

	_, rpcErr = HandleWorkspaceRemove(context.Background(), WorkspaceRemoveParams{
		ID:             created.Workspace.ID,
		DeleteHostPath: true,
	}, mgr, nil)
	if rpcErr == nil {
		t.Fatal("expected invalid params error")
	}
	if rpcErr.Code != rpckit.ErrInvalidParams.Code {
		t.Fatalf("expected invalid params code %d, got %+v", rpckit.ErrInvalidParams.Code, rpcErr)
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
	result, rpcErr := HandleWorkspaceStart(context.Background(), startParams, mgr, nil)
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
	_, rpcErr := HandleWorkspaceStart(context.Background(), startParams, mgr, nil)
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
	result, rpcErr := HandleWorkspaceRestore(context.Background(), restoreParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected restore error with registered backend driver: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected restore result workspace, got %#v", result)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected restored backend firecracker, got %q", result.Workspace.Backend)
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
	if strings.TrimSpace(result.Workspace.LineageSnapshotID) == "" {
		t.Fatal("expected child lineage snapshot id to be set on fork")
	}
}

func TestHandleWorkspaceCheckout(t *testing.T) {
	repoRoot := initGitRepoForCheckoutHandlerTests(t)
	mgr := workspacemgr.NewManager(t.TempDir())
	created, _ := HandleWorkspaceCreate(context.Background(), WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repoRoot,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}, mgr, nil)
	if created == nil || created.Workspace == nil {
		t.Fatal("expected created workspace")
	}

	result, rpcErr := HandleWorkspaceCheckout(context.Background(), WorkspaceCheckoutParams{
		WorkspaceID: created.Workspace.ID,
		TargetRef:   "feature-z",
	}, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected checkout result, got %#v", result)
	}
	if result.CurrentRef != "feature-z" {
		t.Fatalf("expected currentRef %q, got %q", "feature-z", result.CurrentRef)
	}
	if strings.TrimSpace(result.CurrentCommit) == "" {
		t.Fatal("expected currentCommit to be populated after git checkout")
	}
}

func TestHandleWorkspaceCheckout_CreatesNewBranchWhenMissing(t *testing.T) {
	repoRoot := initGitRepoForCheckoutHandlerTests(t)
	mgr := workspacemgr.NewManager(t.TempDir())
	created, _ := HandleWorkspaceCreate(context.Background(), WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repoRoot,
			Ref:           "main",
			WorkspaceName: "alpha",
			AgentProfile:  "default",
		},
	}, mgr, nil)
	if created == nil || created.Workspace == nil {
		t.Fatal("expected created workspace")
	}

	result, rpcErr := HandleWorkspaceCheckout(context.Background(), WorkspaceCheckoutParams{
		WorkspaceID: created.Workspace.ID,
		TargetRef:   "feature-new-branch",
	}, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected checkout result, got %#v", result)
	}
	if result.CurrentRef != "feature-new-branch" {
		t.Fatalf("expected currentRef %q, got %q", "feature-new-branch", result.CurrentRef)
	}
}

func TestHandleWorkspaceCheckout_RejectsDirtyTreeByDefault(t *testing.T) {
	repoRoot := initGitRepoForCheckoutHandlerTests(t)
	mgr := workspacemgr.NewManager(t.TempDir())
	ws, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          repoRoot,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create workspace failed: %v", err)
	}

	filePath := filepath.Join(ws.LocalWorktreePath, "README.md")
	if err := os.WriteFile(filePath, []byte("# changed\n"), 0o644); err != nil {
		t.Fatalf("write dirty change: %v", err)
	}

	_, rpcErr := HandleWorkspaceCheckout(context.Background(), WorkspaceCheckoutParams{
		WorkspaceID: ws.ID,
		TargetRef:   "main",
	}, mgr)
	if rpcErr == nil {
		t.Fatal("expected checkout conflict error for dirty tree")
	}
	if rpcErr.Code != rpckit.ErrCheckoutConflict.Code {
		t.Fatalf("expected checkout conflict code %d, got %+v", rpckit.ErrCheckoutConflict.Code, rpcErr)
	}
	if !strings.Contains(strings.ToLower(rpcErr.Message), "retry with onconflict=stash") {
		t.Fatalf("expected prompt-style guidance message, got %+v", rpcErr)
	}
	dataMap, ok := rpcErr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected structured rpc error data, got %#v", rpcErr.Data)
	}
	if kind, _ := dataMap["kind"].(string); kind != "workspace.checkout.conflict" {
		t.Fatalf("expected conflict kind, got %#v", dataMap["kind"])
	}
	if actions, ok := dataMap["suggestedActions"].([]map[string]any); !ok || len(actions) == 0 {
		t.Fatalf("expected suggestedActions in rpc error data, got %#v", dataMap["suggestedActions"])
	}
}

func initGitRepoForCheckoutHandlerTests(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	runGitForCheckoutHandlerTests(t, repoRoot, "init")
	runGitForCheckoutHandlerTests(t, repoRoot, "config", "user.email", "nexus-tests@example.com")
	runGitForCheckoutHandlerTests(t, repoRoot, "config", "user.name", "Nexus Tests")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGitForCheckoutHandlerTests(t, repoRoot, "add", "README.md")
	runGitForCheckoutHandlerTests(t, repoRoot, "commit", "-m", "init")
	runGitForCheckoutHandlerTests(t, repoRoot, "branch", "feature-z")
	return repoRoot
}

func runGitForCheckoutHandlerTests(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func TestHandleWorkspaceFork_WithFactoryUsesRuntimeCheckpointID(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	parent, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("seed workspace failed: %v", err)
	}

	const checkpointID = "snap-parent-to-child-1"
	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{
			"firecracker": &mockDriver{
				backend: "firecracker",
				checkpointForkFn: func(_ context.Context, workspaceID, childWorkspaceID string) (string, error) {
					if workspaceID != parent.ID {
						t.Fatalf("expected parent id %q, got %q", parent.ID, workspaceID)
					}
					if strings.TrimSpace(childWorkspaceID) == "" {
						t.Fatal("expected non-empty child workspace id")
					}
					return checkpointID, nil
				},
			},
		},
	)

	result, rpcErr := HandleWorkspaceFork(context.Background(), WorkspaceForkParams{
		ID:                 parent.ID,
		ChildWorkspaceName: "alpha-child",
		ChildRef:           "alpha-child",
	}, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace in fork result, got %#v", result)
	}
	if result.Workspace.LineageSnapshotID != checkpointID {
		t.Fatalf("expected lineage snapshot %q, got %q", checkpointID, result.Workspace.LineageSnapshotID)
	}
}

func TestHandleWorkspaceFork_UsesProjectRootSandboxAsForkSource(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	projMgr := projectmgr.NewManager(mgrRoot, mgr.ProjectRepository())
	mgr.SetProjectManager(projMgr)

	project, err := projMgr.GetOrCreateForRepo("git@example/repo.git", "repo-test")
	if err != nil {
		t.Fatalf("seed project failed: %v", err)
	}
	rootWS, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "root",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("create root ws: %v", err)
	}
	if err := mgr.UpdateProjectID(rootWS.ID, project.ID); err != nil {
		t.Fatalf("update root project id: %v", err)
	}
	featureWS, err := mgr.Fork(rootWS.ID, "feature", "feature-a")
	if err != nil {
		t.Fatalf("create feature ws via fork: %v", err)
	}

	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{
			"firecracker": &mockDriver{
				backend: "firecracker",
				checkpointForkFn: func(_ context.Context, workspaceID, childWorkspaceID string) (string, error) {
					if workspaceID != rootWS.ID {
						t.Fatalf("expected runtime fork source to be root workspace %q, got %q", rootWS.ID, workspaceID)
					}
					if strings.TrimSpace(childWorkspaceID) == "" {
						t.Fatal("expected child workspace id")
					}
					return "snap-root-fork", nil
				},
			},
		},
	)

	result, rpcErr := HandleWorkspaceFork(context.Background(), WorkspaceForkParams{
		ID:                 featureWS.ID,
		ChildWorkspaceName: "child",
		ChildRef:           "child-ref",
	}, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected child workspace result, got %#v", result)
	}
	if result.Workspace.ParentWorkspaceID != rootWS.ID {
		t.Fatalf("expected child parent workspace %q, got %q", rootWS.ID, result.Workspace.ParentWorkspaceID)
	}
}

func TestHandleWorkspaceFork_AllowsExplicitSourceWorkspaceOverride(t *testing.T) {
	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	projMgr := projectmgr.NewManager(mgrRoot, mgr.ProjectRepository())
	mgr.SetProjectManager(projMgr)

	project, err := projMgr.GetOrCreateForRepo("git@example/repo.git", "repo-test")
	if err != nil {
		t.Fatalf("seed project failed: %v", err)
	}
	rootWS, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "root",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("create root ws: %v", err)
	}
	if err := mgr.UpdateProjectID(rootWS.ID, project.ID); err != nil {
		t.Fatalf("update root project id: %v", err)
	}
	featureWS, err := mgr.Fork(rootWS.ID, "feature", "feature-a")
	if err != nil {
		t.Fatalf("create feature ws via fork: %v", err)
	}

	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{
			"firecracker": &mockDriver{
				backend: "firecracker",
				checkpointForkFn: func(_ context.Context, workspaceID, childWorkspaceID string) (string, error) {
					if workspaceID != featureWS.ID {
						t.Fatalf("expected explicit runtime fork source %q, got %q", featureWS.ID, workspaceID)
					}
					if strings.TrimSpace(childWorkspaceID) == "" {
						t.Fatal("expected child workspace id")
					}
					return "snap-explicit-fork", nil
				},
			},
		},
	)

	result, rpcErr := HandleWorkspaceFork(context.Background(), WorkspaceForkParams{
		ID:                 rootWS.ID,
		SourceWorkspaceID:  featureWS.ID,
		ChildWorkspaceName: "child",
		ChildRef:           "child-ref",
	}, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected child workspace result, got %#v", result)
	}
	if result.Workspace.ParentWorkspaceID != featureWS.ID {
		t.Fatalf("expected child parent workspace %q, got %q", featureWS.ID, result.Workspace.ParentWorkspaceID)
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
	result, rpcErr := HandleWorkspaceCreate(context.Background(), createParams, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected create failure with registered backend driver: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil {
		t.Fatalf("expected workspace create result, got %#v", result)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected backend firecracker, got %q", result.Workspace.Backend)
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

func TestHandleWorkspaceCreate_UsesPreferredLineageSnapshotForRuntimeCreate(t *testing.T) {
	t.Cleanup(selection.ResetRuntimeSetupRunnerForTest)

	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	parent, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          repo,
		Ref:           "main",
		WorkspaceName: "base",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("seed workspace failed: %v", err)
	}
	if err := mgr.SetLineageSnapshot(parent.ID, "snap-base-1"); err != nil {
		t.Fatalf("set lineage snapshot failed: %v", err)
	}

	var gotSnapshotID string
	factory := runtime.NewFactory([]runtime.Capability{{Name: "runtime.linux", Available: true}, {Name: "runtime.firecracker", Available: true}}, map[string]runtime.Driver{
		"firecracker": &mockDriver{
			backend: "firecracker",
			createFn: func(ctx context.Context, req runtime.CreateRequest) error {
				gotSnapshotID = strings.TrimSpace(req.Options["lineage_snapshot_id"])
				return nil
			},
		},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "feature-snapshot-child",
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
	if gotSnapshotID != "snap-base-1" {
		t.Fatalf("expected runtime create to receive snapshot %q, got %q", "snap-base-1", gotSnapshotID)
	}
}

func TestHandleWorkspaceCreate_AutoCapturesBaselineSnapshotForFirstSandbox(t *testing.T) {
	t.Cleanup(selection.ResetRuntimeSetupRunnerForTest)

	mgrRoot := t.TempDir()
	mgr := workspacemgr.NewManager(mgrRoot)
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	const baselineSnapshotID = "snap-baseline-auto-1"
	var gotCreateSnapshotID string
	var checkpointCalls int
	factory := runtime.NewFactory([]runtime.Capability{{Name: "runtime.linux", Available: true}, {Name: "runtime.firecracker", Available: true}}, map[string]runtime.Driver{
		"firecracker": &mockDriver{
			backend: "firecracker",
			createFn: func(ctx context.Context, req runtime.CreateRequest) error {
				gotCreateSnapshotID = strings.TrimSpace(req.Options["lineage_snapshot_id"])
				return nil
			},
			checkpointForkFn: func(ctx context.Context, workspaceID, childWorkspaceID string) (string, error) {
				checkpointCalls++
				if strings.TrimSpace(workspaceID) == "" {
					t.Fatal("expected non-empty workspaceID for baseline checkpoint")
				}
				if workspaceID != childWorkspaceID {
					t.Fatalf("expected baseline checkpoint to use same workspace id, got parent=%q child=%q", workspaceID, childWorkspaceID)
				}
				return baselineSnapshotID, nil
			},
		},
	})

	params := WorkspaceCreateParams{
		Spec: workspacemgr.CreateSpec{
			Repo:          repo,
			Ref:           "feature-first",
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
	if gotCreateSnapshotID != "" {
		t.Fatalf("expected first create to run without prior snapshot id, got %q", gotCreateSnapshotID)
	}
	if checkpointCalls != 1 {
		t.Fatalf("expected one baseline checkpoint call, got %d", checkpointCalls)
	}
	if result.UsedLineageSnapshotID != baselineSnapshotID {
		t.Fatalf("expected used lineage snapshot %q, got %q", baselineSnapshotID, result.UsedLineageSnapshotID)
	}
	if result.Workspace.LineageSnapshotID != baselineSnapshotID {
		t.Fatalf("expected workspace lineage snapshot %q, got %q", baselineSnapshotID, result.Workspace.LineageSnapshotID)
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

func TestHandleWorkspaceCreate_UnsupportedNestedVirtSelectsPlatformBackend(t *testing.T) {
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
	expectedBackend := "seatbelt"
	if goruntime.GOOS == "darwin" {
		expectedBackend = "firecracker"
	}
	if result.Workspace.Backend != expectedBackend {
		t.Fatalf("expected %s backend, got %q", expectedBackend, result.Workspace.Backend)
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
	expectedBackend := "seatbelt"
	if goruntime.GOOS == "darwin" {
		expectedBackend = "firecracker"
	}
	if result.Workspace.Backend != expectedBackend {
		t.Fatalf("expected %s backend from override, got %q", expectedBackend, result.Workspace.Backend)
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
