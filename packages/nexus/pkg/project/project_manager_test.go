package project_test

import (
	"context"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/project"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type wsMgrAdapter struct{ mgr *workspacemgr.Manager }

func (a wsMgrAdapter) ListEntries() []project.WorkspaceEntry {
	all := a.mgr.List()
	entries := make([]project.WorkspaceEntry, 0, len(all))
	for _, ws := range all {
		entries = append(entries, project.WorkspaceEntry{ID: ws.ID, ProjectID: ws.ProjectID})
	}
	return entries
}

func (a wsMgrAdapter) RemoveWithID(id string) error {
	_, err := a.mgr.RemoveWithOptions(id, workspacemgr.RemoveOptions{DeleteHostPath: false})
	return err
}

func TestHandleProjectCreateAndList(t *testing.T) {
	root := t.TempDir()
	wsMgr := workspacemgr.NewManager(root)
	projMgr := project.NewManager(root, wsMgr.ProjectRepository())
	wsMgr.SetProjectManager(projMgr)

	createResult, rpcErr := project.HandleProjectCreate(context.Background(), project.ProjectCreateParams{Repo: "git@example/repo.git"}, projMgr)
	if rpcErr != nil {
		t.Fatalf("unexpected create rpc error: %+v", rpcErr)
	}
	if createResult == nil || createResult.Project == nil || createResult.Project.ID == "" {
		t.Fatalf("expected created project, got %#v", createResult)
	}

	listResult, rpcErr := project.HandleProjectList(context.Background(), project.ProjectListParams{}, projMgr)
	if rpcErr != nil {
		t.Fatalf("unexpected list rpc error: %+v", rpcErr)
	}
	if len(listResult.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(listResult.Projects))
	}
}

func TestHandleProjectGetIncludesWorkspaces(t *testing.T) {
	root := t.TempDir()
	wsMgr := workspacemgr.NewManager(root)
	projMgr := project.NewManager(root, wsMgr.ProjectRepository())
	wsMgr.SetProjectManager(projMgr)

	p, err := projMgr.GetOrCreateForRepo("git@example/repo.git", "repo-test")
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := wsMgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	getResult, rpcErr := project.HandleProjectGet(context.Background(), project.ProjectGetParams{ID: p.ID}, projMgr, wsMgrAdapter{wsMgr})
	if rpcErr != nil {
		t.Fatalf("unexpected get rpc error: %+v", rpcErr)
	}
	if getResult == nil || getResult.Project == nil {
		t.Fatalf("expected project get result, got %#v", getResult)
	}
	if len(getResult.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace under project, got %d", len(getResult.Workspaces))
	}
}

func TestHandleProjectRemove_RemovesProject(t *testing.T) {
	root := t.TempDir()
	wsMgr := workspacemgr.NewManager(root)
	projMgr := project.NewManager(root, wsMgr.ProjectRepository())
	wsMgr.SetProjectManager(projMgr)

	created, rpcErr := project.HandleProjectCreate(context.Background(), project.ProjectCreateParams{Repo: "git@example/repo-remove.git"}, projMgr)
	if rpcErr != nil {
		t.Fatalf("create project: %+v", rpcErr)
	}
	if created == nil || created.Project == nil {
		t.Fatalf("expected created project, got %#v", created)
	}

	removeResult, rpcErr := project.HandleProjectRemove(context.Background(), project.ProjectRemoveParams{ID: created.Project.ID}, projMgr, wsMgrAdapter{wsMgr})
	if rpcErr != nil {
		t.Fatalf("remove project: %+v", rpcErr)
	}
	if removeResult == nil || !removeResult.Removed {
		t.Fatalf("expected removed=true, got %#v", removeResult)
	}
	if _, ok := projMgr.Get(created.Project.ID); ok {
		t.Fatal("expected project to be removed from manager")
	}
}

func TestHandleProjectList_ReturnsDeterministicOrder(t *testing.T) {
	root := t.TempDir()
	wsMgr := workspacemgr.NewManager(root)
	projMgr := project.NewManager(root, wsMgr.ProjectRepository())
	wsMgr.SetProjectManager(projMgr)

	first, rpcErr := project.HandleProjectCreate(context.Background(), project.ProjectCreateParams{Repo: "git@example/alpha.git"}, projMgr)
	if rpcErr != nil {
		t.Fatalf("create first project: %+v", rpcErr)
	}
	time.Sleep(2 * time.Millisecond)
	second, rpcErr := project.HandleProjectCreate(context.Background(), project.ProjectCreateParams{Repo: "git@example/bravo.git"}, projMgr)
	if rpcErr != nil {
		t.Fatalf("create second project: %+v", rpcErr)
	}

	listResult, rpcErr := project.HandleProjectList(context.Background(), project.ProjectListParams{}, projMgr)
	if rpcErr != nil {
		t.Fatalf("project list: %+v", rpcErr)
	}
	if len(listResult.Projects) < 2 {
		t.Fatalf("expected at least 2 projects, got %d", len(listResult.Projects))
	}
	if listResult.Projects[0].ID != first.Project.ID || listResult.Projects[1].ID != second.Project.ID {
		t.Fatalf("expected deterministic creation order (%s, %s), got (%s, %s)",
			first.Project.ID, second.Project.ID, listResult.Projects[0].ID, listResult.Projects[1].ID)
	}
}
