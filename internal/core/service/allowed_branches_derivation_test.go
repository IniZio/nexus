package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
)

// initTestGitRepo creates a throwaway git repo at t.TempDir(), commits one
// file, and leaves HEAD checked out on branch. Skips the test if git is not
// on PATH (this is a hermetic, VM-free helper — no cloud-hypervisor
// involved).
func initTestGitRepo(t *testing.T, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "init")
	if branch != "main" {
		run("checkout", "-q", "-b", branch)
	}
	return dir
}

// ── resolveAllowedBranches unit tests ──────────────────────────────────────

func TestResolveAllowedBranches_ExplicitOverride(t *testing.T) {
	explicit := []string{"refs/heads/feature/*"}
	got := resolveAllowedBranches(CreateAndBootOptions{
		AllowedBranches: explicit,
		Workspace:       &WorkspaceSpec{SourcePath: initTestGitRepo(t, "unrelated-branch")},
	})
	if len(got) != 1 || got[0] != explicit[0] {
		t.Errorf("resolveAllowedBranches() = %v; want explicit override %v unchanged", got, explicit)
	}
}

func TestResolveAllowedBranches_WorkspaceBranchDerived(t *testing.T) {
	repo := initTestGitRepo(t, "newman/han-744-student-course-selector")
	got := resolveAllowedBranches(CreateAndBootOptions{
		Workspace: &WorkspaceSpec{SourcePath: repo},
	})
	want := []string{"refs/heads/newman/han-744-student-course-selector"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("resolveAllowedBranches() = %v; want %v", got, want)
	}
}

func TestResolveAllowedBranches_LiveMountBranchDerived(t *testing.T) {
	repo := initTestGitRepo(t, "nexus3/some-slice")
	got := resolveAllowedBranches(CreateAndBootOptions{
		LiveMounts: []domain.LiveMount{{HostPath: repo, GuestPath: "/workspace"}},
	})
	want := []string{"refs/heads/nexus3/some-slice"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("resolveAllowedBranches() = %v; want %v", got, want)
	}
}

// TestResolveAllowedBranches_DetachedHead_FailsClosed proves the fail-closed
// path: a workspace IS bound, but its branch cannot be resolved (detached
// HEAD), so the sentinel is returned rather than nil (which would silently
// re-apply the nexus3-only default) or an empty slice (allow-all).
//
// Mutation evidence: see TestResolveAllowedBranches_MutationProof_FailOpen
// below, which flips the `if err != nil` branch and shows it going RED.
func TestResolveAllowedBranches_DetachedHead_FailsClosed(t *testing.T) {
	repo := initTestGitRepo(t, "main")
	cmd := exec.Command("git", "-C", repo, "checkout", "-q", "--detach", "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}
	got := resolveAllowedBranches(CreateAndBootOptions{
		Workspace: &WorkspaceSpec{SourcePath: repo},
	})
	if len(got) != 1 || got[0] != domain.UnresolvedBranchSentinel {
		t.Errorf("resolveAllowedBranches() on detached HEAD = %v; want [%q] (fail-closed sentinel)",
			got, domain.UnresolvedBranchSentinel)
	}
}

// TestResolveAllowedBranches_NotAGitRepo_FailsClosed covers the other
// derivation-failure input: a workspace path bound at create time that is
// not a git repository at all (e.g. a plain --workspace directory).
func TestResolveAllowedBranches_NotAGitRepo_FailsClosed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir() // deliberately never `git init`'d
	got := resolveAllowedBranches(CreateAndBootOptions{
		Workspace: &WorkspaceSpec{SourcePath: dir},
	})
	if len(got) != 1 || got[0] != domain.UnresolvedBranchSentinel {
		t.Errorf("resolveAllowedBranches() on non-git dir = %v; want [%q] (fail-closed sentinel)",
			got, domain.UnresolvedBranchSentinel)
	}
}

