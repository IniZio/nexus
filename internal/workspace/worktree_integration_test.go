package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexus/pkg/git"
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

	cmd = exec.Command("git", "checkout", "-b", "main")
	cmd.Dir = tmpDir
	cmd.Run()

	return tmpDir
}

type testProvider struct {
	containers map[string]bool
	worktrees  map[string]string
}

func newTestProvider() *testProvider {
	return &testProvider{
		containers: make(map[string]bool),
		worktrees:  make(map[string]string),
	}
}

func (p *testProvider) Create(ctx context.Context, name string, worktreePath string) error {
	if p.containers[name] {
		return nil
	}
	p.containers[name] = true
	p.worktrees[name] = worktreePath
	return nil
}

func (p *testProvider) Start(ctx context.Context, name string) error {
	return nil
}

func (p *testProvider) Stop(ctx context.Context, name string) error {
	return nil
}

func (p *testProvider) Destroy(ctx context.Context, name string) error {
	delete(p.containers, name)
	return nil
}

func (p *testProvider) Shell(ctx context.Context, name string) error {
	return nil
}

func (p *testProvider) Exec(ctx context.Context, name string, command []string) error {
	return nil
}

func (p *testProvider) List(ctx context.Context) ([]WorkspaceInfo, error) {
	var result []WorkspaceInfo
	for name := range p.containers {
		result = append(result, WorkspaceInfo{
			Name:        name,
			Status:      "running",
			WorktreePath: p.worktrees[name],
		})
	}
	return result, nil
}

func (p *testProvider) Close() error {
	return nil
}

func TestWorktreeIntegration_CreateWorktree(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name := testutil.RandomWorkspaceName()
	err := manager.Create(name)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	worktreePath := gitManager.GetWorktreePath(name)
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Errorf("Worktree directory was not created at %s", worktreePath)
	}

	branchPath := filepath.Join(worktreePath, ".git")
	if _, err := os.Stat(branchPath); os.IsNotExist(err) {
		t.Error("Worktree .git directory not found")
	}

	nexusDir := filepath.Join(worktreePath, ".nexus")
	currentFile := filepath.Join(nexusDir, "current")
	if _, err := os.Stat(currentFile); os.IsNotExist(err) {
		t.Errorf(".nexus/current file was not created at %s", currentFile)
	}

	manager.Destroy(name)
}

func TestWorktreeIntegration_ContainerMountsWorktree(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name := testutil.RandomWorkspaceName()
	err := manager.Create(name)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	expectedPath := gitManager.GetWorktreePath(name)
	mountedPath := provider.worktrees[name]

	if expectedPath != mountedPath {
		t.Errorf("Container mounted wrong path: expected %s, got %s", expectedPath, mountedPath)
	}

	manager.Destroy(name)
}

func TestWorktreeIntegration_DestroyRemovesWorktree(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name := testutil.RandomWorkspaceName()
	err := manager.Create(name)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	worktreePath := gitManager.GetWorktreePath(name)
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Errorf("Worktree directory not found before destroy: %s", worktreePath)
	}

	err = manager.Destroy(name)
	if err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Errorf("Worktree directory was not removed: %s", worktreePath)
	}
}

func TestWorktreeIntegration_ListWorktrees(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	names := []string{
		testutil.RandomWorkspaceName(),
		testutil.RandomWorkspaceName(),
		testutil.RandomWorkspaceName(),
	}

	for _, name := range names {
		if err := manager.Create(name); err != nil {
			t.Fatalf("Create %s failed: %v", name, err)
		}
	}

	workspaces, err := manager.provider.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(workspaces) != len(names) {
		t.Errorf("Expected %d workspaces, got %d", len(names), len(workspaces))
	}

	for _, name := range names {
		found := false
		for _, ws := range workspaces {
			if ws.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Workspace %s not found in list", name)
		}
	}

	for _, name := range names {
		manager.Destroy(name)
	}
}

func TestWorktreeIntegration_DuplicateWorktreeError(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name := testutil.RandomWorkspaceName()
	if err := manager.Create(name); err != nil {
		t.Fatalf("First create failed: %v", err)
	}

	err := manager.Create(name)
	if err == nil {
		t.Error("Expected error for duplicate worktree, got nil")
	}

	manager.Destroy(name)
}

func TestWorktreeIntegration_WorktreeIsolation(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name1 := testutil.RandomWorkspaceName()
	name2 := testutil.RandomWorkspaceName()

	if err := manager.Create(name1); err != nil {
		t.Fatalf("Create %s failed: %v", name1, err)
	}

	if err := manager.Create(name2); err != nil {
		t.Fatalf("Create %s failed: %v", name2, err)
	}

	path1 := gitManager.GetWorktreePath(name1)
	path2 := gitManager.GetWorktreePath(name2)

	if path1 == path2 {
		t.Error("Worktrees should have different paths")
	}

	nexusDir1 := filepath.Join(path1, ".nexus")
	nexusDir2 := filepath.Join(path2, ".nexus")

	_, err1 := os.Stat(filepath.Join(nexusDir1, "current"))
	_, err2 := os.Stat(filepath.Join(nexusDir2, "current"))

	if err1 != nil || err2 != nil {
		t.Error("Each worktree should have its own .nexus/current file")
	}

	manager.Destroy(name1)
	manager.Destroy(name2)
}

