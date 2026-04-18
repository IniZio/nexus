package workspacemgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNodeStorePathForRoot_UsesTempScopedDBForTmpSymlinkPath(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "state-home", "nexus", "node.db")
	target := t.TempDir()

	link := filepath.Join("/tmp", fmt.Sprintf("nexus-link-%d", time.Now().UnixNano()))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink setup unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	cleanLink := filepath.Clean(link)
	cleanTemp := filepath.Clean(os.TempDir())
	if strings.HasPrefix(cleanLink+string(filepath.Separator), cleanTemp+string(filepath.Separator)) {
		t.Skip("raw path already under tempdir; canonicalization case not applicable")
	}

	resolvedLink, err := filepath.EvalSymlinks(cleanLink)
	if err != nil {
		t.Skipf("cannot resolve link path: %v", err)
	}
	resolvedTemp, err := filepath.EvalSymlinks(cleanTemp)
	if err != nil {
		t.Skipf("cannot resolve tempdir path: %v", err)
	}
	if !strings.HasPrefix(resolvedLink+string(filepath.Separator), resolvedTemp+string(filepath.Separator)) {
		t.Skip("resolved link path is not under resolved tempdir on this host")
	}

	got := nodeStorePathForRoot(link, defaultPath)
	want := filepath.Join(cleanLink, ".nexus", "state", "node.db")
	if got != want {
		t.Fatalf("expected temp-scoped db path %q, got %q", want, got)
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state-home"))
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
	if ws.State != StateCreated {
		t.Fatalf("expected state %q, got %q", StateCreated, ws.State)
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

func TestManager_CreateWorkspace_UsesDeterministicHostWorkspacePathForLocalRepo(t *testing.T) {
	m := newTestManager(t)
	repoRoot := initLocalRepoDirForWorkspaceTests(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	wantPrefix := filepath.Join(repoRoot, ".worktrees") + string(filepath.Separator)
	if !strings.HasPrefix(ws.LocalWorktreePath, wantPrefix) {
		t.Fatalf("expected local workspace path prefix %q, got %q", wantPrefix, ws.LocalWorktreePath)
	}
	if gotBase := filepath.Base(ws.LocalWorktreePath); gotBase != "main" {
		t.Fatalf("expected branch-based host path segment %q, got %q", "main", gotBase)
	}
	if _, statErr := os.Stat(ws.LocalWorktreePath); statErr != nil {
		t.Fatalf("expected local workspace path to exist: %v", statErr)
	}
	if !HasValidHostWorkspaceMarker(ws.LocalWorktreePath, ws.ID) {
		t.Fatalf("expected host workspace marker for %q", ws.ID)
	}
	if _, readErr := os.ReadFile(filepath.Join(ws.LocalWorktreePath, "README.md")); readErr != nil {
		t.Fatalf("expected git checkout materialized in host workspace path: %v", readErr)
	}
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	gitignoreData, readErr := os.ReadFile(gitignorePath)
	if readErr != nil {
		t.Fatalf("read repo .gitignore: %v", readErr)
	}
	if !strings.Contains(string(gitignoreData), ".worktrees/") {
		t.Fatalf("expected repo .gitignore to include .worktrees/, got %q", string(gitignoreData))
	}
}

func TestManager_CreateWorkspace_UsesBranchNameForHostWorkspacePath(t *testing.T) {
	m := newTestManager(t)
	repoRoot := initLocalRepoDirForWorkspaceTests(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "feature/auth-flow",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if got := filepath.Base(ws.LocalWorktreePath); got != "feature-auth-flow" {
		t.Fatalf("expected sanitized branch dir name %q, got %q", "feature-auth-flow", got)
	}
}

func TestManager_CreateWorkspace_ProjectRootUsesRepoPath(t *testing.T) {
	m := newTestManager(t)
	repoRoot := initLocalRepoDirForWorkspaceTests(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:               repoRoot,
		Ref:                "main",
		WorkspaceName:      "base",
		AgentProfile:       "default",
		UseProjectRootPath: true,
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if ws.LocalWorktreePath != repoRoot {
		t.Fatalf("expected project root host path %q, got %q", repoRoot, ws.LocalWorktreePath)
	}
	if ws.HostWorkspacePath != repoRoot {
		t.Fatalf("expected project root canonical host path %q, got %q", repoRoot, ws.HostWorkspacePath)
	}
	if HasValidHostWorkspaceMarker(repoRoot, ws.ID) {
		t.Fatalf("did not expect marker file in repo root for project base workspace")
	}
}

func TestManager_LoadAll_NormalizesLegacyLocalWorktreePath(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root)
	repoRoot := initLocalRepoDirForWorkspaceTests(t)

	ws, err := mgr.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	legacyPath := filepath.Join(repoRoot, ".nexus", "workspaces", ws.ID)
	if err := mgr.SetLocalWorktree(ws.ID, legacyPath, ""); err != nil {
		t.Fatalf("set legacy local worktree path: %v", err)
	}

	reloaded := NewManager(root)
	wsReloaded, ok := reloaded.Get(ws.ID)
	if !ok {
		t.Fatalf("expected workspace %s after reload", ws.ID)
	}
	want := filepath.Join(repoRoot, ".worktrees", "main")
	if wsReloaded.LocalWorktreePath != want {
		t.Fatalf("expected legacy path normalization to %q, got %q", want, wsReloaded.LocalWorktreePath)
	}
}

func TestManager_ForkCopiesDirtyParentStateToChildHostWorkspace(t *testing.T) {
	m := newTestManager(t)
	repoRoot := initLocalRepoDirForWorkspaceTests(t)

	parent, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create parent returned error: %v", err)
	}

	trackedFile := filepath.Join(parent.LocalWorktreePath, "README.md")
	if err := os.WriteFile(trackedFile, []byte("# dirty parent\n"), 0o644); err != nil {
		t.Fatalf("write tracked dirty file: %v", err)
	}
	untrackedFile := filepath.Join(parent.LocalWorktreePath, "local-only.txt")
	if err := os.WriteFile(untrackedFile, []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	child, err := m.Fork(parent.ID, "alpha-child", "feature-dirty")
	if err != nil {
		t.Fatalf("fork returned error: %v", err)
	}

	trackedChildData, readErr := os.ReadFile(filepath.Join(child.LocalWorktreePath, "README.md"))
	if readErr != nil {
		t.Fatalf("read child tracked file: %v", readErr)
	}
	if string(trackedChildData) != "# dirty parent\n" {
		t.Fatalf("expected child tracked changes to carry over, got %q", string(trackedChildData))
	}
	untrackedChildData, readErr := os.ReadFile(filepath.Join(child.LocalWorktreePath, "local-only.txt"))
	if readErr != nil {
		t.Fatalf("read child untracked file: %v", readErr)
	}
	if string(untrackedChildData) != "untracked\n" {
		t.Fatalf("expected child untracked file to carry over, got %q", string(untrackedChildData))
	}
}

func TestManager_CopyDirtyStateFromWorkspace(t *testing.T) {
	m := newTestManager(t)
	repoRoot := initLocalRepoDirForWorkspaceTests(t)

	source, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "main",
		WorkspaceName: "base",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create source returned error: %v", err)
	}
	target, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "feature-copy",
		WorkspaceName: "child",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create target returned error: %v", err)
	}

	if err := os.WriteFile(filepath.Join(source.LocalWorktreePath, "README.md"), []byte("# YOLO\n"), 0o644); err != nil {
		t.Fatalf("write source tracked dirty file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source.LocalWorktreePath, "YOLO"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("write source untracked file: %v", err)
	}

	if err := m.CopyDirtyStateFromWorkspace(source.ID, target.ID); err != nil {
		t.Fatalf("copy dirty state: %v", err)
	}

	trackedData, err := os.ReadFile(filepath.Join(target.LocalWorktreePath, "README.md"))
	if err != nil {
		t.Fatalf("read target tracked file: %v", err)
	}
	if string(trackedData) != "# YOLO\n" {
		t.Fatalf("expected tracked content propagated, got %q", string(trackedData))
	}
	untrackedData, err := os.ReadFile(filepath.Join(target.LocalWorktreePath, "YOLO"))
	if err != nil {
		t.Fatalf("read target untracked file: %v", err)
	}
	if string(untrackedData) != "untracked\n" {
		t.Fatalf("expected untracked content propagated, got %q", string(untrackedData))
	}
}

func TestManager_CreateWorkspace_RejectsDuplicateBranchPerRepo(t *testing.T) {
	m := newTestManager(t)
	spec := CreateSpec{
		Repo:          "git@example/repo.git",
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	}
	if _, err := m.Create(context.Background(), spec); err != nil {
		t.Fatalf("first create returned error: %v", err)
	}
	_, err := m.Create(context.Background(), CreateSpec{
		Repo:          spec.Repo,
		Ref:           spec.Ref,
		WorkspaceName: "beta",
		AgentProfile:  "default",
	})
	if err == nil {
		t.Fatal("expected duplicate branch create to fail")
	}
	if !strings.Contains(err.Error(), "workspace already exists for branch") {
		t.Fatalf("expected duplicate branch error, got %v", err)
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

func TestManager_StopRestorePersistsState(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	if err := m.Stop(ws.ID); err != nil {
		t.Fatalf("stop returned error: %v", err)
	}

	m2 := NewManager(m.Root())
	r, ok := m2.Restore(ws.ID)
	if !ok {
		t.Fatal("expected restore to return true")
	}
	if r.State != StateRestored {
		t.Fatalf("expected state %q, got %q", StateRestored, r.State)
	}
}

func TestManager_StopPersistsStateAcrossReload(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	if err := m.Stop(ws.ID); err != nil {
		t.Fatalf("stop returned error: %v", err)
	}

	m2 := NewManager(m.Root())
	got, ok := m2.Get(ws.ID)
	if !ok {
		t.Fatal("expected to get workspace after reload")
	}
	if got.State != StateStopped {
		t.Fatalf("expected state %q after reload, got %q", StateStopped, got.State)
	}
}

func TestManager_RemovePersistsRecordDeletion(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	id := ws.ID
	m.Remove(id)

	m2 := NewManager(m.Root())
	_, ok := m2.Get(id)
	if ok {
		t.Fatal("expected workspace to be gone after remove and reload")
	}
}

func TestManager_LoadAll_IgnoresLegacyJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state-home"))

	if err := os.WriteFile(filepath.Join(root, ".nexus"), []byte("block sqlite dir"), 0o644); err != nil {
		t.Fatalf("write sqlite blocker file: %v", err)
	}

	legacyDir := filepath.Join(root, "workspaces")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}

	legacy := Workspace{
		ID:            "ws-legacy",
		Repo:          "git@example/legacy.git",
		WorkspaceName: "legacy",
		AgentProfile:  "default",
		State:         StateCreated,
		RootPath:      filepath.Join(root, "instances", "ws-legacy"),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "ws-legacy.json"), data, 0o644); err != nil {
		t.Fatalf("write legacy workspace json: %v", err)
	}

	m := NewManager(root)
	if _, ok := m.Get("ws-legacy"); ok {
		t.Fatal("expected manager to ignore legacy workspace json files")
	}
}

func TestManager_CreateFailsWhenSQLiteStoreUnavailable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state-home"))

	if err := os.WriteFile(filepath.Join(root, ".nexus"), []byte("block sqlite dir"), 0o644); err != nil {
		t.Fatalf("write sqlite blocker file: %v", err)
	}

	m := NewManager(root)

	_, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err == nil {
		t.Fatal("expected create to fail when sqlite store is unavailable")
	}
}

func TestManager_CreateRollbackOnPersistFailure_RemovesCreateSideEffects(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state-home"))

	if err := os.WriteFile(filepath.Join(root, ".nexus"), []byte("block sqlite dir"), 0o644); err != nil {
		t.Fatalf("write sqlite blocker file: %v", err)
	}

	m := NewManager(root)
	repoRoot := initGitRepoForWorktreeTests(t)

	_, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err == nil {
		t.Fatal("expected create to fail when sqlite store is unavailable")
	}

	if got := len(m.List()); got != 0 {
		t.Fatalf("expected no workspaces in manager after failed create, got %d", got)
	}

	instancesDir := filepath.Join(root, "instances")
	entries, readErr := os.ReadDir(instancesDir)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read instances dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no workspace roots after failed create, got %d entries", len(entries))
	}

	worktreeEntries, readErr := os.ReadDir(filepath.Join(repoRoot, ".worktrees"))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read local workspace dir: %v", readErr)
	}
	if len(worktreeEntries) != 0 {
		t.Fatalf("expected no local workspace paths after failed create, got %d entries", len(worktreeEntries))
	}
}

