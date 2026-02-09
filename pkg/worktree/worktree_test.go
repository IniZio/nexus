package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func setupTestRepo(t *testing.T) string {
	tempDir, err := os.MkdirTemp("", "worktree-test-")
	if err != nil {
		t.Fatal(err)
	}

	repoPath := filepath.Join(tempDir, "repo")
	os.MkdirAll(repoPath, 0755)

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v, output: %s", args, err, string(output))
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")

	os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# Test Repo"), 0644)
	runGit("add", "README.md")
	runGit("commit", "-m", "Initial commit")

	return repoPath
}

func TestManager_Add(t *testing.T) {
	repoPath := setupTestRepo(t)
	defer os.RemoveAll(filepath.Dir(repoPath))

	baseDir := filepath.Join(filepath.Dir(repoPath), "worktrees")
	os.MkdirAll(baseDir, 0755)

	manager := NewManager(repoPath, baseDir)

	wtPath, err := manager.Add("feature-1")
	assert.NoError(t, err)
	assert.DirExists(t, wtPath)
	assert.FileExists(t, filepath.Join(wtPath, "README.md"))

	wtPath2, err := manager.Add("feature-1")
	assert.NoError(t, err)
	assert.Equal(t, wtPath, wtPath2)
}

func TestManager_Remove(t *testing.T) {
	repoPath := setupTestRepo(t)
	defer os.RemoveAll(filepath.Dir(repoPath))

	baseDir := filepath.Join(filepath.Dir(repoPath), "worktrees")
	os.MkdirAll(baseDir, 0755)

	manager := NewManager(repoPath, baseDir)

	wtPath, err := manager.Add("feature-1")
	assert.NoError(t, err)
	assert.DirExists(t, wtPath)

	err = manager.Remove("feature-1")
	assert.NoError(t, err)
	assert.NoDirExists(t, wtPath)
}
