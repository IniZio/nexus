package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"nexus/pkg/testutil"
)

func setupTestRepo(t *testing.T) string {
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to init git repo: %v", err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	cmd.Run()

	readmePath := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("Failed to write README: %v", err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to add files: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	return tmpDir
}

func TestManager_CreateWorktree(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()
	path, err := mgr.CreateWorktree(name)
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	expectedPath := filepath.Join(repoDir, ".nexus", "worktrees", name)
	if path != expectedPath {
		t.Errorf("Expected path %s, got %s", expectedPath, path)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("Worktree directory was not created at %s", path)
	}

	gitFile := filepath.Join(path, ".git")
	if _, err := os.Stat(gitFile); os.IsNotExist(err) {
		t.Error("Worktree .git file not found")
	}

	mgr.RemoveWorktree(name)
}

func TestManager_RemoveWorktree(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()
	_, err := mgr.CreateWorktree(name)
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	err = mgr.RemoveWorktree(name)
	if err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}

	worktreePath := filepath.Join(repoDir, ".nexus", "worktrees", name)
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Errorf("Worktree directory was not removed: %s", worktreePath)
	}
}

func TestManager_GetWorktreePath(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()
	expectedPath := filepath.Join(repoDir, ".nexus", "worktrees", name)

	path := mgr.GetWorktreePath(name)
	if path != expectedPath {
		t.Errorf("Expected path %s, got %s", expectedPath, path)
	}
}

func TestManager_WorktreeExists(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()

	if mgr.WorktreeExists(name) {
		t.Error("Worktree should not exist before creation")
	}

	_, err := mgr.CreateWorktree(name)
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	if !mgr.WorktreeExists(name) {
		t.Error("Worktree should exist after creation")
	}

	mgr.RemoveWorktree(name)

	if mgr.WorktreeExists(name) {
		t.Error("Worktree should not exist after removal")
	}
}

func TestManager_ListWorktrees(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	names := []string{
		testutil.RandomWorkspaceName(),
		testutil.RandomWorkspaceName(),
		testutil.RandomWorkspaceName(),
	}

	for _, name := range names {
		_, err := mgr.CreateWorktree(name)
		if err != nil {
			t.Fatalf("CreateWorktree %s failed: %v", name, err)
		}
	}

	worktrees, err := mgr.ListWorktrees()
	if err != nil {
		t.Fatalf("ListWorktrees failed: %v", err)
	}

	if len(worktrees) != len(names) {
		t.Errorf("Expected %d worktrees, got %d", len(names), len(worktrees))
	}

	for _, name := range names {
		found := false
		for _, wt := range worktrees {
			if wt.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Worktree %s not found in list", name)
		}
	}

	for _, name := range names {
		mgr.RemoveWorktree(name)
	}
}

func TestManager_CreateBranch(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()
	branchName := "nexus/" + name

	err := mgr.CreateBranch(name)
	if err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	cmd := exec.Command("git", "rev-parse", "--abspath", branchName)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Errorf("Branch should exist: %v", err)
	}
}

func TestManager_GetRepoRoot(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	if mgr.GetRepoRoot() != repoDir {
		t.Errorf("Expected repo root %s, got %s", repoDir, mgr.GetRepoRoot())
	}
}

func TestManager_HasGitRepo(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	if !mgr.HasGitRepo() {
		t.Error("Should detect git repo")
	}

	nonGitDir := t.TempDir()
	mgrNonGit := NewManagerWithRepoRoot(nonGitDir)

	if mgrNonGit.HasGitRepo() {
		t.Error("Should not detect git repo in non-git directory")
	}
}

func TestManager_DuplicateWorktreeError(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()
	_, err := mgr.CreateWorktree(name)
	if err != nil {
		t.Fatalf("First CreateWorktree failed: %v", err)
	}

	_, err = mgr.CreateWorktree(name)
	if err == nil {
		t.Error("Expected error for duplicate worktree")
	}

	mgr.RemoveWorktree(name)
}

func TestWorktreeInfo_Struct(t *testing.T) {
	info := WorktreeInfo{
		Name:    "test-workspace",
		Path:    "/path/to/worktree",
		Branch:  "nexus/test-workspace",
		Exists:  true,
	}

	if info.Name != "test-workspace" {
		t.Errorf("Expected name 'test-workspace', got '%s'", info.Name)
	}

	if info.Path != "/path/to/worktree" {
		t.Errorf("Expected path '/path/to/worktree', got '%s'", info.Path)
	}

	if info.Branch != "nexus/test-workspace" {
		t.Errorf("Expected branch 'nexus/test-workspace', got '%s'", info.Branch)
	}

	if !info.Exists {
		t.Error("Expected Exists to be true")
	}
}

func TestManager_WorktreeWithSpecialChars(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := "test-workspace-123-abc"
	path, err := mgr.CreateWorktree(name)
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("Worktree directory was not created at %s", path)
	}

	mgr.RemoveWorktree(name)
}