func TestManager_ListWorkspaces_PersistedAcrossReload(t *testing.T) {
	m := newTestManager(t)
	_, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	_, err = m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo2.git",
		WorkspaceName: "beta",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	m2 := NewManager(m.Root())
	wsList := m2.List()
	if len(wsList) != 2 {
		t.Fatalf("expected 2 workspaces after reload, got %d", len(wsList))
	}
}

func TestManager_StartTransitionsToRunning(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if ws.State != StateCreated {
		t.Fatalf("expected initial state %q, got %q", StateCreated, ws.State)
	}

	if err := m.Start(ws.ID); err != nil {
		t.Fatalf("start returned error: %v", err)
	}

	got, ok := m.Get(ws.ID)
	if !ok {
		t.Fatal("expected to get workspace after start")
	}
	if got.State != StateRunning {
		t.Fatalf("expected state %q after start, got %q", StateRunning, got.State)
	}
}

func TestManager_StartPersistsAcrossReload(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	if err := m.Start(ws.ID); err != nil {
		t.Fatalf("start returned error: %v", err)
	}

	m2 := NewManager(m.Root())
	got, ok := m2.Get(ws.ID)
	if !ok {
		t.Fatal("expected to get workspace after reload")
	}
	if got.State != StateRunning {
		t.Fatalf("expected state %q after reload, got %q", StateRunning, got.State)
	}
}

