package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_NewManager(t *testing.T) {
	manager := NewManager()
	assert.NotNil(t, manager)
	assert.NotEmpty(t, manager.repoRoot)
}

func TestManager_NewManagerWithRepoRoot(t *testing.T) {
	manager := NewManagerWithRepoRoot("/custom/path")
	assert.NotNil(t, manager)
	assert.Equal(t, "/custom/path", manager.repoRoot)
}

func TestManager_worktreesPath(t *testing.T) {
	manager := NewManagerWithRepoRoot("/test/repo")
	path := manager.worktreesPath()
	assert.Equal(t, "/test/repo/.worktree", path)
}

func TestManager_HasGitRepo(t *testing.T) {
	// Create a temp directory that is a git repo
	tmpDir := t.TempDir()
	
	// Initialize a git repo
	err := os.WriteFile(filepath.Join(tmpDir, ".git"), []byte("gitdir: .git"), 0755)
	require.NoError(t, err)
	
	manager := NewManagerWithRepoRoot(tmpDir)
	// This will check if .git exists
	assert.True(t, manager.HasGitRepo())
}

func TestManager_validateWorktreeName(t *testing.T) {
	manager := NewManager()

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid-workspace", false},
		{"workspace123", false},
		{"ws_123", false},
		{"", true},
		{"workspace with spaces", true},
		{"-leading-dash", true},
		{"trailing-dash-", true},
		{"special@char", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.validateWorktreeName(tt.name)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestManager_branchExists(t *testing.T) {
	manager := NewManager()
	
	// This test will depend on actual git state
	// Skip if not in a git repo
	if !manager.HasGitRepo() {
		t.Skip("Not in a git repository")
	}
	
	exists := manager.branchExists("main")
	assert.True(t, exists)
	
	exists = manager.branchExists("nonexistent-branch-12345")
	assert.False(t, exists)
}

func TestManager_ListWorktrees(t *testing.T) {
	manager := NewManager()
	
	// This test will depend on actual git state
	if !manager.HasGitRepo() {
		t.Skip("Not in a git repository")
	}
	
	worktrees, err := manager.ListWorktrees()
	assert.NoError(t, err)
	assert.NotNil(t, worktrees)
}

func TestManager_WorktreeExists(t *testing.T) {
	manager := NewManager()
	
	// This test will depend on actual git state
	if !manager.HasGitRepo() {
		t.Skip("Not in a git repository")
	}
	
	exists := manager.WorktreeExists("nonexistent-worktree-12345")
	assert.False(t, exists)
}

func TestManager_GetWorktreeInfo(t *testing.T) {
	manager := NewManager()
	
	// This test will depend on actual git state
	if !manager.HasGitRepo() {
		t.Skip("Not in a git repository")
	}
	
	info := manager.GetWorktreeInfo("nonexistent-worktree")
	assert.False(t, info.Exists)
}

func TestWorktreeInfo_String(t *testing.T) {
	info := WorktreeInfo{
		Name:   "test-ws",
		Path:   "/path/to/worktree",
		Branch: "nexus/test-ws",
		Exists: true,
	}
	
	str := info.String()
	assert.Contains(t, str, "test-ws")
	assert.Contains(t, str, "nexus/test-ws")
}

func TestManager_WorktreePath(t *testing.T) {
	manager := NewManagerWithRepoRoot("/test/repo")
	
	path := manager.WorktreePath("my-workspace")
	assert.Equal(t, "/test/repo/.worktree/my-workspace", path)
}
