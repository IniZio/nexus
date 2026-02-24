package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTempGitRepo(t *testing.T) string {
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to init git repo: %v\nOutput: %s", err, string(out))
	}

	cmd = exec.Command("git", "config", "init.defaultBranch", "main")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("failed to set default branch: %v\nOutput: %s", err, string(out))
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run())

	testFile := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(testFile, []byte("# Test\n"), 0644))

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "checkout", "-b", "main")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "already exists") {
			t.Logf("failed to create main branch (may already exist): %v\nOutput: %s", err, string(out))
		}
	}

	return tmpDir
}

func TestNewWorktreeManager(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	assert.NotNil(t, mgr)
	assert.Equal(t, repoDir, mgr.GetProjectRoot())
}

func TestGetWorktreePath(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	path := mgr.GetWorktreePath("test-worktree")
	expected := filepath.Join(repoDir, ".worktree", "test-worktree")
	assert.Equal(t, expected, path)
}

func TestWorktreeExists(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	assert.False(t, mgr.WorktreeExists("nonexistent"))

	worktreePath := filepath.Join(repoDir, ".worktree", "test-exists")
	os.MkdirAll(worktreePath, 0755)
	gitFile := filepath.Join(worktreePath, ".git")
	os.WriteFile(gitFile, []byte("gitfile: .git\n"), 0644)

	assert.True(t, mgr.WorktreeExists("test-exists"))
}

func TestCreateWorktree_Success(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	path, err := mgr.CreateWorktree("feature-branch", "main")

	require.NoError(t, err)
	assert.NotEmpty(t, path)
	assert.True(t, mgr.WorktreeExists("feature-branch"))

	gitFile := filepath.Join(path, ".git")
	_, err = os.Stat(gitFile)
	assert.NoError(t, err)
}

func TestCreateWorktree_EmptyName(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	path, err := mgr.CreateWorktree("", "main")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be empty")
	assert.Empty(t, path)
}

func TestCreateWorktree_AlreadyExists(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	_, err := mgr.CreateWorktree("duplicate-test", "main")
	require.NoError(t, err)

	path, err := mgr.CreateWorktree("duplicate-test", "main")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Empty(t, path)
}

func TestCreateWorktree_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "somefile.txt"), []byte("content"), 0644)
	mgr := NewWorktreeManager(tmpDir)

	path, err := mgr.CreateWorktree("test", "main")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "working tree has uncommitted changes")
	assert.Empty(t, path)
}

func TestCreateWorktree_BranchConflict(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	cmd := exec.Command("git", "checkout", "-b", "nexus/conflict-test")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	path, err := mgr.CreateWorktree("conflict-test", "main")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "branch")
	assert.Empty(t, path)
}

func TestCreateWorktree_DefaultBranch(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	path, err := mgr.CreateWorktree("test-default", "")

	require.NoError(t, err)
	assert.NotEmpty(t, path)
}

func TestDeleteWorktree_Success(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	_, err := mgr.CreateWorktree("to-delete", "main")
	require.NoError(t, err)

	err = mgr.DeleteWorktree("to-delete", true)

	require.NoError(t, err)
	assert.False(t, mgr.WorktreeExists("to-delete"))

	cmd := exec.Command("git", "branch", "--list", "nexus/to-delete")
	cmd.Dir = repoDir
	output, _ := cmd.Output()
	assert.Empty(t, output)
}

func TestDeleteWorktree_NotExists(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	err := mgr.DeleteWorktree("nonexistent", false)

	assert.NoError(t, err)
}

func TestListWorktrees_Empty(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	worktrees, err := mgr.ListWorktrees()

	require.NoError(t, err)
	assert.Empty(t, worktrees)
}

func TestListWorktrees_Success(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	_, err := mgr.CreateWorktree("list-test-1", "main")
	require.NoError(t, err)
	_, err = mgr.CreateWorktree("list-test-2", "main")
	require.NoError(t, err)

	worktrees, err := mgr.ListWorktrees()

	require.NoError(t, err)
	assert.Len(t, worktrees, 2)

	names := []string{worktrees[0].Name, worktrees[1].Name}
	assert.Contains(t, names, "list-test-1")
	assert.Contains(t, names, "list-test-2")
}

func TestListWorktrees_NonGitDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".worktree", "fake"), 0755)

	mgr := NewWorktreeManager(tmpDir)

	worktrees, err := mgr.ListWorktrees()

	require.NoError(t, err)
	assert.Empty(t, worktrees)
}

func TestWorktreeManager_GetProjectRoot(t *testing.T) {
	repoDir := createTempGitRepo(t)
	mgr := NewWorktreeManager(repoDir)

	assert.Equal(t, repoDir, mgr.GetProjectRoot())
}