func TestManager_CheckoutUpdatesRefAndPersists(t *testing.T) {
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

	updated, err := m.Checkout(ws.ID, "feature-a")
	if err != nil {
		t.Fatalf("checkout returned error: %v", err)
	}
	if updated.Ref != "feature-a" {
		t.Fatalf("expected ref %q after checkout, got %q", "feature-a", updated.Ref)
	}
	if updated.TargetBranch != "feature-a" || updated.CurrentRef != "feature-a" {
		t.Fatalf("expected target/current ref to track checkout branch, got target=%q current=%q", updated.TargetBranch, updated.CurrentRef)
	}

	reloaded := NewManager(m.Root())
	reloadedWS, ok := reloaded.Get(ws.ID)
	if !ok {
		t.Fatal("expected workspace after reload")
	}
	if reloadedWS.Ref != "feature-a" {
		t.Fatalf("expected persisted ref %q, got %q", "feature-a", reloadedWS.Ref)
	}
	if reloadedWS.TargetBranch != "feature-a" || reloadedWS.CurrentRef != "feature-a" {
		t.Fatalf("expected persisted target/current refs feature-a, got target=%q current=%q", reloadedWS.TargetBranch, reloadedWS.CurrentRef)
	}
}

func TestManager_CheckoutRejectsDuplicateBranchPerRepo(t *testing.T) {
	m := newTestManager(t)
	repo := "git@example/repo.git"
	first, err := m.Create(context.Background(), CreateSpec{
		Repo:          repo,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create first returned error: %v", err)
	}
	if _, err := m.Create(context.Background(), CreateSpec{
		Repo:          repo,
		Ref:           "feature-a",
		WorkspaceName: "beta",
		AgentProfile:  "default",
	}); err != nil {
		t.Fatalf("create second returned error: %v", err)
	}

	_, err = m.Checkout(first.ID, "feature-a")
	if err == nil {
		t.Fatal("expected checkout duplicate branch to fail")
	}
	if !strings.Contains(err.Error(), "workspace already exists for branch") {
		t.Fatalf("expected duplicate branch error, got %v", err)
	}
}

func TestManager_CanCheckoutRejectsDuplicateBranchPerRepo(t *testing.T) {
	m := newTestManager(t)
	repo := "git@example/repo.git"
	first, err := m.Create(context.Background(), CreateSpec{
		Repo:          repo,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create first returned error: %v", err)
	}
	if _, err := m.Create(context.Background(), CreateSpec{
		Repo:          repo,
		Ref:           "feature-a",
		WorkspaceName: "beta",
		AgentProfile:  "default",
	}); err != nil {
		t.Fatalf("create second returned error: %v", err)
	}

	if err := m.CanCheckout(first.ID, "feature-a"); err == nil {
		t.Fatal("expected duplicate branch precheck to fail")
	}
}

func TestManager_CreateWorkspace_WithBackendAndAuthBinding(t *testing.T) {
	m := newTestManager(t)
	spec := CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "test-backend",
		AuthBinding: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}
	ws, err := m.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if ws.Backend != spec.Backend {
		t.Fatalf("expected backend %q, got %q", spec.Backend, ws.Backend)
	}
	if len(ws.AuthBinding) != len(spec.AuthBinding) {
		t.Fatalf("expected %d auth bindings, got %d", len(spec.AuthBinding), len(ws.AuthBinding))
	}
	for k, v := range spec.AuthBinding {
		if ws.AuthBinding[k] != v {
			t.Fatalf("expected auth binding %q=%q, got %q", k, v, ws.AuthBinding[k])
		}
	}
}

func TestManager_CreateWorkspace_AuthBindingDefaultEmpty(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if ws.AuthBinding == nil {
		t.Fatal("expected non-nil auth binding map")
	}
	if len(ws.AuthBinding) != 0 {
		t.Fatalf("expected empty auth binding, got %d entries", len(ws.AuthBinding))
	}
}

func TestManager_CloneWorkspace_AvoidsAuthProfilesSliceAliasing(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Policy: Policy{
			AuthProfiles: []AuthProfile{AuthProfileGitCfg},
		},
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	got, _ := m.Get(ws.ID)
	if len(got.Policy.AuthProfiles) != 1 || got.Policy.AuthProfiles[0] != AuthProfileGitCfg {
		t.Fatal("expected auth profile to be set")
	}

	got.Policy.AuthProfiles[0] = "modified"
	got.Policy.AuthProfiles = append(got.Policy.AuthProfiles, "another")

	if ws.Policy.AuthProfiles[0] == "modified" {
		t.Fatal("clone should not share auth profiles slice with original")
	}

	internal, _ := m.Get(ws.ID)
	if len(internal.Policy.AuthProfiles) != 1 || internal.Policy.AuthProfiles[0] != AuthProfileGitCfg {
		t.Fatalf("expected internal auth profiles unchanged, got %v", internal.Policy.AuthProfiles)
	}
}

func TestManager_CloneWorkspace_AvoidsMapAliasing(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		AuthBinding: map[string]string{
			"key1": "value1",
		},
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	got, _ := m.Get(ws.ID)
	if got.AuthBinding["key1"] != "value1" {
		t.Fatal("expected auth binding to be set")
	}

	got.AuthBinding["key1"] = "modified"
	if ws.AuthBinding["key1"] == "modified" {
		t.Fatal("clone should not share auth binding map with original")
	}
}

func TestManager_StopRestore_PreserveRunningState(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	if err := m.Start(ws.ID); err != nil {
		t.Fatalf("start returned error: %v", err)
	}

	if err := m.Stop(ws.ID); err != nil {
		t.Fatalf("stop returned error: %v", err)
	}

	m2 := NewManager(m.Root())
	got, ok := m2.Get(ws.ID)
	if !ok {
		t.Fatal("expected to get workspace after reload")
	}
	if got.State != StateStopped {
		t.Fatalf("expected state %q after stop, got %q", StateStopped, got.State)
	}

	r, ok := m2.Restore(ws.ID)
	if !ok {
		t.Fatal("expected restore to return true")
	}
	if r.State != StateRestored {
		t.Fatalf("expected state %q after restore, got %q", StateRestored, r.State)
	}
}

func TestManager_ForkPersistsParentWorkspaceID(t *testing.T) {
	m := newTestManager(t)
	parent, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "firecracker",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	child, err := m.Fork(parent.ID, "alpha-child", "alpha-child")
	if err != nil {
		t.Fatalf("fork returned error: %v", err)
	}
	if child.ParentWorkspaceID != parent.ID {
		t.Fatalf("expected child parent %q, got %q", parent.ID, child.ParentWorkspaceID)
	}
	if child.RepoID == "" {
		t.Fatal("expected child repoId to be set")
	}
	if child.RepoID != parent.RepoID {
		t.Fatalf("expected child repoId %q, got %q", parent.RepoID, child.RepoID)
	}
	if child.LineageRootID != parent.ID {
		t.Fatalf("expected child lineage root %q, got %q", parent.ID, child.LineageRootID)
	}
	if child.DerivedFromRef != parent.Ref {
		t.Fatalf("expected child derivedFromRef %q, got %q", parent.Ref, child.DerivedFromRef)
	}
	if child.Backend != parent.Backend {
		t.Fatalf("expected child backend %q, got %q", parent.Backend, child.Backend)
	}

	restored, ok := NewManager(m.Root()).Get(child.ID)
	if !ok {
		t.Fatal("expected child workspace to persist")
	}
	if restored.ParentWorkspaceID != parent.ID {
		t.Fatalf("expected persisted parent %q, got %q", parent.ID, restored.ParentWorkspaceID)
	}
	if restored.LineageRootID != parent.ID {
		t.Fatalf("expected persisted lineage root %q, got %q", parent.ID, restored.LineageRootID)
	}
}

func TestManager_SetLineageSnapshotPersistsAcrossReload(t *testing.T) {
	m := newTestManager(t)
	ws, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	if err := m.SetLineageSnapshot(ws.ID, "snap-123"); err != nil {
		t.Fatalf("set lineage snapshot returned error: %v", err)
	}

	reloaded := NewManager(m.Root())
	got, ok := reloaded.Get(ws.ID)
	if !ok {
		t.Fatal("expected workspace to exist after reload")
	}
	if got.LineageSnapshotID != "snap-123" {
		t.Fatalf("expected persisted lineage snapshot %q, got %q", "snap-123", got.LineageSnapshotID)
	}
}

func TestManager_ForkInheritsParentLineageSnapshotID(t *testing.T) {
	m := newTestManager(t)
	parent, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if err := m.SetLineageSnapshot(parent.ID, "snap-parent"); err != nil {
		t.Fatalf("set parent lineage snapshot returned error: %v", err)
	}

	child, err := m.Fork(parent.ID, "alpha-child", "alpha-child")
	if err != nil {
		t.Fatalf("fork returned error: %v", err)
	}
	if child.LineageSnapshotID != "snap-parent" {
		t.Fatalf("expected child to inherit lineage snapshot %q, got %q", "snap-parent", child.LineageSnapshotID)
	}
}

func TestManager_CreateSetsRepoIdentityForHostedAndLocal(t *testing.T) {
	m := newTestManager(t)
	hosted, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@github.com:IniZio/hanlun-lms.git",
		Ref:           "main",
		WorkspaceName: "hosted",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("hosted create returned error: %v", err)
	}
	if hosted.RepoID == "" {
		t.Fatal("expected hosted repoId to be set")
	}
	if hosted.RepoKind != "hosted" {
		t.Fatalf("expected hosted repo kind 'hosted', got %q", hosted.RepoKind)
	}
	if hosted.LineageRootID != hosted.ID {
		t.Fatalf("expected hosted lineage root %q, got %q", hosted.ID, hosted.LineageRootID)
	}

	local, err := m.Create(context.Background(), CreateSpec{
		Repo:          "./repos/hanlun-lms-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Ref:           "feature/worktree",
		WorkspaceName: "local",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("local create returned error: %v", err)
	}
	if local.RepoID == "" {
		t.Fatal("expected local repoId to be set")
	}
	if local.RepoKind != "local" {
		t.Fatalf("expected local repo kind 'local', got %q", local.RepoKind)
	}
	if local.LineageRootID != local.ID {
		t.Fatalf("expected local lineage root %q, got %q", local.ID, local.LineageRootID)
	}
}

func TestManager_ForkParallelWorkspacesRemainIndependent(t *testing.T) {
	m := newTestManager(t)
	parentA, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo-a.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "process",
	})
	if err != nil {
		t.Fatalf("create parentA returned error: %v", err)
	}
	parentB, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo-b.git",
		WorkspaceName: "beta",
		AgentProfile:  "default",
		Backend:       "process",
	})
	if err != nil {
		t.Fatalf("create parentB returned error: %v", err)
	}

	if err := m.Start(parentA.ID); err != nil {
		t.Fatalf("start parentA returned error: %v", err)
	}
	if err := m.Start(parentB.ID); err != nil {
		t.Fatalf("start parentB returned error: %v", err)
	}

	childA, err := m.Fork(parentA.ID, "alpha-child", "alpha-child")
	if err != nil {
		t.Fatalf("fork parentA returned error: %v", err)
	}

	if childA.ParentWorkspaceID != parentA.ID {
		t.Fatalf("expected child parent %q, got %q", parentA.ID, childA.ParentWorkspaceID)
	}
	if childA.Repo != parentA.Repo {
		t.Fatalf("expected child repo %q, got %q", parentA.Repo, childA.Repo)
	}

	gotB, ok := m.Get(parentB.ID)
	if !ok {
		t.Fatal("expected parentB to exist")
	}
	if gotB.ParentWorkspaceID != "" {
		t.Fatalf("expected parentB ParentWorkspaceID empty, got %q", gotB.ParentWorkspaceID)
	}
	if gotB.State != StateRunning {
		t.Fatalf("expected parentB state %q, got %q", StateRunning, gotB.State)
	}
}

