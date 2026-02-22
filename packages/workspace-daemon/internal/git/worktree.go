package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type WorktreeInfo struct {
	Name   string
	Branch string
	Path   string
	Head   string
}

type WorktreeManager struct {
	projectRoot string
}

func NewWorktreeManager(projectRoot string) *WorktreeManager {
	return &WorktreeManager{projectRoot: projectRoot}
}

func (m *WorktreeManager) worktreesPath() string {
	return filepath.Join(m.projectRoot, ".worktree")
}

func (m *WorktreeManager) CreateWorktree(name string, baseBranch string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("worktree name cannot be empty")
	}

	worktreesPath := m.worktreesPath()
	if err := os.MkdirAll(worktreesPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create worktrees directory: %w", err)
	}

	worktreePath := filepath.Join(worktreesPath, name)
	branchName := fmt.Sprintf("nexus/%s", name)

	if baseBranch == "" {
		baseBranch = "main"
	}

	if m.WorktreeExists(name) {
		return "", fmt.Errorf("worktree '%s' already exists at %s", name, worktreePath)
	}

	if m.branchExists(branchName) {
		return "", fmt.Errorf("branch '%s' already exists", branchName)
	}

	if err := m.checkForDirtyTree(); err != nil {
		return "", err
	}

	if !m.isGitRepo() {
		return "", fmt.Errorf("not a git repository: %s", m.projectRoot)
	}

	cmd := exec.Command("git", "worktree", "add", worktreePath, "-b", branchName, baseBranch)
	cmd.Dir = m.projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add failed: %w\nOutput: %s", err, string(output))
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", fmt.Errorf("worktree directory was not created at %s", worktreePath)
	}

	return worktreePath, nil
}

func (m *WorktreeManager) DeleteWorktree(name string, deleteBranch bool) error {
	worktreePath := m.GetWorktreePath(name)
	branchName := fmt.Sprintf("nexus/%s", name)

	cmd := exec.Command("git", "worktree", "remove", "-f", worktreePath)
	cmd.Dir = m.projectRoot
	if err := cmd.Run(); err != nil {
		gitFile := filepath.Join(worktreePath, ".git")
		if _, statErr := os.Stat(gitFile); os.IsNotExist(statErr) {
			os.RemoveAll(worktreePath)
		} else {
			return fmt.Errorf("failed to remove worktree: %w", err)
		}
	}

	if deleteBranch {
		deleteCmd := exec.Command("git", "branch", "-D", branchName)
		deleteCmd.Dir = m.projectRoot
		deleteCmd.Run()
	}

	return nil
}

func (m *WorktreeManager) ListWorktrees() ([]WorktreeInfo, error) {
	worktreesPath := m.worktreesPath()
	entries, err := os.ReadDir(worktreesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []WorktreeInfo{}, nil
		}
		return nil, fmt.Errorf("failed to read worktrees directory: %w", err)
	}

	var worktrees []WorktreeInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		worktreePath := filepath.Join(worktreesPath, name)

		gitFile := filepath.Join(worktreePath, ".git")
		if _, err := os.Stat(gitFile); err != nil {
			continue
		}

		branchName := fmt.Sprintf("nexus/%s", name)
		cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
		cmd.Dir = worktreePath
		output, err := cmd.Output()
		actualBranch := branchName
		if err == nil && len(output) > 0 {
			actualBranch = strings.TrimSpace(string(output))
		}

		headCmd := exec.Command("git", "rev-parse", "HEAD")
		headCmd.Dir = worktreePath
		headOutput, _ := headCmd.Output()
		head := strings.TrimSpace(string(headOutput))

		worktrees = append(worktrees, WorktreeInfo{
			Name:   name,
			Branch: actualBranch,
			Path:   worktreePath,
			Head:   head,
		})
	}

	return worktrees, nil
}

func (m *WorktreeManager) GetWorktreePath(name string) string {
	return filepath.Join(m.worktreesPath(), name)
}

func (m *WorktreeManager) WorktreeExists(name string) bool {
	worktreePath := m.GetWorktreePath(name)
	gitFile := filepath.Join(worktreePath, ".git")
	_, err := os.Stat(gitFile)
	return err == nil
}

func (m *WorktreeManager) branchExists(branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branchName)
	cmd.Dir = m.projectRoot
	if err := cmd.Run(); err == nil {
		return true
	}

	cmd = exec.Command("git", "branch", "--list", branchName)
	cmd.Dir = m.projectRoot
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		return true
	}

	return false
}

func (m *WorktreeManager) checkForDirtyTree() error {
	cmd := exec.Command("git", "diff", "--quiet", "--ignore-submodules")
	cmd.Dir = m.projectRoot
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("working tree has uncommitted changes; commit or stash them before creating a worktree")
	}

	cmd = exec.Command("git", "diff", "--quiet", "--ignore-submodules", "--cached")
	cmd.Dir = m.projectRoot
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("index has uncommitted changes; commit or stash them before creating a worktree")
	}

	return nil
}

func (m *WorktreeManager) isGitRepo() bool {
	_, err := os.Stat(filepath.Join(m.projectRoot, ".git"))
	return err == nil
}

func (m *WorktreeManager) GetProjectRoot() string {
	return m.projectRoot
}
