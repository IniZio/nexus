package worktree

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ForkParentInput struct {
	Repo              string
	WorkspaceName     string
	LocalWorktreePath string
}

func Create(repoPath, ref, workspaceName string) (string, error) {
	base := filepath.Clean(repoPath)
	if workspaceName == "" {
		workspaceName = "workspace"
	}
	safeName := SanitizeWorktreeName(workspaceName)
	worktreesDir := filepath.Join(base, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", fmt.Errorf("create .worktrees dir: %w", err)
	}
	worktreePath := filepath.Join(worktreesDir, safeName)
	worktreePath = uniqueWorktreePath(worktreePath)

	branch := strings.TrimSpace(ref)
	if branch == "" {
		branch = safeName
	}
	branch = uniqueBranchName(base, branch)

	cmd := exec.Command("git", "-C", base, "worktree", "add", "-b", branch, worktreePath, "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		for retry := 0; retry < 5 && err != nil; retry++ {
			if strings.Contains(string(out), "already exists") {
				worktreePath = uniqueWorktreePath(worktreePath)
				branch = uniqueBranchName(base, branch)
				cmd = exec.Command("git", "-C", base, "worktree", "add", "-b", branch, worktreePath, "HEAD")
				out, err = cmd.CombinedOutput()
				continue
			}
			break
		}
		if err != nil {
			return "", fmt.Errorf("git worktree add failed: %s", strings.TrimSpace(string(out)))
		}
	}
	return worktreePath, nil
}

func CreateFork(parentWorktreePath, ref, childWorkspaceName string) (string, error) {
	parentPath := filepath.Clean(parentWorktreePath)
	worktreesDir := ForkChildrenDir(parentPath)
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", fmt.Errorf("create nested .worktrees dir: %w", err)
	}
	safeName := SanitizeWorktreeName(childWorkspaceName)
	childPath := filepath.Join(worktreesDir, safeName)
	childPath = uniqueWorktreePath(childPath)

	branch := strings.TrimSpace(ref)
	if branch == "" {
		branch = safeName
	}
	branch = uniqueBranchName(parentPath, branch)

	cmd := exec.Command("git", "-C", parentPath, "worktree", "add", "-b", branch, childPath, "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		for retry := 0; retry < 5 && err != nil; retry++ {
			if strings.Contains(string(out), "already exists") {
				childPath = uniqueWorktreePath(childPath)
				branch = uniqueBranchName(parentPath, branch)
				cmd = exec.Command("git", "-C", parentPath, "worktree", "add", "-b", branch, childPath, "HEAD")
				out, err = cmd.CombinedOutput()
				continue
			}
			break
		}
		if err != nil {
			return "", fmt.Errorf("git nested worktree add failed: %s", strings.TrimSpace(string(out)))
		}
	}
	return childPath, nil
}

func ForkChildrenDir(parentPath string) string {
	marker := string(filepath.Separator) + ".worktrees" + string(filepath.Separator)
	if idx := strings.Index(parentPath, marker); idx >= 0 {
		repoRoot := parentPath[:idx]
		return filepath.Join(repoRoot, ".worktrees")
	}
	return filepath.Join(parentPath, ".worktrees")
}

func ResolveForkBasePath(parent ForkParentInput) string {
	if parent.LocalWorktreePath != "" {
		candidate := filepath.Clean(strings.TrimSpace(parent.LocalWorktreePath))
		if !looksLikeWorktree(candidate) {
			candidate = ""
		}
		if candidate != "" {
			if looksLikeRepoRoot(candidate) {
				nested := filepath.Join(candidate, ".worktrees", SanitizeWorktreeName(parent.WorkspaceName))
				if pathExists(nested) {
					return nested
				}
			}
			return candidate
		}
	}

	if isLikelyLocalPath(parent.Repo) {
		inferred := filepath.Join(filepath.Clean(parent.Repo), ".worktrees", SanitizeWorktreeName(parent.WorkspaceName))
		if pathExists(inferred) {
			return inferred
		}
	}

	return ""
}

func looksLikeWorktree(path string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	return false
}

func looksLikeRepoRoot(path string) bool {
	if path == "" {
		return false
	}
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return true
	}
	return false
}

func pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func SanitizeWorktreeName(name string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	if n == "" {
		return "workspace"
	}
	n = strings.ReplaceAll(n, " ", "-")
	var b strings.Builder
	for _, r := range n {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "workspace"
	}
	return out
}

func uniqueBranchName(repoPath, desired string) string {
	branch := desired
	if !branchExists(repoPath, branch) {
		return branch
	}
	for i := 2; i < 500; i++ {
		candidate := fmt.Sprintf("%s-%d", desired, i)
		if !branchExists(repoPath, candidate) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", desired, time.Now().Unix())
}

func branchExists(repoPath, branch string) bool {
	// Check if the exact branch exists.
	cmd := exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if cmd.Run() == nil {
		return true
	}
	// Also check if any branch uses this name as a namespace prefix (e.g.
	// refs/heads/nexus/... exists), which would prevent creating refs/heads/nexus.
	cmd2 := exec.Command("git", "-C", repoPath, "for-each-ref", "--format=%(refname)", "refs/heads/"+branch+"/")
	out, err := cmd2.Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

func uniqueWorktreePath(desired string) string {
	if _, err := os.Stat(desired); os.IsNotExist(err) {
		return desired
	}
	for i := 2; i < 500; i++ {
		candidate := fmt.Sprintf("%s-%d", desired, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", desired, time.Now().Unix())
}

func CleanupCreatedWorktree(repoPath, worktreePath string) {
	if !isSafeWorktreeCleanupPath(repoPath, worktreePath) {
		return
	}

	cleanRepo := filepath.Clean(repoPath)
	cleanWorktree := filepath.Clean(worktreePath)
	cmd := exec.Command("git", "-C", cleanRepo, "worktree", "remove", "--force", cleanWorktree)
	_ = cmd.Run()
	_ = os.RemoveAll(cleanWorktree)
}

func isSafeWorktreeCleanupPath(repoPath, worktreePath string) bool {
	if strings.TrimSpace(repoPath) == "" || strings.TrimSpace(worktreePath) == "" {
		return false
	}
	cleanRepo := filepath.Clean(repoPath)
	cleanWorktree := filepath.Clean(worktreePath)
	worktreesRoot := filepath.Join(cleanRepo, ".worktrees")
	prefix := worktreesRoot + string(filepath.Separator)
	if cleanWorktree == worktreesRoot || !strings.HasPrefix(cleanWorktree+string(filepath.Separator), prefix) {
		return false
	}
	return true
}

func isLikelyLocalPath(repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	if isLikelyRemoteRepo(repo) {
		return false
	}
	if strings.HasPrefix(repo, "./repos/") || strings.HasPrefix(repo, "repos/") {
		return false
	}
	if strings.HasPrefix(repo, "/") || strings.HasPrefix(repo, "./") || strings.HasPrefix(repo, "../") {
		return true
	}

	if strings.HasPrefix(repo, "~/") {
		return true
	}

	if strings.Contains(repo, string(filepath.Separator)) {
		return true
	}

	if info, err := os.Stat(repo); err == nil && info.IsDir() {
		return true
	}

	return false
}

func isLikelyRemoteRepo(repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	if strings.HasPrefix(repo, "git@") || strings.HasPrefix(repo, "ssh://") {
		return true
	}
	if u, err := url.Parse(repo); err == nil && u.Scheme != "" && u.Host != "" {
		return true
	}
	if strings.Contains(repo, "@") && strings.Contains(repo, ":") {
		return true
	}
	return false
}
