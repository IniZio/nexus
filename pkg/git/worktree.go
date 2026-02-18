package git

import (
	"os"
	"os/exec"
	"path/filepath"
)

type WorktreeInfo struct {
	Name    string
	Path    string
	Branch  string
	Exists  bool
}

type Manager struct {
	repoRoot string
}

func NewManager() *Manager {
	repoRoot, _ := os.Getwd()
	return &Manager{repoRoot: repoRoot}
}

func NewManagerWithRepoRoot(repoRoot string) *Manager {
	return &Manager{repoRoot: repoRoot}
}

func (m *Manager) worktreesPath() string {
	return filepath.Join(m.repoRoot, ".nexus", "worktrees")
}

func (m *Manager) CreateWorktree(name string) (string, error) {
	worktreesPath := m.worktreesPath()
	if err := os.MkdirAll(worktreesPath, 0755); err != nil {
		return "", err
	}

	worktreePath := filepath.Join(worktreesPath, name)
	branchName := "nexus/" + name

	cmd := exec.Command("git", "worktree", "add", worktreePath, "-b", branchName)
	cmd.Dir = m.repoRoot
	if err := cmd.Run(); err != nil {
		return "", err
	}

	return worktreePath, nil
}

func (m *Manager) CreateBranch(name string) error {
	branchName := "nexus/" + name
	cmd := exec.Command("git", "branch", branchName)
	cmd.Dir = m.repoRoot
	return cmd.Run()
}

func (m *Manager) RemoveWorktree(name string) error {
	worktreePath := filepath.Join(m.worktreesPath(), name)
	branchName := "nexus/" + name

	removeCmd := exec.Command("git", "worktree", "remove", "-f", worktreePath)
	removeCmd.Dir = m.repoRoot
	if err := removeCmd.Run(); err != nil {
		return err
	}

	deleteCmd := exec.Command("git", "branch", "-D", branchName)
	deleteCmd.Dir = m.repoRoot
	return deleteCmd.Run()
}

func (m *Manager) GetWorktreePath(name string) string {
	return filepath.Join(m.worktreesPath(), name)
}

func (m *Manager) WorktreeExists(name string) bool {
	worktreePath := filepath.Join(m.worktreesPath(), name)
	_, err := os.Stat(worktreePath)
	return err == nil
}

func (m *Manager) SyncToMain(name string) error {
	branchName := "nexus/" + name

	mergeCmd := exec.Command("git", "checkout", "main")
	mergeCmd.Dir = m.repoRoot
	if err := mergeCmd.Run(); err != nil {
		return err
	}

	pullCmd := exec.Command("git", "pull", "origin", "main")
	pullCmd.Dir = m.repoRoot
	if err := pullCmd.Run(); err != nil {
		return err
	}

	mergeCmd = exec.Command("git", "merge", "--no-ff", "-m", "Merge worktree "+name, branchName)
	mergeCmd.Dir = m.repoRoot
	return mergeCmd.Run()
}

func (m *Manager) SyncFromMain(name string) error {
	worktreePath := filepath.Join(m.worktreesPath(), name)
	branchName := "nexus/" + name

	checkoutCmd := exec.Command("git", "checkout", branchName)
	checkoutCmd.Dir = worktreePath
	if err := checkoutCmd.Run(); err != nil {
		return err
	}

	pullCmd := exec.Command("git", "pull", "origin", "main")
	pullCmd.Dir = worktreePath
	return pullCmd.Run()
}

func (m *Manager) ListWorktrees() ([]WorktreeInfo, error) {
	worktreesPath := m.worktreesPath()
	entries, err := os.ReadDir(worktreesPath)
	if err != nil {
		return nil, err
	}

	var worktrees []WorktreeInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		worktreePath := filepath.Join(worktreesPath, name)
		worktreeInfo := WorktreeInfo{
			Name:    name,
			Path:    worktreePath,
			Branch:  "nexus/" + name,
			Exists:  true,
		}

		if _, err := os.Stat(filepath.Join(worktreePath, ".git")); err == nil {
			cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
			cmd.Dir = worktreePath
			if output, err := cmd.Output(); err == nil && len(output) > 0 {
				worktreeInfo.Branch = string(output)
			}
		}

		worktrees = append(worktrees, worktreeInfo)
	}

	return worktrees, nil
}

func (m *Manager) GetRepoRoot() string {
	return m.repoRoot
}

func (m *Manager) InitRepo() error {
	cmd := exec.Command("git", "init")
	cmd.Dir = m.repoRoot
	return cmd.Run()
}

func (m *Manager) HasGitRepo() bool {
	_, err := os.Stat(filepath.Join(m.repoRoot, ".git"))
	return err == nil
}
