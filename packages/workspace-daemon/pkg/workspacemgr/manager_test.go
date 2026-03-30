package workspacemgr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(t.TempDir())
}

func TestManager_CreateWorkspace_InitialState(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if ws.State != StateSetup {
		t.Fatalf("expected state %q, got %q", StateSetup, ws.State)
	}
}

func TestManager_CreateWorkspace_AssignsRootPath(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if ws.RootPath == "" {
		t.Fatal("expected non-empty root path")
	}
	wantPrefix := filepath.Join(m.root, "instances")
	if len(ws.RootPath) < len(wantPrefix) || ws.RootPath[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("expected root path with prefix %q, got %q", wantPrefix, ws.RootPath)
	}
	if _, err := os.Stat(ws.RootPath); err != nil {
		t.Fatalf("expected workspace root to exist: %v", err)
	}
}

func TestManager_RemoveWorkspace_DeletesRoot(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	if !m.Remove(ws.ID) {
		t.Fatal("expected remove to return true")
	}

	if _, err := os.Stat(ws.RootPath); !os.IsNotExist(err) {
		t.Fatalf("expected workspace root to be removed, got err=%v", err)
	}
}
