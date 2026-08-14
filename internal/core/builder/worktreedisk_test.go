package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/moby/patternmatcher"
)

// TestWorktreeToDisk_UntrackedFileCaptured verifies that a file present on
// disk but never committed to git IS captured by WorktreeToDisk. This is the
// key difference from "git archive HEAD", which only exports tracked content.
func TestWorktreeToDisk_UntrackedFileCaptured(t *testing.T) {
	skipIfInGuest(t)
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not available; skipping worktree untracked-file test")
	}
	if !debugfsAvailable() {
		t.Skip("debugfs not available; skipping worktree untracked-file test")
	}

	ctx := context.Background()
	srcDir := t.TempDir()

	// untracked.txt is never committed; WorktreeToDisk must still capture it.
	if err := os.WriteFile(filepath.Join(srcDir, "untracked.txt"), []byte("untracked content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	if err := WorktreeToDisk(ctx, srcDir, outExt4, DefaultCaptureMaxBytes); err != nil {
		t.Fatalf("WorktreeToDisk: %v", err)
	}

	out, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /untracked.txt", outExt4).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat /untracked.txt: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "untracked content") {
		t.Errorf("untracked.txt not captured or content wrong; debugfs output: %q", string(out))
	}
}

// TestWorktreeToDisk_DirtyEditCaptured verifies that an uncommitted edit to a
// tracked file IS captured. WorktreeToDisk reads from the filesystem, not from
// git HEAD, so the dirty on-disk content must appear in the image.
func TestWorktreeToDisk_DirtyEditCaptured(t *testing.T) {
	skipIfInGuest(t)
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not available; skipping worktree dirty-edit test")
	}
	if !debugfsAvailable() {
		t.Skip("debugfs not available; skipping worktree dirty-edit test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available; skipping worktree dirty-edit test")
	}

	ctx := context.Background()
	srcDir := t.TempDir()

	// Set up a minimal git repo with one committed file.
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = srcDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.invalid",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.invalid",
			"HOME="+srcDir, // prevent picking up real ~/.gitconfig author settings
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init")
	gitRun("config", "user.email", "test@test.invalid")
	gitRun("config", "user.name", "test")

	// Commit the file with "original" content.
	trackFile := filepath.Join(srcDir, "tracked.txt")
	if err := os.WriteFile(trackFile, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "tracked.txt")
	gitRun("commit", "-m", "initial commit")

	// Overwrite with dirty content (uncommitted edit).
	if err := os.WriteFile(trackFile, []byte("dirty content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	if err := WorktreeToDisk(ctx, srcDir, outExt4, DefaultCaptureMaxBytes); err != nil {
		t.Fatalf("WorktreeToDisk: %v", err)
	}

	out, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /tracked.txt", outExt4).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat /tracked.txt: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "dirty content") {
		t.Errorf("dirty edit not captured; got %q (want 'dirty content')", got)
	}
	// Confirm we did NOT capture the committed content instead.
	if strings.Contains(got, "original") && !strings.Contains(got, "dirty") {
		t.Errorf("captured HEAD instead of dirty state; got %q", got)
	}
}

