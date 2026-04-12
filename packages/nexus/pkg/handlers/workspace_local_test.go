package handlers

import (
	"context"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestHandleWorkspaceSetLocalWorktree_ClearsDuplicateMutagenSessionID(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())

	first, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo-one.git",
		WorkspaceName: "one",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create first workspace: %v", err)
	}
	second, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo-two.git",
		WorkspaceName: "two",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create second workspace: %v", err)
	}

	if err := mgr.SetLocalWorktree(first.ID, "/tmp/worktree-one", "mutagen-shared"); err != nil {
		t.Fatalf("seed first mutagen session: %v", err)
	}

	_, rpcErr := HandleWorkspaceSetLocalWorktree(context.Background(), WorkspaceSetLocalWorktreeParams{
		ID:                second.ID,
		LocalWorktreePath: "/tmp/worktree-two",
		MutagenSessionID:  "mutagen-shared",
	}, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}

	updatedFirst, ok := mgr.Get(first.ID)
	if !ok {
		t.Fatal("expected first workspace")
	}
	if updatedFirst.MutagenSessionID != "" {
		t.Fatalf("expected first mutagen session to be cleared, got %q", updatedFirst.MutagenSessionID)
	}

	updatedSecond, ok := mgr.Get(second.ID)
	if !ok {
		t.Fatal("expected second workspace")
	}
	if updatedSecond.MutagenSessionID != "mutagen-shared" {
		t.Fatalf("expected second mutagen session to be set, got %q", updatedSecond.MutagenSessionID)
	}
}
