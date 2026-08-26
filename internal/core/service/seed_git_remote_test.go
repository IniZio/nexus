package service_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// ── SSHToHTTPS unit tests ─────────────────────────────────────────────────────

func TestSSHToHTTPS_ScpStyle(t *testing.T) {
	got, ok := service.SSHToHTTPS("git@github.com:owner/repo.git")
	if !ok {
		t.Fatal("expected rewrite; got ok=false")
	}
	if got != "https://github.com/owner/repo.git" {
		t.Fatalf("want https://github.com/owner/repo.git, got %q", got)
	}
}

func TestSSHToHTTPS_SSHScheme(t *testing.T) {
	got, ok := service.SSHToHTTPS("ssh://git@github.com/owner/repo.git")
	if !ok {
		t.Fatal("expected rewrite; got ok=false")
	}
	if got != "https://github.com/owner/repo.git" {
		t.Fatalf("want https://github.com/owner/repo.git, got %q", got)
	}
}

func TestSSHToHTTPS_AlreadyHTTPS_NoRewrite(t *testing.T) {
	const url = "https://github.com/owner/repo.git"
	got, ok := service.SSHToHTTPS(url)
	if ok {
		t.Fatal("expected no rewrite for already-HTTPS URL; got ok=true")
	}
	if got != url {
		t.Fatalf("URL must be unchanged; want %q, got %q", url, got)
	}
}

func TestSSHToHTTPS_Empty_NoRewrite(t *testing.T) {
	got, ok := service.SSHToHTTPS("")
	if ok {
		t.Fatal("expected no rewrite for empty URL; got ok=true")
	}
	if got != "" {
		t.Fatalf("want empty string, got %q", got)
	}
}

func TestSSHToHTTPS_NonGitHub_NoRewrite(t *testing.T) {
	const url = "git@gitlab.com:owner/repo.git"
	got, ok := service.SSHToHTTPS(url)
	if ok {
		t.Fatal("expected no rewrite for non-GitHub SSH URL; got ok=true")
	}
	if got != url {
		t.Fatalf("URL must be unchanged; want %q, got %q", url, got)
	}
}

// ── SeedGitRemoteHTTPS integration tests ─────────────────────────────────────
//
// These tests use a real local git repository and a GuestExecer that runs
// commands on the host (no VM required). They prove the shell one-liner
// correctly rewrites SSH remotes and leaves HTTPS remotes unchanged.

// localExecer implements service.GuestExecer by running argv locally.
// Used only in tests — no production code uses this.
func localExecer(_ context.Context, _ domain.SandboxID, argv []string, stdin io.Reader) (int32, error) {
	if len(argv) == 0 {
		return -1, errors.New("empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // test helper; argv is test-controlled
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return int32(exitErr.ExitCode()), nil //nolint:gosec // exit code fits int32
		}
		return -1, err
	}
	return 0, nil
}

// initGitRepo creates a minimal git repo at dir and sets the "origin" remote
// to remoteURL. Skips t if git is not available in PATH.
func initGitRepo(t *testing.T, dir, remoteURL string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH; skipping integration test")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"remote", "add", "origin", remoteURL},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test helper
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
}

// getRemoteURL returns the current URL of the "origin" remote in dir.
func getRemoteURL(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output() //nolint:gosec // test helper
	if err != nil {
		t.Fatalf("git remote get-url origin: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestSeedGitRemoteHTTPS_ScpSSH_RewrittenToHTTPS(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir, "git@github.com:owner/repo.git")

	err := service.SeedGitRemoteHTTPS(context.Background(), domain.SandboxID{}, dir, localExecer)
	if err != nil {
		t.Fatalf("SeedGitRemoteHTTPS: %v", err)
	}

	got := getRemoteURL(t, dir)
	const want = "https://github.com/owner/repo.git"
	if got != want {
		t.Fatalf("remote URL after rewrite: want %q, got %q", want, got)
	}
}

func TestSeedGitRemoteHTTPS_SSHScheme_RewrittenToHTTPS(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir, "ssh://git@github.com/owner/repo.git")

	err := service.SeedGitRemoteHTTPS(context.Background(), domain.SandboxID{}, dir, localExecer)
	if err != nil {
		t.Fatalf("SeedGitRemoteHTTPS: %v", err)
	}

	got := getRemoteURL(t, dir)
	const want = "https://github.com/owner/repo.git"
	if got != want {
		t.Fatalf("remote URL after rewrite: want %q, got %q", want, got)
	}
}

func TestSeedGitRemoteHTTPS_AlreadyHTTPS_Idempotent(t *testing.T) {
	dir := t.TempDir()
	const url = "https://github.com/owner/repo.git"
	initGitRepo(t, dir, url)

	err := service.SeedGitRemoteHTTPS(context.Background(), domain.SandboxID{}, dir, localExecer)
	if err != nil {
		t.Fatalf("SeedGitRemoteHTTPS: %v", err)
	}

	got := getRemoteURL(t, dir)
	if got != url {
		t.Fatalf("already-HTTPS remote must be unchanged: want %q, got %q", url, got)
	}
}

func TestSeedGitRemoteHTTPS_NoOrigin_SilentNoOp(t *testing.T) {
	dir := t.TempDir()
	// init without adding any remote
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH; skipping integration test")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	// Must succeed with no error even when origin is absent.
	err := service.SeedGitRemoteHTTPS(context.Background(), domain.SandboxID{}, dir, localExecer)
	if err != nil {
		t.Fatalf("SeedGitRemoteHTTPS with no origin: %v", err)
	}
}

func TestSeedGitRemoteHTTPS_NilExecer_NoOp(t *testing.T) {
	err := service.SeedGitRemoteHTTPS(context.Background(), domain.SandboxID{}, "/some/path", nil)
	if err != nil {
		t.Fatalf("nil execer must be a no-op; got %v", err)
	}
}

func TestSeedGitRemoteHTTPS_EmptyPath_NoOp(t *testing.T) {
	called := false
	execer := func(_ context.Context, _ domain.SandboxID, _ []string, _ io.Reader) (int32, error) {
		called = true
		return 0, nil
	}
	err := service.SeedGitRemoteHTTPS(context.Background(), domain.SandboxID{}, "", execer)
	if err != nil {
		t.Fatalf("empty path must be a no-op; got %v", err)
	}
	if called {
		t.Fatal("execer must not be called when guestRepoPath is empty")
	}
}