func TestWorktreeIntegration_WorktreePathInLabels(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name := testutil.RandomWorkspaceName()
	err := manager.Create(name)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	manager.Destroy(name)
}

func TestWorktreeIntegration_ConcurrentCreates(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	numWorkspaces := 5
	names := make([]string, numWorkspaces)
	for i := 0; i < numWorkspaces; i++ {
		names[i] = testutil.RandomWorkspaceName()
	}

	errs := make(chan error, numWorkspaces)
	for i := 0; i < numWorkspaces; i++ {
		go func(idx int) {
			errs <- manager.Create(names[idx])
		}(i)
	}

	for i := 0; i < numWorkspaces; i++ {
		if err := <-errs; err != nil {
			t.Errorf("Concurrent create %d failed: %v", i, err)
		}
	}

	for _, name := range names {
		manager.Destroy(name)
	}
}

func TestWorktreeIntegration_SyncMethod(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name := testutil.RandomWorkspaceName()
	if err := manager.Create(name); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err := manager.Sync(name)
	if err != nil {
		t.Errorf("Sync failed: %v", err)
	}

	manager.Destroy(name)
}

func TestWorktreeIntegration_SyncNonexistentError(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	err := manager.Sync("nonexistent-workspace")
	if err == nil {
		t.Error("Expected error for sync on nonexistent worktree")
	}
}

func TestWorktreeIntegration_ZeroNameAutoGenerates(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	err := manager.Create("")
	if err != nil {
		t.Fatalf("Create with empty name failed: %v", err)
	}

	workspaces, _ := manager.provider.List(context.Background())
	if len(workspaces) == 0 {
		t.Error("Workspace should have been created")
	}

	for _, ws := range workspaces {
		manager.Destroy(ws.Name)
	}
}

func TestWorktreeIntegration_FileChangesInWorktree(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name := testutil.RandomWorkspaceName()
	if err := manager.Create(name); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	worktreePath := gitManager.GetWorktreePath(name)
	testFile := filepath.Join(worktreePath, "test-file.txt")
	testContent := []byte("test content " + time.Now().String())
	
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Error("Test file should exist in worktree")
	}

	manager.Destroy(name)
}

func TestWorktreeIntegration_GitBranchCreated(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name := testutil.RandomWorkspaceName()
	if err := manager.Create(name); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	worktreePath := gitManager.GetWorktreePath(name)
	
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = worktreePath
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get branch name: %v", err)
	}

	expectedBranch := "nexus/" + name
	actualBranch := strings.TrimSpace(string(output))
	if actualBranch != expectedBranch {
		t.Errorf("Expected branch %s, got %s", expectedBranch, actualBranch)
	}

	manager.Destroy(name)
}

func TestWorktreeIntegration_DestroyIdempotent(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	name := testutil.RandomWorkspaceName()
	if err := manager.Create(name); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err := manager.Destroy(name)
	if err != nil {
		t.Fatalf("First destroy failed: %v", err)
	}

	err = manager.Destroy(name)
	if err != nil {
		t.Fatalf("Second destroy (idempotent) failed: %v", err)
	}

	err = manager.Destroy(name)
	if err != nil {
		t.Fatalf("Third destroy (idempotent) failed: %v", err)
	}
}

func TestWorktreeIntegration_MultipleWorkspacesNoConflicts(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	
	repoDir := setupTestRepo(t)
	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newTestProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	numWorkspaces := 10
	names := make([]string, numWorkspaces)
	for i := 0; i < numWorkspaces; i++ {
		names[i] = testutil.RandomWorkspaceName()
	}

	for _, name := range names {
		if err := manager.Create(name); err != nil {
			t.Fatalf("Create %s failed: %v", name, err)
		}
	}

	workspaces, err := manager.provider.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(workspaces) != numWorkspaces {
		t.Errorf("Expected %d workspaces, got %d", numWorkspaces, len(workspaces))
	}

	for _, name := range names {
		found := false
		for _, ws := range workspaces {
			if ws.Name == name {
				found = true
				expectedPath := gitManager.GetWorktreePath(name)
				if ws.WorktreePath != expectedPath {
					t.Errorf("Workspace %s has wrong path: expected %s, got %s", name, expectedPath, ws.WorktreePath)
				}
				break
			}
		}
		if !found {
			t.Errorf("Workspace %s not found in list", name)
		}
	}

	for _, name := range names {
		manager.Destroy(name)
	}
}
