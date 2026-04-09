package workspacemgr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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

func TestManager_PauseResumeStateTransitions(t *testing.T) {
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

	if err := m.Pause(ws.ID); err != nil {
		t.Fatalf("pause returned error: %v", err)
	}
	paused, ok := m.Get(ws.ID)
	if !ok {
		t.Fatal("expected workspace after pause")
	}
	if paused.State != StatePaused {
		t.Fatalf("expected paused state %q, got %q", StatePaused, paused.State)
	}

	if err := m.Resume(ws.ID); err != nil {
		t.Fatalf("resume returned error: %v", err)
	}
	running, ok := m.Get(ws.ID)
	if !ok {
		t.Fatal("expected workspace after resume")
	}
	if running.State != StateRunning {
		t.Fatalf("expected running state %q, got %q", StateRunning, running.State)
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
		Backend:       "local",
	})
	if err != nil {
		t.Fatalf("create parentA returned error: %v", err)
	}
	parentB, err := m.Create(context.Background(), CreateSpec{
		Repo:          "git@example/repo-b.git",
		WorkspaceName: "beta",
		AgentProfile:  "default",
		Backend:       "local",
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

func TestResolveForkBasePath_PrefersNestedParentUnderRepoRoot(t *testing.T) {
	repoRoot := t.TempDir()
	parentName := "alpha"
	nestedParent := filepath.Join(repoRoot, ".worktrees", sanitizeWorktreeName(parentName))
	if err := os.MkdirAll(nestedParent, 0o755); err != nil {
		t.Fatalf("mkdir nested parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".git"), []byte("gitdir: /tmp/fake\n"), 0o644); err != nil {
		t.Fatalf("write .git marker: %v", err)
	}

	parent := &Workspace{
		Repo:              repoRoot,
		WorkspaceName:     parentName,
		LocalWorktreePath: repoRoot,
	}

	got := resolveForkBasePath(parent)
	if got != nestedParent {
		t.Fatalf("expected nested parent %q, got %q", nestedParent, got)
	}
}

func TestResolveForkBasePath_FallsBackToInferredPathWithoutLocalWorktree(t *testing.T) {
	repoRoot := t.TempDir()
	parentName := "alpha"
	inferred := filepath.Join(repoRoot, ".worktrees", sanitizeWorktreeName(parentName))
	if err := os.MkdirAll(inferred, 0o755); err != nil {
		t.Fatalf("mkdir inferred parent: %v", err)
	}

	parent := &Workspace{
		Repo:          repoRoot,
		WorkspaceName: parentName,
	}

	got := resolveForkBasePath(parent)
	if got != inferred {
		t.Fatalf("expected inferred path %q, got %q", inferred, got)
	}
}

func TestForkChildrenDir_UsesRepoRootForNestedParent(t *testing.T) {
	parent := filepath.Join("/tmp/repo", ".worktrees", "alpha")
	got := forkChildrenDir(parent)
	want := filepath.Join("/tmp/repo", ".worktrees")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
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
	repoRoot := initGitRepoForWorktreeTests(t)

	parent, err := m.Create(context.Background(), CreateSpec{
		Repo:          repoRoot,
		Ref:           "parent-base",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		Backend:       "local",
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
		t.Fatalf("expected child worktree under %q, got %q", filepath.Join(repoRoot, ".worktrees"), child.LocalWorktreePath)
	}
}

func initGitRepoForWorktreeTests(t *testing.T) string {
	t.Helper()

	repoRoot := t.TempDir()
	runGitForWorktreeTests(t, repoRoot, "init")
	runGitForWorktreeTests(t, repoRoot, "config", "user.email", "nexus-tests@example.com")
	runGitForWorktreeTests(t, repoRoot, "config", "user.name", "Nexus Tests")

	readmePath := filepath.Join(repoRoot, "README.md")
	if err := os.WriteFile(readmePath, []byte("# test repo\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGitForWorktreeTests(t, repoRoot, "add", "README.md")
	runGitForWorktreeTests(t, repoRoot, "commit", "-m", "initial commit")

	return repoRoot
}

func runGitForWorktreeTests(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}