func TestIsLikelyLocalPath_DetectsExistingRelativeDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	if err := os.MkdirAll("hanlun-lms", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if !isLikelyLocalPath("hanlun-lms") {
		t.Fatal("expected bare existing directory name to be treated as local path")
	}
}

func TestDeriveRepoKind_DetectsExistingRelativeDirectoryAsLocal(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	if err := os.MkdirAll("hanlun-lms", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if got := deriveRepoKind("hanlun-lms"); got != "local" {
		t.Fatalf("expected repo kind local, got %q", got)
	}
}

func TestManager_ForkFallsBackWhenLocalWorktreePathIsStale(t *testing.T) {
	m := newTestManager(t)
	repoRoot := initLocalRepoDirForWorkspaceTests(t)

	parent, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "parent-base",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "process",
	})
	if err != nil {
		t.Fatalf("create parent returned error: %v", err)
	}

	stalePath := filepath.Join(t.TempDir(), "missing-worktree")
	if err := m.SetLocalWorktree(parent.ID, stalePath, ""); err != nil {
		t.Fatalf("set stale local worktree path: %v", err)
	}

	child, err := m.Fork(parent.ID, "alpha-child", "child-ref")
	if err != nil {
		t.Fatalf("fork should recover from stale local worktree path: %v", err)
	}
	if child.LocalWorktreePath == "" {
		t.Fatal("expected child local worktree path to be set")
	}
	if _, statErr := os.Stat(child.LocalWorktreePath); statErr != nil {
		t.Fatalf("expected child local worktree path to exist: %v", statErr)
	}
	if gotBase := filepath.Dir(child.LocalWorktreePath); gotBase != filepath.Join(repoRoot, ".worktrees") {
		t.Fatalf("expected child host workspace path under %q, got %q", filepath.Join(repoRoot, ".worktrees"), child.LocalWorktreePath)
	}
}

