package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type Manager interface {
	Add(branch string) (string, error)
	Remove(branch string) error
}

type gitManager struct {
	RepoPath string
	BaseDir  string
}

func NewManager(repoPath string, baseDir string) Manager {
	return &gitManager{
		RepoPath: repoPath,
		BaseDir:  baseDir,
	}
}

func (m *gitManager) Add(branch string) (string, error) {
	wtPath := filepath.Join(m.BaseDir, branch)
	if _, err := os.Stat(wtPath); err == nil {
		return wtPath, nil
	}

	currentBranch, err := m.getCurrentBranch()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}

	if currentBranch == branch {
		safeBranches := []string{"main", "master"}
		switched := false
		for _, safeBranch := range safeBranches {
			if m.branchExists(safeBranch) {
				if err := m.switchToBranch(safeBranch); err == nil {
					switched = true
					break
				}
			}
		}
		if !switched {
			return "", fmt.Errorf("cannot create worktree: currently on branch %s and no safe branch (main/master) found", branch)
		}
	}

	if !m.branchExists(branch) {
		cmd := exec.Command("git", "checkout", "-b", branch)
		cmd.Dir = m.RepoPath
		if output, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to create branch %s: %w, output: %s", branch, err, string(output))
		}
		if currentBranch != "" && currentBranch != branch {
			_ = m.switchToBranch(currentBranch)
		}
	}

	cmd := exec.Command("git", "worktree", "add", wtPath, branch)
	cmd.Dir = m.RepoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create worktree: %w, output: %s", err, string(output))
	}

	return wtPath, nil
}

func (m *gitManager) getCurrentBranch() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = m.RepoPath
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output[:len(output)-1]), nil
}

func (m *gitManager) branchExists(branch string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = m.RepoPath
	return cmd.Run() == nil
}

func (m *gitManager) switchToBranch(branch string) error {
	cmd := exec.Command("git", "checkout", branch)
	cmd.Dir = m.RepoPath
	return cmd.Run()
}

func (m *gitManager) Remove(branch string) error {
	wtPath := filepath.Join(m.BaseDir, branch)

	cmd := exec.Command("git", "worktree", "remove", branch)
	cmd.Dir = m.RepoPath
	_ = cmd.Run()

	return os.RemoveAll(wtPath)
}
