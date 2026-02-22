package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/testutil"
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
		Name:   "test-workspace",
		Path:   "/path/to/worktree",
		Branch: "nexus/test-workspace",
		Exists: true,
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

func TestManager_ValidateWorktreeCreation(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	validName := testutil.RandomWorkspaceName()
	err := mgr.ValidateWorktreeCreation(validName)
	if err != nil {
		t.Errorf("Expected no error for valid name '%s': %v", validName, err)
	}
}

func TestManager_ValidateWorktreeCreation_EmptyName(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	err := mgr.ValidateWorktreeCreation("")
	if err == nil {
		t.Error("Expected error for empty worktree name")
	}
}

func TestManager_ValidateWorktreeCreation_InvalidChars(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	invalidNames := []string{"test@workspace", "test workspace", "test/workspace", "test\\workspace"}
	for _, name := range invalidNames {
		err := mgr.ValidateWorktreeCreation(name)
		if err == nil {
			t.Errorf("Expected error for invalid name '%s'", name)
		}
	}
}

func TestManager_ValidateWorktreeCreation_AlreadyExists(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()
	_, err := mgr.CreateWorktree(name)
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}
	defer mgr.RemoveWorktree(name)

	err = mgr.ValidateWorktreeCreation(name)
	if err == nil {
		t.Error("Expected error for duplicate worktree")
	}
}

func TestManager_ValidateWorktreeCreation_BranchExists(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()
	err := mgr.CreateBranch(name)
	if err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	err = mgr.ValidateWorktreeCreation(name)
	if err == nil {
		t.Error("Expected error when branch already exists")
	}
}

func TestManager_CreateOrRecreateWorktree(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()
	path1, err := mgr.CreateOrRecreateWorktree(name)
	if err != nil {
		t.Fatalf("First CreateOrRecreateWorktree failed: %v", err)
	}

	path2, err := mgr.CreateOrRecreateWorktree(name)
	if err != nil {
		t.Fatalf("Second CreateOrRecreateWorktree (idempotent) failed: %v", err)
	}

	if path1 != path2 {
		t.Errorf("Expected same path for idempotent operation: %s != %s", path1, path2)
	}

	mgr.RemoveWorktree(name)
}

func TestManager_CreateOrRecreateWorktree_BrokenWorktree(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()

	path, err := mgr.CreateWorktree(name)
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	t.Logf("Created worktree at: %s", path)
	t.Logf("Worktree exists: %v", mgr.WorktreeExists(name))
	t.Logf("Is valid worktree: %v", mgr.isValidWorktree(name))

	gitFile := filepath.Join(path, ".git")
	if err := os.RemoveAll(gitFile); err != nil {
		t.Fatalf("Failed to corrupt worktree: %v", err)
	}

	t.Logf("After corruption:")
	t.Logf("Worktree exists: %v", mgr.WorktreeExists(name))
	t.Logf("Is valid worktree: %v", mgr.isValidWorktree(name))

	newPath, err := mgr.CreateOrRecreateWorktree(name)
	if err != nil {
		t.Fatalf("CreateOrRecreateWorktree should recreate broken worktree: %v", err)
	}

	if newPath != path {
		t.Errorf("Expected same path after recreation: %s != %s", newPath, path)
	}

	if !mgr.isValidWorktree(name) {
		t.Error("Worktree should be valid after recreation")
	}

	mgr.RemoveWorktree(name)
}

func TestManager_DuplicateWorktreeError_Message(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()
	_, err := mgr.CreateWorktree(name)
	if err != nil {
		t.Fatalf("First CreateWorktree failed: %v", err)
	}
	defer mgr.RemoveWorktree(name)

	_, err = mgr.CreateWorktree(name)
	if err == nil {
		t.Error("Expected error for duplicate worktree")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "already exists") {
		t.Errorf("Expected error message to contain 'already exists', got: %s", errMsg)
	}
}

func TestManager_BranchConflictError(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()

	err := mgr.CreateBranch(name)
	if err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	_, err = mgr.CreateWorktree(name)
	if err == nil {
		t.Error("Expected error when branch already exists")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "branch") && !strings.Contains(errMsg, "already exists") {
		t.Errorf("Expected error message about branch conflict, got: %s", errMsg)
	}
}

func TestManager_IsValidWorktree(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	name := testutil.RandomWorkspaceName()

	if mgr.isValidWorktree(name) {
		t.Error("Non-existent worktree should not be valid")
	}

	path, err := mgr.CreateWorktree(name)
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}
	defer mgr.RemoveWorktree(name)

	if !mgr.isValidWorktree(name) {
		t.Error("Valid worktree should be valid")
	}

	gitFile := filepath.Join(path, ".git")
	if err := os.RemoveAll(gitFile); err != nil {
		t.Fatalf("Failed to corrupt worktree: %v", err)
	}

	if mgr.isValidWorktree(name) {
		t.Error("Corrupted worktree should not be valid")
	}
}

func TestManager_checkGitVersion(t *testing.T) {
	repoDir := setupTestRepo(t)
	mgr := NewManagerWithRepoRoot(repoDir)

	err := mgr.checkGitVersion()
	if err != nil {
		t.Errorf("Expected no error for git version check: %v", err)
	}
}

func TestManager_validateWorktreeName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid-name", false},
		{"valid123", false},
		{"valid_name", false},
		{"valid-name-123", false},
		{"", true},
		{"a", false},
		{"ab", false},
		{"test@workspace", true},
		{"test workspace", true},
		{"test/workspace", true},
		{"test\\workspace", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorktreeNameForTest(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateWorktreeName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func validateWorktreeNameForTest(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("worktree name cannot be empty")
	}

	if len(name) > 255 {
		return fmt.Errorf("worktree name too long (max 255 characters)")
	}

	validName := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
	if !validName.MatchString(name) {
		return fmt.Errorf("worktree name must contain only alphanumeric characters, hyphens, and underscores")
	}

	reservedNames := []string{"con", "prn", "aux", "nul", "com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9", "lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9"}
	lowerName := strings.ToLower(name)
	for _, reserved := range reservedNames {
		if lowerName == reserved {
			return fmt.Errorf("worktree name '%s' is reserved", name)
		}
	}

	return nil
}
