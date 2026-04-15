package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestHandleProjectCreateAndList(t *testing.T) {
	root := t.TempDir()
	wsMgr := workspacemgr.NewManager(root)
	projMgr := projectmgr.NewManager(root, wsMgr.ProjectRepository())
	wsMgr.SetProjectManager(projMgr)

	createResult, rpcErr := HandleProjectCreate(context.Background(), ProjectCreateParams{Repo: "git@example/repo.git"}, projMgr)
	if rpcErr != nil {
		t.Fatalf("unexpected create rpc error: %+v", rpcErr)
	}
	if createResult == nil || createResult.Project == nil || createResult.Project.ID == "" {
		t.Fatalf("expected created project, got %#v", createResult)
	}

	listResult, rpcErr := HandleProjectList(context.Background(), ProjectListParams{}, projMgr)
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
	projMgr := projectmgr.NewManager(root, wsMgr.ProjectRepository())
	wsMgr.SetProjectManager(projMgr)

	project, err := projMgr.GetOrCreateForRepo("git@example/repo.git", "repo-test")
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

	getResult, rpcErr := HandleProjectGet(context.Background(), ProjectGetParams{ID: project.ID}, projMgr, wsMgr)
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
	projMgr := projectmgr.NewManager(root, wsMgr.ProjectRepository())
	wsMgr.SetProjectManager(projMgr)

	created, rpcErr := HandleProjectCreate(context.Background(), ProjectCreateParams{Repo: "git@example/repo-remove.git"}, projMgr)
	if rpcErr != nil {
		t.Fatalf("create project: %+v", rpcErr)
	}
	if created == nil || created.Project == nil {
		t.Fatalf("expected created project, got %#v", created)
	}

	removeResult, rpcErr := HandleProjectRemove(context.Background(), ProjectRemoveParams{ID: created.Project.ID}, projMgr, wsMgr)
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
	projMgr := projectmgr.NewManager(root, wsMgr.ProjectRepository())
	wsMgr.SetProjectManager(projMgr)

	first, rpcErr := HandleProjectCreate(context.Background(), ProjectCreateParams{Repo: "git@example/alpha.git"}, projMgr)
	if rpcErr != nil {
		t.Fatalf("create first project: %+v", rpcErr)
	}
	time.Sleep(2 * time.Millisecond)
	second, rpcErr := HandleProjectCreate(context.Background(), ProjectCreateParams{Repo: "git@example/bravo.git"}, projMgr)
	if rpcErr != nil {
		t.Fatalf("create second project: %+v", rpcErr)
	}

	listResult, rpcErr := HandleProjectList(context.Background(), ProjectListParams{}, projMgr)
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
