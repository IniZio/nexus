package sandbox

import (
	"fmt"
	"os/exec"
)

// ForkWorktree creates a new git worktree at childPath from the repository
// rooted at parentPath. The child worktree starts at the same commit as the
// parent's current HEAD.
//
// This is the fork primitive for the sandbox driver. It provides source-tree
// isolation via git worktrees, not filesystem-level CoW isolation.
func ForkWorktree(parentPath, childPath string) error {
	cmd := exec.Command("git", "worktree", "add", "--detach", childPath, "HEAD")
	cmd.Dir = parentPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %w\n%s", err, out)
	}
	return nil
}
