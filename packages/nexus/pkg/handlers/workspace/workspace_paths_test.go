package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestPreferredProjectRootForRuntimePrefersLocalWorktree(t *testing.T) {
	t.Parallel()

	local := t.TempDir()
	repo := t.TempDir()
	if err := workspacemgr.WriteHostWorkspaceMarker(local, "ws-local"); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	ws := &workspacemgr.Workspace{
		ID:                "ws-local",
		LocalWorktreePath: local,
		Repo:              repo,
		WorkspaceName:     "hanlun-lms",
	}

	want := canonicalPathForTest(t, local)
	if got := preferredProjectRootForRuntime(ws); got != want {
		t.Fatalf("expected local worktree %q, got %q", want, got)
	}
}

func TestPreferredProjectRootForRuntimePrefersInferredWorktree(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	inferred := filepath.Join(repo, ".worktrees", "feature-auth")
	if err := os.MkdirAll(inferred, 0o755); err != nil {
		t.Fatalf("mkdir inferred worktree: %v", err)
	}
	if err := workspacemgr.WriteHostWorkspaceMarker(inferred, "ws-test"); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	ws := &workspacemgr.Workspace{
		Repo:          repo,
		ID:            "ws-test",
		Ref:           "feature/auth",
		WorkspaceName: "hanlun-lms",
	}

	want := canonicalPathForTest(t, inferred)
	if got := preferredProjectRootForRuntime(ws); got != want {
		t.Fatalf("expected inferred worktree %q, got %q", want, got)
	}
}

func TestPreferredProjectRootForRuntimeCanonicalizesSymlinkRepo(t *testing.T) {
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
	if err := workspacemgr.WriteHostWorkspaceMarker(inferred, "ws-test"); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	ws := &workspacemgr.Workspace{
		Repo:          linkRepo,
		ID:            "ws-test",
		Ref:           "feature/auth",
		WorkspaceName: "hanlun-lms",
	}

	want := canonicalPathForTest(t, inferred)
	if got := preferredProjectRootForRuntime(ws); got != want {
		t.Fatalf("expected canonical inferred worktree %q, got %q", want, got)
	}
}

func TestPreferredProjectRootForRuntimeRejectsManagedPathWithoutMarker(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	missingMarker := filepath.Join(repo, ".worktrees", "feature-auth")
	if err := os.MkdirAll(missingMarker, 0o755); err != nil {
		t.Fatalf("mkdir managed path: %v", err)
	}
	ws := &workspacemgr.Workspace{
		ID:            "ws-no-marker",
		Repo:          repo,
		Ref:           "feature/auth",
		WorkspaceName: "hanlun-lms",
	}

	want := canonicalPathForTest(t, repo)
	if got := preferredProjectRootForRuntime(ws); got != want {
		t.Fatalf("expected repo fallback %q when marker missing, got %q", want, got)
	}
}

func canonicalPathForTest(t *testing.T, path string) string {
	t.Helper()
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(path)
}
