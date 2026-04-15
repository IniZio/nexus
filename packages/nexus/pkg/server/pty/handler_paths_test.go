package pty

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestBuildRecoveredShellOpenRequestIncludesLocalPath(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	ws := &workspacemgr.Workspace{
		ID:   "ws-root",
		Repo: repo,
	}
	info := SessionInfo{
		ID:      "pty-1",
		WorkDir: "/workspace",
	}

	req := buildRecoveredShellOpenRequest(info, ws)
	if got, _ := req["type"].(string); got != "shell.open" {
		t.Fatalf("expected shell.open type, got %q", got)
	}
	if got, _ := req["workdir"].(string); got != "/workspace" {
		t.Fatalf("expected workdir /workspace, got %q", got)
	}
	localPath, ok := req["local_path"].(string)
	if !ok || localPath == "" {
		t.Fatalf("expected local_path in recovered shell request, got %#v", req)
	}
	want := canonicalTestPath(t, repo)
	if localPath != want {
		t.Fatalf("expected local_path %q, got %q", want, localPath)
	}
}

func TestLocalWorkspacePathFromRecordPrefersLocalWorktree(t *testing.T) {
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
	}

	want := canonicalTestPath(t, local)
	if got := localWorkspacePathFromRecord(ws); got != want {
		t.Fatalf("expected local worktree path %q, got %q", want, got)
	}
}

func TestLocalWorkspacePathFromRecordFallsBackToRepo(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	ws := &workspacemgr.Workspace{
		LocalWorktreePath: filepath.Join(t.TempDir(), "missing-worktree"),
		Repo:              repo,
	}

	want := canonicalTestPath(t, repo)
	if got := localWorkspacePathFromRecord(ws); got != want {
		t.Fatalf("expected repo path fallback %q, got %q", want, got)
	}
}

func TestLocalWorkspacePathFromRecordPrefersInferredWorktree(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	inferred := filepath.Join(repo, ".worktrees", "feature-auth")
	if err := os.MkdirAll(inferred, 0o755); err != nil {
		t.Fatalf("mkdir inferred worktree: %v", err)
	}
	if err := workspacemgr.WriteHostWorkspaceMarker(inferred, "ws-inferred"); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	ws := &workspacemgr.Workspace{
		ID:            "ws-inferred",
		WorkspaceName: "hanlun-lms",
		Repo:          repo,
		Ref:           "feature/auth",
	}

	want := canonicalTestPath(t, inferred)
	if got := localWorkspacePathFromRecord(ws); got != want {
		t.Fatalf("expected inferred worktree path %q, got %q", want, got)
	}
}

func TestLocalWorkspacePathFromRecordRejectsManagedPathWithoutMarker(t *testing.T) {
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
	}

	want := canonicalTestPath(t, repo)
	if got := localWorkspacePathFromRecord(ws); got != want {
		t.Fatalf("expected repo fallback %q when marker missing, got %q", want, got)
	}
}

func TestLocalWorkDirForOpenPrefersLocalWorkspacePath(t *testing.T) {
	t.Parallel()

	local := t.TempDir()
	if err := workspacemgr.WriteHostWorkspaceMarker(local, "ws-local-open"); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	wsRecord := &workspacemgr.Workspace{
		ID:                "ws-local-open",
		LocalWorktreePath: local,
		RootPath:          filepath.Join(t.TempDir(), "instance-root"),
	}
	wsResolved, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}

	got := localWorkDirForOpen(wsRecord, wsResolved)
	want := canonicalTestPath(t, local)
	if got != want {
		t.Fatalf("expected local workdir %q, got %q", want, got)
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(path)
}
