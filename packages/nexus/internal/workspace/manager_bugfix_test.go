package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/git"
)

func TestValidateCreate_EmptyName(t *testing.T) {
	provider := newMockProvider()
	manager := NewManager(provider)

	err := manager.validateCreate("")
	if err == nil {
		t.Error("Expected error for empty name")
	}
}

func TestValidateCreate_InvalidCharacters(t *testing.T) {
	provider := newMockProvider()
	manager := NewManager(provider)

	testCases := []struct {
		name    string
		wantErr bool
	}{
		{"valid-name", false},
		{"valid_name", false},
		{"Valid123", false},
		{"invalid name", true},
		{"invalid@name", true},
		{"invalid.name", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := manager.validateCreate(tc.name)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateCreate(%s) error = %v, wantErr %v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func TestValidateCreate_AlreadyExists(t *testing.T) {
	repoDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to init git repo: %v", err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = repoDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoDir
	cmd.Run()

	readmePath := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("Failed to write README: %v", err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to add files: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	gitManager := git.NewManagerWithRepoRoot(repoDir)
	provider := newMockProvider()
	manager := NewManagerWithGitManager(provider, gitManager)

	wsName := "testwsexists"
	_, err := gitManager.CreateWorktree(wsName)
	if err != nil {
		t.Fatalf("Failed to create worktree: %v", err)
	}

	err = manager.validateCreate(wsName)
	if err == nil {
		t.Error("Expected error for existing worktree")
	}
}

func TestRepair_WorkspaceNotFound(t *testing.T) {
	provider := newMockProvider()
	manager := NewManager(provider)

	err := manager.Repair("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent workspace")
	}
}

func TestRepair_WorktreeExistsContainerMissing(t *testing.T) {
	provider := newMockProvider()

	wsName := "repair-test-" + t.Name()

	ctx := context.Background()
	exists, _ := provider.ContainerExists(ctx, wsName)
	if exists {
		t.Skip("Container already exists")
	}
}

func TestUp_ContainerNotFound(t *testing.T) {
	provider := newMockProvider()
	manager := NewManager(provider)

	err := manager.Up("nonexistent")
	if err == nil {
		t.Error("Expected error when container not found")
	}
}

func TestUp_WorktreeExistsNoContainer(t *testing.T) {
	provider := newMockProvider()

	wsName := "up-test-" + t.Name()

	ctx := context.Background()
	exists, _ := provider.ContainerExists(ctx, wsName)
	if exists {
		t.Skip("Container already exists")
	}
}

func TestContainerExists(t *testing.T) {
	provider := newMockProvider()
	ctx := context.Background()

	exists, err := provider.ContainerExists(ctx, "nonexistent")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if exists {
		t.Error("Expected false for nonexistent container")
	}

	provider.containers["test-ws"] = true
	exists, err = provider.ContainerExists(ctx, "test-ws")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !exists {
		t.Error("Expected true for existing container")
	}
}
