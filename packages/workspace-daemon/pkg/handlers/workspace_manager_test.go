package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/workspacemgr"
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

	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result == nil || result.Workspace == nil || result.Workspace.ID == "" {
		t.Fatalf("expected workspace with id, got %#v", result)
	}
}

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

	created, rpcErr := HandleWorkspaceCreate(context.Background(), createParams, mgr)
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