// TestResolveAllowedBranches_NoWorkspace_LegacyDefault verifies that a
// create call with no bound workspace at all (no Workspace, no LiveMounts)
// leaves AllowedBranches nil, unchanged from today's behaviour — there is no
// worktree to derive a branch from, and Envelope.ResolvedAllowedBranches
// applies its own default at boot time.
func TestResolveAllowedBranches_NoWorkspace_LegacyDefault(t *testing.T) {
	got := resolveAllowedBranches(CreateAndBootOptions{})
	if got != nil {
		t.Errorf("resolveAllowedBranches() with no workspace = %v; want nil (legacy default deferred to ResolvedAllowedBranches)", got)
	}
}

// TestResolveAllowedBranches_MutationProof_FailOpen simulates the bug this
// slice fixes reappearing: a derivation-failure path that returns nil
// (fail-open, re-triggering the nexus3-only default) instead of the
// sentinel. It calls hostWorktreeBranch directly and asserts the exact
// failure mode resolveAllowedBranches must not paper over.
//
// This exists so the RED output can be captured without hand-editing
// production code: the assertion below is what breaks if someone "fixes" a
// git error by falling back to `return nil, nil` in hostWorktreeBranch.
func TestResolveAllowedBranches_MutationProof_FailOpen(t *testing.T) {
	repo := initTestGitRepo(t, "main")
	cmd := exec.Command("git", "-C", repo, "checkout", "-q", "--detach", "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}
	branch, err := hostWorktreeBranch(repo)
	if err == nil {
		t.Fatalf("hostWorktreeBranch(detached HEAD) = %q, nil error; want a non-nil error", branch)
	}
}

// ── CreateAndBoot end-to-end plumbing ───────────────────────────────────────

// TestCreateAndBoot_WorktreeWorkspace_DerivesAllowedBranches proves the full
// path: a worktree-sandbox-shaped create call (LiveMounts at /workspace,
// mirroring herdrWorktreeSandbox) results in Envelope.AllowedBranches
// containing exactly the worktree's own branch — this is what AC-1 requires
// (that branch is now pushable) and, combined with the mitm-layer sentinel
// tests, what AC-2 requires (nothing else is).
func TestCreateAndBoot_WorktreeWorkspace_DerivesAllowedBranches(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	repo := initTestGitRepo(t, "newman/han-744-student-course-selector")

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "worktree-derive",
		CreateAndBootOptions{
			Image:      ImageSpec{Digest: string(img.Digest)},
			CacheRoot:  cacheRoot,
			LiveMounts: []domain.LiveMount{{HostPath: repo, GuestPath: "/workspace"}},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	want := []string{"refs/heads/newman/han-744-student-course-selector"}
	if len(sb.Envelope.AllowedBranches) != 1 || sb.Envelope.AllowedBranches[0] != want[0] {
		t.Errorf("Envelope.AllowedBranches = %v; want %v", sb.Envelope.AllowedBranches, want)
	}
}

// TestCreateAndBoot_DetachedWorkspace_DeniesAllInEnvelope proves AC-2's
// negative half at the Envelope layer: a bound-but-unresolvable workspace
// never reaches CreateAndBoot's caller with a permissive Envelope.
func TestCreateAndBoot_DetachedWorkspace_DeniesAllInEnvelope(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	repo := initTestGitRepo(t, "main")
	cmd := exec.Command("git", "-C", repo, "checkout", "-q", "--detach", "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "worktree-detached",
		CreateAndBootOptions{
			Image:      ImageSpec{Digest: string(img.Digest)},
			CacheRoot:  cacheRoot,
			LiveMounts: []domain.LiveMount{{HostPath: repo, GuestPath: "/workspace"}},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if len(sb.Envelope.AllowedBranches) != 1 || sb.Envelope.AllowedBranches[0] != domain.UnresolvedBranchSentinel {
		t.Errorf("Envelope.AllowedBranches = %v; want [%q] (fail-closed)",
			sb.Envelope.AllowedBranches, domain.UnresolvedBranchSentinel)
	}
}
