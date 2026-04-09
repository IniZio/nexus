package localws

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateWorktree_RecreatesWhenPathExistsButIsNotGitWorktree(t *testing.T) {
	repo := initLocalWSBareRepo(t)
	worktreeRoot := t.TempDir()

	m, err := NewManager(Config{
		WorktreeRoot: worktreeRoot,
		RepoCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	stalePath := filepath.Join(worktreeRoot, "alpha")
	if err := os.MkdirAll(stalePath, 0o755); err != nil {
		t.Fatalf("mkdir stale path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stalePath, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	cacheDir, err := m.ensureRepoCacheDir(context.Background(), repo)
	if err != nil {
		t.Fatalf("ensure repo cache: %v", err)
	}

	path, err := m.createWorktree(context.Background(), cacheDir, "alpha", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if path != stalePath {
		t.Fatalf("expected worktree path %q, got %q", stalePath, path)
	}

	if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
		t.Fatalf("expected recreated git worktree at %q: %v", path, statErr)
	}
}

func initLocalWSBareRepo(t *testing.T) string {
	t.Helper()

	src := t.TempDir()
	runGitForLocalWS(t, src, "init")
	runGitForLocalWS(t, src, "config", "user.email", "nexus-localws-tests@example.com")
	runGitForLocalWS(t, src, "config", "user.name", "Nexus LocalWS Tests")

	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("localws test repo\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGitForLocalWS(t, src, "add", "README.md")
	runGitForLocalWS(t, src, "commit", "-m", "initial commit")

	bareParent := t.TempDir()
	bare := filepath.Join(bareParent, "repo.git")
	runGitForLocalWS(t, bareParent, "clone", "--bare", src, bare)

	return bare
}

func runGitForLocalWS(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}
