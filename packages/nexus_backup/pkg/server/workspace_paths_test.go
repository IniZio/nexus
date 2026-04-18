package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestPreferredWorkspaceRootPrefersLocalWorktree(t *testing.T) {
	t.Parallel()

	local := t.TempDir()
	repo := t.TempDir()
	root := t.TempDir()
	if err := workspacemgr.WriteHostWorkspaceMarker(local, "ws-local"); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	ws := &workspacemgr.Workspace{
		ID:                "ws-local",
		LocalWorktreePath: local,
		Repo:              repo,
		RootPath:          root,
	}

	want := canonicalTestPath(t, local)
	if got := preferredWorkspaceRoot(ws); got != want {
		t.Fatalf("expected local worktree path %q, got %q", want, got)
	}
}

func TestPreferredWorkspaceRootFallsBackToRepoWhenLocalMissing(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	root := t.TempDir()
	ws := &workspacemgr.Workspace{
		LocalWorktreePath: filepath.Join(t.TempDir(), "missing-worktree"),
		Repo:              repo,
		RootPath:          root,
	}

	want := canonicalTestPath(t, repo)
	if got := preferredWorkspaceRoot(ws); got != want {
		t.Fatalf("expected repo path %q fallback, got %q", want, got)
	}
}

func TestPreferredWorkspaceRootPrefersInferredWorktreeFromRepo(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	worktree := filepath.Join(repo, ".worktrees", "feature-auth")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir inferred worktree: %v", err)
	}
	if err := workspacemgr.WriteHostWorkspaceMarker(worktree, "ws-inferred"); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	root := t.TempDir()
	ws := &workspacemgr.Workspace{
		ID:            "ws-inferred",
		WorkspaceName: "hanlun-lms",
		Repo:          repo,
		Ref:           "feature/auth",
		RootPath:      root,
	}

	want := canonicalTestPath(t, worktree)
	if got := preferredWorkspaceRoot(ws); got != want {
		t.Fatalf("expected inferred worktree path %q, got %q", want, got)
	}
}

func TestPreferredWorkspaceRootCanonicalizesSymlinkRepo(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	realRepo := filepath.Join(base, "real-repo")
	if err := os.MkdirAll(realRepo, 0o755); err != nil {
		t.Fatalf("mkdir real repo: %v", err)
	}
	linkRepo := filepath.Join(base, "repo-link")
	if err := os.Symlink(realRepo, linkRepo); err != nil {
		t.Fatalf("create symlink repo: %v", err)
	}
	inferred := filepath.Join(realRepo, ".worktrees", "feature-auth")
	if err := os.MkdirAll(inferred, 0o755); err != nil {
		t.Fatalf("mkdir inferred worktree: %v", err)
	}
	if err := workspacemgr.WriteHostWorkspaceMarker(inferred, "ws-inferred"); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	ws := &workspacemgr.Workspace{
		ID:            "ws-inferred",
		WorkspaceName: "hanlun-lms",
		Repo:          linkRepo,
		Ref:           "feature/auth",
		RootPath:      t.TempDir(),
	}

	want := canonicalTestPath(t, inferred)
	if got := preferredWorkspaceRoot(ws); got != want {
		t.Fatalf("expected canonical inferred worktree %q, got %q", want, got)
	}
}

func TestPreferredWorkspaceRootRejectsManagedPathWithoutMarker(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	missingMarker := filepath.Join(repo, ".worktrees", "feature-auth")
	if err := os.MkdirAll(missingMarker, 0o755); err != nil {
		t.Fatalf("mkdir managed path: %v", err)
	}
	ws := &workspacemgr.Workspace{
		ID:                "ws-no-marker",
		LocalWorktreePath: missingMarker,
		Repo:              repo,
		Ref:               "feature/auth",
		RootPath:          t.TempDir(),
	}

	want := canonicalTestPath(t, repo)
	if got := preferredWorkspaceRoot(ws); got != want {
		t.Fatalf("expected fallback repo %q when marker missing, got %q", want, got)
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(path)
}
