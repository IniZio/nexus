package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type WorktreeInfo struct {
	Name   string
	Path   string
	Branch string
	Exists bool
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

func (m *Manager) ValidateWorktreeCreation(name string) error {
	if err := m.checkGitVersion(); err != nil {
		return fmt.Errorf("git version check failed: %w", err)
	}

	if !m.HasGitRepo() {
		return fmt.Errorf("not a git repository: %s", m.repoRoot)
	}

	if err := m.validateWorktreeName(name); err != nil {
		return fmt.Errorf("invalid worktree name: %w", err)
	}

	worktreePath := filepath.Join(m.worktreesPath(), name)
	if _, err := os.Stat(worktreePath); err == nil {
		return fmt.Errorf("worktree '%s' already exists at %s", name, worktreePath)
	}

	branchName := fmt.Sprintf("nexus/%s", name)
	if m.branchExists(branchName) {
		return fmt.Errorf("branch '%s' already exists", branchName)
	}

	return nil
}

func (m *Manager) checkGitVersion() error {
	cmd := exec.Command("git", "version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get git version: %w", err)
	}

	versionStr := string(output)
	if strings.Contains(versionStr, "git version") {
		parts := strings.Split(versionStr, " ")
		if len(parts) >= 3 {
			versionPart := parts[2]
			versionNumbers := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(versionPart)
			if len(versionNumbers) >= 2 {
				major := versionNumbers[1]
				minor := versionNumbers[2]
				if major == "1" || (major == "2" && minor < "15") {
					return fmt.Errorf("git version 2.15+ required, found %s", versionPart)
				}
			}
		}
	}

	return nil
}

func (m *Manager) validateWorktreeName(name string) error {
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

func (m *Manager) branchExists(branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branchName)
	cmd.Dir = m.repoRoot
	if err := cmd.Run(); err == nil {
		return true
	}

	cmd = exec.Command("git", "branch", "--list", branchName)
	cmd.Dir = m.repoRoot
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		return true
	}

	return false
}

func (m *Manager) isValidWorktree(name string) bool {
	worktreePath := filepath.Join(m.worktreesPath(), name)

	gitFile := filepath.Join(worktreePath, ".git")
	if _, err := os.Stat(gitFile); os.IsNotExist(err) {
		return false
	}

	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = worktreePath
	if err := cmd.Run(); err != nil {
		return false
	}

	branchName := fmt.Sprintf("nexus/%s", name)
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = worktreePath
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	actualBranch := strings.TrimSpace(string(output))
	if actualBranch != branchName {
		return false
	}

	return true
}

func (m *Manager) CreateOrRecreateWorktree(name string) (string, error) {
	if m.WorktreeExists(name) {
		if m.isValidWorktree(name) {
			return m.GetWorktreePath(name), nil
		}

		worktreePath := filepath.Join(m.worktreesPath(), name)
		branchName := "nexus/" + name

		gitFile := filepath.Join(worktreePath, ".git")
		if _, err := os.Stat(gitFile); os.IsNotExist(err) {
			pruneCmd := exec.Command("git", "worktree", "prune")
			pruneCmd.Dir = m.repoRoot
			pruneCmd.Run()

			deleteCmd := exec.Command("git", "branch", "-D", branchName)
			deleteCmd.Dir = m.repoRoot
			deleteCmd.Run()

			os.RemoveAll(worktreePath)
		} else {
			if err := m.RemoveWorktree(name); err != nil {
				return "", fmt.Errorf("failed to remove broken worktree: %w", err)
			}
		}
	}

	return m.CreateWorktree(name)
}

func (m *Manager) CreateWorktree(name string) (string, error) {
	worktreesPath := m.worktreesPath()
	if err := os.MkdirAll(worktreesPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create worktrees directory: %w", err)
	}

	worktreePath := filepath.Join(worktreesPath, name)
	branchName := "nexus/" + name

	if m.WorktreeExists(name) {
		return "", fmt.Errorf("worktree '%s' already exists at %s", name, worktreePath)
	}

	if m.branchExists(branchName) {
		return "", fmt.Errorf("branch '%s' already exists", branchName)
	}

	dirExists, err := pathExists(worktreePath)
	if err != nil {
		return "", fmt.Errorf("failed to check worktree path: %w", err)
	}
	if dirExists {
		return "", fmt.Errorf("directory '%s' already exists but is not a valid worktree", worktreePath)
	}

	cmd := exec.Command("git", "worktree", "add", worktreePath, "-b", branchName)
	cmd.Dir = m.repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add failed (exit status %d): %w\nOutput: %s", getExitCode(err), err, string(output))
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", fmt.Errorf("worktree directory was not created at %s", worktreePath)
	}

	return worktreePath, nil
}

func getExitCode(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return -1
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
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
		gitFile := filepath.Join(worktreePath, ".git")
		if _, statErr := os.Stat(gitFile); os.IsNotExist(statErr) {
			os.RemoveAll(worktreePath)
		} else {
			return fmt.Errorf("failed to remove worktree: %w", err)
		}
	}

	deleteCmd := exec.Command("git", "branch", "-D", branchName)
	deleteCmd.Dir = m.repoRoot
	if err := deleteCmd.Run(); err != nil {
	}

	return nil
}

func (m *Manager) GetWorktreePath(name string) string {
	return filepath.Join(m.worktreesPath(), name)
}

func (m *Manager) WorktreeExists(name string) bool {
	worktreePath := filepath.Join(m.worktreesPath(), name)
	exists, _ := pathExists(worktreePath)
	return exists
}

func (m *Manager) SyncToMain(name string) error {
	branchName := "nexus/" + name

	mergeCmd := exec.Command("git", "checkout", "main")
	mergeCmd.Dir = m.repoRoot
	if err := mergeCmd.Run(); err != nil {
		return fmt.Errorf("failed to checkout main: %w", err)
	}

	pullCmd := exec.Command("git", "pull", "origin", "main")
	pullCmd.Dir = m.repoRoot
	if err := pullCmd.Run(); err != nil {
		return fmt.Errorf("failed to pull main: %w", err)
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
		return fmt.Errorf("failed to checkout branch: %w", err)
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
			Name:   name,
			Path:   worktreePath,
			Branch: "nexus/" + name,
			Exists: true,
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
