package cli

// Tests for the build-context capture mechanism used by "nexus3 sandbox
// create --file" (D-DC-08, S-SANDBOX-CLI-CAPTURE).
//
// These tests call builder.WorktreeToDisk — the function that cmd_sandbox.go
// now delegates to — directly, so they exercise the exact code path the
// production command uses. They skip when the required host tools (mke2fs,
// debugfs) are unavailable, and they do NOT require KVM.
//
// The builder package's own worktreedisk_test.go already tests the internal
// capture mechanics (exclusion logic, size guard, etc.). The tests here are
// focused on the cases that matter from the CLI's perspective:
//
//   - A dirty (uncommitted) edit to a tracked file is captured.
//   - An untracked file is captured.
//   - A path excluded by .dockerignore is NOT captured.
//   - A directory with no git repo at all (and a git repo with no commits)
//     succeeds — the old "git archive HEAD" path hard-failed here.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder"
)

// skipUnlessCaptureDeps skips t if mke2fs or debugfs are unavailable. Both
// tools are part of e2fsprogs; if the package is installed both are present.
func skipUnlessCaptureDeps(t *testing.T) {
	t.Helper()
	if !builder.Mke2fsAvailable() {
		t.Skip("mke2fs not available; install e2fsprogs to run capture tests")
	}
	if _, err := exec.LookPath("debugfs"); err != nil {
		t.Skip("debugfs not available; install e2fsprogs to run capture tests")
	}
}

// debugfsReadFile reads the content of absGuestPath from ext4Image using
// debugfs. It fails the test if debugfs errors.
func debugfsReadFile(t *testing.T, ext4Image, absGuestPath string) string {
	t.Helper()
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "debugfs", "-R", "cat "+absGuestPath, ext4Image)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat %s: %v\n%s", absGuestPath, err, out)
	}
	return string(out)
}

// debugfsListRoot returns the debugfs "ls /" output for ext4Image.
func debugfsListRoot(t *testing.T, ext4Image string) string {
	t.Helper()
	ctx := context.Background()
	out, err := exec.CommandContext(ctx, "debugfs", "-R", "ls /", ext4Image).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs ls /: %v\n%s", err, out)
	}
	return string(out)
}

// TestSandboxCapture_DirtyEditCaptured verifies that an uncommitted edit to a
// tracked file appears in the build-context ext4. Under the old "git archive
// HEAD" path this was silently dropped; WorktreeToDisk reads the filesystem
// directly so the dirty on-disk bytes must be present.
func TestSandboxCapture_DirtyEditCaptured(t *testing.T) {
	skipUnlessCaptureDeps(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available; skipping dirty-edit capture test")
	}

	ctx := context.Background()
	srcDir := t.TempDir()

	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.invalid",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.invalid",
		"HOME="+srcDir,
	)
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = srcDir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	gitRun("init")
	gitRun("config", "user.email", "test@test.invalid")
	gitRun("config", "user.name", "test")

	trackFile := filepath.Join(srcDir, "tracked.txt")
	must(t, os.WriteFile(trackFile, []byte("original\n"), 0o644))
	gitRun("add", "tracked.txt")
	gitRun("commit", "-m", "initial")

	// Dirty edit: overwrite with new content without staging or committing.
	must(t, os.WriteFile(trackFile, []byte("dirty content\n"), 0o644))

	outExt4 := filepath.Join(t.TempDir(), "ctx.ext4")
	if err := builder.WorktreeToDisk(ctx, srcDir, outExt4, 0 /* auto */); err != nil {
		t.Fatalf("WorktreeToDisk: %v", err)
	}

	got := debugfsReadFile(t, outExt4, "/tracked.txt")
	if !strings.Contains(got, "dirty content") {
		t.Errorf("dirty edit not captured; got %q (want 'dirty content')", got)
	}
	if strings.Contains(got, "original") && !strings.Contains(got, "dirty") {
		t.Errorf("captured HEAD instead of dirty on-disk state; got %q", got)
	}
}

// TestSandboxCapture_UntrackedFileCaptured verifies that a file that exists on
// disk but has never been git-tracked appears in the build-context ext4.
// "git archive HEAD" silently omitted such files; WorktreeToDisk does not.
func TestSandboxCapture_UntrackedFileCaptured(t *testing.T) {
	skipUnlessCaptureDeps(t)

	ctx := context.Background()
	srcDir := t.TempDir()

	// No git init — plain directory. The untracked file must still be captured.
	must(t, os.WriteFile(filepath.Join(srcDir, "untracked.txt"), []byte("untracked content\n"), 0o644))

	outExt4 := filepath.Join(t.TempDir(), "ctx.ext4")
	if err := builder.WorktreeToDisk(ctx, srcDir, outExt4, 0 /* auto */); err != nil {
		t.Fatalf("WorktreeToDisk: %v", err)
	}

	got := debugfsReadFile(t, outExt4, "/untracked.txt")
	if !strings.Contains(got, "untracked content") {
		t.Errorf("untracked.txt not captured; debugfs output: %q", got)
	}
}

// TestSandboxCapture_DockerignoreExcludes verifies that a path matched by
// .dockerignore is NOT included in the build-context ext4.
func TestSandboxCapture_DockerignoreExcludes(t *testing.T) {
	skipUnlessCaptureDeps(t)

	ctx := context.Background()
	srcDir := t.TempDir()

	// dist/bundle.js should be excluded; main.go should be included.
	distDir := filepath.Join(srcDir, "dist")
	must(t, os.MkdirAll(distDir, 0o755))
	must(t, os.WriteFile(filepath.Join(distDir, "bundle.js"), []byte("build output\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(srcDir, ".dockerignore"), []byte("dist\n"), 0o644))

	outExt4 := filepath.Join(t.TempDir(), "ctx.ext4")
	if err := builder.WorktreeToDisk(ctx, srcDir, outExt4, 0 /* auto */); err != nil {
		t.Fatalf("WorktreeToDisk: %v", err)
	}

	lsOut := debugfsListRoot(t, outExt4)
	if strings.Contains(lsOut, "dist") {
		t.Errorf("dist/ should be excluded by .dockerignore but appears in root listing:\n%s", lsOut)
	}

	// main.go must still be present.
	got := debugfsReadFile(t, outExt4, "/main.go")
	if !strings.Contains(got, "package main") {
		t.Errorf("main.go not captured; debugfs output: %q", got)
	}
}

// TestSandboxCapture_NoCommitsSucceeds verifies that WorktreeToDisk succeeds
// for a git repository that has been initialised but has no commits. Under the
// old approach, "git archive HEAD" failed fatally in this case. The new path
// reads the filesystem directly and has no such requirement.
func TestSandboxCapture_NoCommitsSucceeds(t *testing.T) {
	skipUnlessCaptureDeps(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available; skipping no-commits test")
	}

	ctx := context.Background()
	srcDir := t.TempDir()

	// Initialise a git repo but make no commits.
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// Add a file to the working tree (staged or not — either way unborn HEAD).
	must(t, os.WriteFile(filepath.Join(srcDir, "pending.txt"), []byte("not yet committed\n"), 0o644))

	outExt4 := filepath.Join(t.TempDir(), "ctx.ext4")
	if err := builder.WorktreeToDisk(ctx, srcDir, outExt4, 0 /* auto */); err != nil {
		t.Fatalf("WorktreeToDisk with no commits: %v", err)
	}

	// The file must appear in the image.
	got := debugfsReadFile(t, outExt4, "/pending.txt")
	if !strings.Contains(got, "not yet committed") {
		t.Errorf("pending.txt not captured from no-commit repo; debugfs output: %q", got)
	}
}
