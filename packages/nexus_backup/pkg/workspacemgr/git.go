package workspacemgr

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func normalizeWorkspaceRef(ref string) string {
	normalized := strings.TrimSpace(ref)
	if normalized == "" {
		return "main"
	}
	return normalized
}

func deriveRepoKind(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "unknown"
	}
	if isLikelyRemoteRepo(repo) {
		return "hosted"
	}
	if strings.HasPrefix(repo, "/") || strings.HasPrefix(repo, "./") || strings.HasPrefix(repo, "../") {
		return "local"
	}
	if strings.HasPrefix(repo, "~/") {
		return "local"
	}
	if strings.Contains(repo, string(filepath.Separator)) {
		return "local"
	}
	if info, err := os.Stat(repo); err == nil && info.IsDir() {
		return "local"
	}
	return "unknown"
}

func DeriveRepoID(repo string) string {
	return deriveRepoID(repo)
}

func deriveRepoID(repo string) string {
	normalized := strings.ToLower(strings.TrimSpace(repo))
	if normalized == "" {
		return "repo-unknown"
	}
	sum := sha1.Sum([]byte(normalized))
	return fmt.Sprintf("repo-%x", sum[:8])
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

func workspaceScopeKey(projectID, repoID string) string {
	if strings.TrimSpace(projectID) != "" {
		return "project:" + projectID
	}
	return "repo:" + repoID
}

func setupLocalWorkspaceCheckout(repoPath, workspacePath, targetRef string) error {
	repoPath = strings.TrimSpace(repoPath)
	workspacePath = strings.TrimSpace(workspacePath)
	targetRef = normalizeWorkspaceRef(targetRef)
	if repoPath == "" || workspacePath == "" {
		return nil
	}
	if !looksLikeGitRepo(repoPath) {
		return nil
	}
	if !isDirEmpty(workspacePath) {
		return fmt.Errorf("workspace path must be empty before checkout: %s", workspacePath)
	}

	startRef := targetRef
	if !localBranchExists(repoPath, targetRef) {
		startRef = "HEAD"
	}

	if _, err := runGit(repoPath, "worktree", "add", "--force", "--detach", workspacePath, startRef); err != nil {
		return err
	}
	if localBranchExists(repoPath, targetRef) {
		if _, err := runGit(workspacePath, "checkout", "--ignore-other-worktrees", targetRef); err != nil {
			cleanupLocalWorkspaceCheckout(repoPath, workspacePath)
			return err
		}
		return nil
	}
	if _, err := runGit(workspacePath, "checkout", "--ignore-other-worktrees", "-B", targetRef); err != nil {
		cleanupLocalWorkspaceCheckout(repoPath, workspacePath)
		return err
	}
	return nil
}

func setupForkLocalWorkspaceCheckout(repoPath, parentWorkspacePath, childWorkspacePath, targetRef string) error {
	if err := setupLocalWorkspaceCheckout(repoPath, childWorkspacePath, targetRef); err != nil {
		return err
	}
	parentWorkspacePath = strings.TrimSpace(parentWorkspacePath)
	if parentWorkspacePath == "" || !looksLikeGitRepo(parentWorkspacePath) {
		return nil
	}
	if err := copyDirtyStateFromParent(parentWorkspacePath, childWorkspacePath); err != nil {
		cleanupLocalWorkspaceCheckout(repoPath, childWorkspacePath)
		return err
	}
	return nil
}

func copyDirtyStateFromParent(parentWorkspacePath, childWorkspacePath string) error {
	diffOut, err := runGitRaw(parentWorkspacePath, "diff", "--binary", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(diffOut) != "" {
		if err := runGitWithInput(childWorkspacePath, diffOut, "apply", "--whitespace=nowarn", "--binary"); err != nil {
			return err
		}
	}
	return copyUntrackedFiles(parentWorkspacePath, childWorkspacePath)
}

func copyUntrackedFiles(parentWorkspacePath, childWorkspacePath string) error {
	out, err := runGitRaw(parentWorkspacePath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	if out == "" {
		return nil
	}
	paths := strings.Split(out, "\x00")
	for _, rel := range paths {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		src := filepath.Join(parentWorkspacePath, rel)
		dst := filepath.Join(childWorkspacePath, rel)
		if err := copyPath(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		_ = os.Remove(dst)
		return os.Symlink(target, dst)
	}
	if info.IsDir() {
		return os.MkdirAll(dst, info.Mode().Perm())
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

func cleanupLocalWorkspaceCheckout(repoPath, workspacePath string) {
	repoPath = strings.TrimSpace(repoPath)
	workspacePath = strings.TrimSpace(workspacePath)
	if repoPath == "" || workspacePath == "" {
		return
	}
	if looksLikeGitRepo(repoPath) {
		_, _ = runGit(repoPath, "worktree", "remove", "--force", workspacePath)
		_, _ = runGit(repoPath, "worktree", "prune")
	}
	_ = os.RemoveAll(workspacePath)
}

func runGit(dir string, args ...string) (string, error) {
	out, err := runGitRaw(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func runGitRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s failed in %s: %s", strings.Join(args, " "), dir, msg)
	}
	return stdout.String(), nil
}

func runGitWithInput(dir string, stdin string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s failed in %s: %s", strings.Join(args, " "), dir, msg)
	}
	return nil
}

func looksLikeGitRepo(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := runGit(path, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

func localBranchExists(repoPath, branch string) bool {
	if strings.TrimSpace(repoPath) == "" || strings.TrimSpace(branch) == "" {
		return false
	}
	_, err := runGit(repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+strings.TrimSpace(branch))
	return err == nil
}

func isDirEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) == 0
}