// TestWorktreeToDisk_DockerignoreExcludes verifies that paths matched by the
// project's .dockerignore are NOT captured.
func TestWorktreeToDisk_DockerignoreExcludes(t *testing.T) {
	skipIfInGuest(t)
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not available; skipping worktree dockerignore test")
	}
	if !debugfsAvailable() {
		t.Skip("debugfs not available; skipping worktree dockerignore test")
	}

	ctx := context.Background()
	srcDir := t.TempDir()

	// keep.txt — must appear in the image.
	if err := os.WriteFile(filepath.Join(srcDir, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dist/bundle.js — excluded via .dockerignore.
	if err := os.MkdirAll(filepath.Join(srcDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "dist", "bundle.js"), []byte("bundle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write .dockerignore excluding dist.
	if err := os.WriteFile(filepath.Join(srcDir, ".dockerignore"), []byte("dist\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	if err := WorktreeToDisk(ctx, srcDir, outExt4, DefaultCaptureMaxBytes); err != nil {
		t.Fatalf("WorktreeToDisk: %v", err)
	}

	// keep.txt must be present.
	out, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /keep.txt", outExt4).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat /keep.txt: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "keep") {
		t.Errorf("keep.txt missing from image; debugfs output: %q", string(out))
	}

	// dist directory must be absent from the root.
	lsOut, _ := exec.CommandContext(ctx, "debugfs", "-R", "ls /", outExt4).CombinedOutput()
	if strings.Contains(string(lsOut), "dist") {
		t.Errorf("dist/ should be excluded by .dockerignore but appears in root listing: %s", lsOut)
	}
}

// TestWorktreeToDisk_NexusAlwaysExclude verifies that paths in
// [nexus3AlwaysExclude] (.claude, .agents, .groundwork, .pnpm-store) are NOT
// captured even when no .dockerignore is present.
func TestWorktreeToDisk_NexusAlwaysExclude(t *testing.T) {
	skipIfInGuest(t)
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not available; skipping worktree always-exclude test")
	}
	if !debugfsAvailable() {
		t.Skip("debugfs not available; skipping worktree always-exclude test")
	}

	ctx := context.Background()
	srcDir := t.TempDir()

	// A regular source file that must appear.
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// .claude is in nexus3AlwaysExclude; must be excluded without any .dockerignore.
	claudeDir := filepath.Join(srcDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	if err := WorktreeToDisk(ctx, srcDir, outExt4, DefaultCaptureMaxBytes); err != nil {
		t.Fatalf("WorktreeToDisk: %v", err)
	}

	lsOut, _ := exec.CommandContext(ctx, "debugfs", "-R", "ls /", outExt4).CombinedOutput()
	if strings.Contains(string(lsOut), ".claude") {
		t.Errorf(".claude should be excluded (nexus3AlwaysExclude) but appears in root listing: %s", lsOut)
	}

	// main.go must be present.
	out, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /main.go", outExt4).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat /main.go: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "package main") {
		t.Errorf("main.go missing from image; debugfs output: %q", string(out))
	}
}

// TestWorktreeToDisk_SizeGuardTrips verifies that WorktreeToDisk returns an
// actionable error — before writing any ext4 image — when included content
// exceeds the maxBytes threshold. This test does NOT require mke2fs or KVM.
func TestWorktreeToDisk_SizeGuardTrips(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()

	// Write files totalling well above a small threshold.
	// big.bin: 25 bytes in the root; node_modules/pkg.js: 5 bytes.
	if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), []byte("0123456789012345678901234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "node_modules", "pkg.js"), []byte("01234"), 0o644); err != nil {
		t.Fatal(err)
	}

	const threshold int64 = 10 // well below total (30 bytes)
	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err := WorktreeToDisk(ctx, srcDir, outExt4, threshold)
	if err == nil {
		t.Fatal("expected size-guard error, got nil")
	}

	msg := err.Error()
	t.Logf("size-guard error: %s", msg)

	if !strings.Contains(msg, "limit") {
		t.Errorf("size-guard error should mention the limit; got: %q", msg)
	}
	if !strings.Contains(msg, ".dockerignore") {
		t.Errorf("size-guard error should suggest .dockerignore; got: %q", msg)
	}
	// Should name at least one of the large contributors.
	if !strings.Contains(msg, "node_modules") && !strings.Contains(msg, "(root files)") {
		t.Errorf("size-guard error should name offending directories; got: %q", msg)
	}

	// Confirm no ext4 file was written (fail-fast guarantee).
	if _, statErr := os.Stat(outExt4); statErr == nil {
		t.Error("ext4 image should not exist after size-guard rejection")
	}
}

// TestFilteredWorktreeDir_CrossDeviceRefuses verifies that filteredWorktreeDir
// returns a clear, actionable error — rather than silently falling back to a
// large file copy — when the staging directory is on a different device than
// the source tree. The test uses /dev/shm (a tmpfs) as the staging base; it
// is skipped when /dev/shm is unavailable or happens to be on the same device.
func TestFilteredWorktreeDir_CrossDeviceRefuses(t *testing.T) {
	// Check /dev/shm availability.
	shmStat, err := os.Stat("/dev/shm")
	if err != nil || !shmStat.IsDir() {
		t.Skip("/dev/shm not available or not a directory; skipping cross-device staging test")
	}

	srcDir := t.TempDir()

	// Compare device IDs: skip if they're already on the same device.
	srcSysStat, err := os.Stat(srcDir)
	if err != nil {
		t.Fatalf("stat srcDir: %v", err)
	}
	srcDev := srcSysStat.Sys().(*syscall.Stat_t).Dev
	shmDev := shmStat.Sys().(*syscall.Stat_t).Dev
	if srcDev == shmDev {
		t.Skip("/dev/shm is on the same device as the test temp dir; cross-device guard would be a no-op on this host")
	}

	// Write a small file into srcDir so the walk has something to do.
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	pm, err := patternmatcher.New(nil)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}

	// Stage into /dev/shm — must refuse with a clear error.
	_, cleanup, err := filteredWorktreeDir(srcDir, pm, "/dev/shm")
	if err == nil {
		cleanup()
		t.Fatal("expected cross-device staging error, got nil")
	}
	msg := err.Error()
	t.Logf("cross-device error: %s", msg)

	if !strings.Contains(msg, "different device") {
		t.Errorf("error should mention 'different device'; got: %q", msg)
	}
	if !strings.Contains(msg, "TMPDIR") {
		t.Errorf("error should mention 'TMPDIR'; got: %q", msg)
	}
	if !strings.Contains(msg, srcDir) {
		t.Errorf("error should include source path %q; got: %q", srcDir, msg)
	}
}
