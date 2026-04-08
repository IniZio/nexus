package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestHandleWorkspaceRelationsList_GroupsByRepoAndLineage(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())

	parent, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@github.com:IniZio/hanlun-lms.git",
		Ref:           "main",
		WorkspaceName: "hanlun-main",
		AgentProfile:  "default",
		Backend:       "local",
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child, err := mgr.Fork(parent.ID, "hanlun-feature", "hanlun-feature")
	if err != nil {
		t.Fatalf("fork child: %v", err)
	}
	if err := mgr.SetLocalWorktree(child.ID, "/tmp/worktrees/hanlun-feature", "mutagen-hanlun"); err != nil {
		t.Fatalf("set local worktree: %v", err)
	}

	_, err = mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "./repos/local-only-relations-test",
		Ref:           "dev",
		WorkspaceName: "local-only",
		AgentProfile:  "default",
		Backend:       "local",
	})
	if err != nil {
		t.Fatalf("create local-only: %v", err)
	}

	res, rpcErr := HandleWorkspaceRelationsList(context.Background(), nil, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if len(res.Relations) != 2 {
		t.Fatalf("expected 2 repo groups, got %d", len(res.Relations))
	}

	var hosted *WorkspaceRelationsGroup
	for i := range res.Relations {
		if res.Relations[i].RepoKind == "hosted" {
			hosted = &res.Relations[i]
			break
		}
	}
	if hosted == nil {
		t.Fatal("expected hosted repo group")
	}
	if hosted.RemoteURL == "" {
		t.Fatal("expected hosted group to include remoteUrl")
	}
	if len(hosted.Nodes) != 2 {
		t.Fatalf("expected 2 nodes in hosted group, got %d", len(hosted.Nodes))
	}
	if len(hosted.LineageRoots) != 1 {
		t.Fatalf("expected 1 lineage root in hosted group, got %d", len(hosted.LineageRoots))
	}
}

func TestHandleWorkspaceRelationsList_FilterByRepoID(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())

	first, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@github.com:IniZio/one.git",
		Ref:           "main",
		WorkspaceName: "one",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	_, err = mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@github.com:IniZio/two.git",
		Ref:           "main",
		WorkspaceName: "two",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	params, _ := json.Marshal(WorkspaceRelationsListParams{RepoID: first.RepoID})
	res, rpcErr := HandleWorkspaceRelationsList(context.Background(), params, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if len(res.Relations) != 1 {
		t.Fatalf("expected 1 relation group, got %d", len(res.Relations))
	}
	if res.Relations[0].RepoID != first.RepoID {
		t.Fatalf("expected repo id %q, got %q", first.RepoID, res.Relations[0].RepoID)
	}
}
