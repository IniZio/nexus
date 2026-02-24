package git

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