func TestManager_ForkRejectsDuplicateBranchPerRepo(t *testing.T) {
	m := newTestManager(t)
	repoRoot := initLocalRepoDirForWorkspaceTests(t)

	parent, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "main",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
	})
	if err != nil {
		t.Fatalf("create parent returned error: %v", err)
	}
	if _, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "feature-x",
		WorkspaceName: "existing-feature",
		AgentProfile:  "default",
	}); err != nil {
		t.Fatalf("create existing feature workspace returned error: %v", err)
	}

	_, err = m.Fork(parent.ID, "alpha-child", "feature-x")
	if err == nil {
		t.Fatal("expected duplicate branch fork to fail")
	}
	if !strings.Contains(err.Error(), "workspace already exists for branch") {
		t.Fatalf("expected duplicate branch error, got %v", err)
	}
}

func initGitRepoForWorktreeTests(t *testing.T) string {
	t.Helper()
	return initLocalRepoDirForWorkspaceTests(t)
}

func initLocalRepoDirForWorkspaceTests(t *testing.T) string {
	t.Helper()

	repoRoot := t.TempDir()
	runGitForWorkspaceTests(t, repoRoot, "init")
	runGitForWorkspaceTests(t, repoRoot, "config", "user.email", "nexus-tests@example.com")
	runGitForWorkspaceTests(t, repoRoot, "config", "user.name", "Nexus Tests")
	runGitForWorkspaceTests(t, repoRoot, "checkout", "-B", "main")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGitForWorkspaceTests(t, repoRoot, "add", "README.md")
	runGitForWorkspaceTests(t, repoRoot, "commit", "-m", "init")
	return repoRoot
}

func runGitForWorkspaceTests(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}
