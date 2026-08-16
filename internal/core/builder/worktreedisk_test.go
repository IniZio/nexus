package builder

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	if err := WorktreeToDisk(ctx, srcDir, outExt4, 0 /* auto */); err != nil {
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
	if err := WorktreeToDisk(ctx, srcDir, outExt4, 0 /* auto */); err != nil {
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
	if err := WorktreeToDisk(ctx, srcDir, outExt4, 0 /* auto */); err != nil {
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
	if err := WorktreeToDisk(ctx, srcDir, outExt4, 0 /* auto */); err != nil {
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

// TestWorktreeToDisk_AutoCapture_SmallTreeSucceeds verifies that an auto
// capture (maxBytes=0) of a tiny tree does NOT trip the free-space guard.
// This is a falsifiability test: with the old contract (maxBytes=0 ⟹ limit=0),
// any non-empty tree would fail because total > 0.
func TestWorktreeToDisk_AutoCapture_SmallTreeSucceeds(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "tiny.txt"), []byte("tiny"), 0o644); err != nil {
		t.Fatal(err)
	}

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	// maxBytes=0 = auto; a 4-byte tree must never trigger the guard.
	err := WorktreeToDisk(ctx, srcDir, outExt4, 0)
	if err == nil {
		return // success: guard did not trip and mke2fs produced the image
	}

	// mke2fs absent is an explicit skip, not a pass-through.
	if errors.Is(err, ErrMke2fsUnavailable) {
		t.Skip("mke2fs not available")
	}

	// A guard trip on a 4-byte tree is always a bug.
	if strings.Contains(err.Error(), "projected") || strings.Contains(err.Error(), "exceeds") ||
		strings.Contains(err.Error(), "limit") {
		t.Fatalf("auto guard tripped on tiny tree (falsifiable: old maxBytes=0 behaviour would always fail): %v", err)
	}

	// Any other error is a real failure — do not silently accept it.
	t.Fatalf("unexpected WorktreeToDisk error: %v", err)
}

// TestWorktreeToDisk_AutoCapture_StatfsError_FailsClosed verifies that when
// statfsAvail returns an error the guard fails closed — returning an error —
// rather than silently proceeding unguarded (fail-open). A guard that cannot
// measure cannot guarantee safety: mke2fs will write a large image until ENOSPC,
// which is exactly the hazard this guard exists to prevent.
//
// Break 6 in the advisor matrix (statfs always errors → guard skipped) is
// detected by this test: if the production code reverts to "return nil" on
// statfs error, this test goes RED.
func TestWorktreeToDisk_AutoCapture_StatfsError_FailsClosed(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "data.bin"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stub: statfs always errors.
	old := statfsAvail
	statfsAvail = func(path string) (int64, error) {
		return 0, fmt.Errorf("injected statfs error")
	}
	defer func() { statfsAvail = old }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err := WorktreeToDisk(ctx, srcDir, outExt4, 0) // 0 = auto
	if err == nil {
		t.Fatal("expected error when statfs fails (fail-closed policy), got nil")
	}
	// The guard must surface the statfs error — not silently skip.
	if !strings.Contains(err.Error(), "statfs") && !strings.Contains(err.Error(), "injected") {
		t.Errorf("error should reference the statfs failure; got: %q", err.Error())
	}
	// Confirm no ext4 file was written.
	if _, statErr := os.Stat(outExt4); statErr == nil {
		t.Error("ext4 image should not exist when preflight guard errors")
	}
}

// TestWorktreeToDisk_AutoCapture_FractionCalibration pins the
// captureFreeSpaceFraction constant (0.8). It stubs avail=70_000_000 bytes
// so that the projectedBytes value (≈67 MiB) falls strictly between
// 0.8×avail (56 MB) and 1.0×avail (70 MB). The guard MUST trip at 0.8 and
// would pass at 1.0, so break 1 (fraction 0.8 → 1.0) is detected.
func TestWorktreeToDisk_AutoCapture_FractionCalibration(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()

	// 1000-byte file: total=1000, projectedBytes = 1000*2 + 67_108_864 = 67_110_864.
	// avail=70_000_000: safeAvail(0.8)=56_000_000 < 67_110_864 < 70_000_000=safeAvail(1.0).
	if err := os.WriteFile(filepath.Join(srcDir, "data.bin"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}

	old := statfsAvail
	statfsAvail = func(path string) (int64, error) { return 70_000_000, nil }
	defer func() { statfsAvail = old }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err := WorktreeToDisk(ctx, srcDir, outExt4, 0)
	if err == nil {
		t.Fatal("guard must trip: projected ~67 MiB > 80% of 70 MB avail (56 MB); would pass only at fraction=1.0")
	}
	if !strings.Contains(err.Error(), "projected") {
		t.Errorf("error should mention 'projected'; got: %q", err.Error())
	}
}

// TestWorktreeToDisk_AutoCapture_ProjectionCalibration verifies that the guard
// applies the full ×2 + 64 MiB headroom formula, not a bare total.
// With avail=80_000_000 and a 1000-byte tree:
//   - projectedBytes (×2+64MiB) = 67_110_864 > 64_000_000 (80% of avail) → trips.
//   - projectedBytes (bare total) = 1000 < 64_000_000 → would pass.
//
// Break 2 (headroom removed) is detected: the test goes RED when the formula
// is broken to projectedBytes = total.
func TestWorktreeToDisk_AutoCapture_ProjectionCalibration(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()

	// 1000-byte file: total=1000.
	// projectedBytes (full formula) = 1000*2 + 67_108_864 = 67_110_864.
	// projectedBytes (no headroom)  = 1000.
	// avail=80_000_000 → safeAvail=64_000_000.
	// Full formula: 67_110_864 > 64_000_000 → trips.
	// No headroom:  1000 < 64_000_000 → passes.
	if err := os.WriteFile(filepath.Join(srcDir, "data.bin"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}

	old := statfsAvail
	statfsAvail = func(path string) (int64, error) { return 80_000_000, nil }
	defer func() { statfsAvail = old }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err := WorktreeToDisk(ctx, srcDir, outExt4, 0)
	if err == nil {
		t.Fatal("guard must trip: ×2+64MiB projected image exceeds 80% of 80 MB avail; would pass only if headroom is removed")
	}
	if !strings.Contains(err.Error(), "projected") {
		t.Errorf("error should mention 'projected'; got: %q", err.Error())
	}
}

// TestPreflightCaptureSize_UnreadableDir verifies that preflightCaptureSize fails
// closed — returning an error — when a directory in the source tree cannot be
// read due to permissions. An unreadable subtree means its file sizes are absent
// from the measured total, making it an undercount; the guard must not pass on an
// undercount because it cannot guarantee safety.
//
// The test must be skipped when running as root: root bypasses permission bits,
// so the directory would be readable and the error-skip path would never be
// exercised — the test would silently pass without covering anything.
//
// Falsifiability: if the production code reverts to returning nil on a walk error
// (the pre-fix behaviour), the error-skip is swallowed, total underestimates the
// tree, and with ample space the guard returns nil — this test goes RED.
func TestPreflightCaptureSize_UnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: root bypasses permission bits, cannot test unreadable-dir error-skip path")
	}

	srcDir := t.TempDir()

	// A readable file to give the walk some content.
	if err := os.WriteFile(filepath.Join(srcDir, "visible.txt"), []byte("visible"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A directory that cannot be read: WalkDir will deliver a permission error
	// when it tries to descend into it, exercising the werr != nil branch.
	unreadableDir := filepath.Join(srcDir, "secret")
	if err := os.MkdirAll(unreadableDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unreadableDir, "hidden.txt"), []byte("hidden content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadableDir, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restore permissions so TempDir cleanup can remove the directory.
	defer func() { _ = os.Chmod(unreadableDir, 0o755) }()

	pm, err := patternmatcher.New(nil)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}

	// Stub: ample space so the guard would pass if total were correct (7 bytes).
	old := statfsAvail
	statfsAvail = func(path string) (int64, error) { return 1 << 40, nil } // 1 TiB
	defer func() { statfsAvail = old }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err = preflightCaptureSize(srcDir, outExt4, pm, 0 /* auto */)
	if err == nil {
		t.Fatal("expected error due to unreadable directory (fail-closed policy), got nil")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)
	if !strings.Contains(msg, "could not be read") {
		t.Errorf("error should mention paths that could not be read; got: %q", msg)
	}
}

// TestPreflightCaptureSize_VanishedEntry_NotFailClosed verifies that an entry
// that produces ENOENT during the walk does NOT trip the fail-closed guard.
// ENOENT means the entry has vanished: it contributes zero bytes, so there is no
// undercount, and the guard must not reject the capture.
//
// Deterministic injection: rather than deleting a file mid-walk (inherently
// racy), we override the walkDirFn package variable to intercept walk callbacks
// and inject a synthetic ENOENT in the werr position for a specific path. This
// matches the real ENOENT that filepath.WalkDir would deliver if the OS returned
// ENOENT when statting a freshly-listed entry, without any OS-level timing
// dependency.
//
// Note: a dangling symlink was evaluated as a deterministic alternative and
// rejected: WalkDir calls lstat on the symlink itself (not the target), so both
// werr and d.Info() return nil for a dangling symlink — it does not exercise the
// ENOENT path in this code.
//
// Falsifiability: revert the isVanishedEntry carve-out in preflightCaptureSize
// (so ENOENT is treated as an unmeasurable skip) and this test goes RED because
// the injected ENOENT trips the fail-closed guard.
func TestPreflightCaptureSize_VanishedEntry_NotFailClosed(t *testing.T) {
	srcDir := t.TempDir()

	// A normally readable file that the guard will measure.
	if err := os.WriteFile(filepath.Join(srcDir, "visible.txt"), []byte("visible"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file that will "vanish" via the injected ENOENT. It exists on disk so
	// the directory listing includes it; the injected error simulates it being
	// deleted between the listing and the stat.
	vanishName := "vanished.txt"
	if err := os.WriteFile(filepath.Join(srcDir, vanishName), []byte("this will appear to vanish"), 0o644); err != nil {
		t.Fatal(err)
	}

	pm, err := patternmatcher.New(nil)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}

	// Inject a walker that wraps filepath.WalkDir and synthesises ENOENT in the
	// werr position for vanishName, simulating the real-world race where the
	// entry is in the directory listing but is deleted before WalkDir stats it.
	old := walkDirFn
	walkDirFn = func(root string, fn fs.WalkDirFunc) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if filepath.Base(path) == vanishName && werr == nil {
				// Synthesise ENOENT: the entry vanished between ReadDir and stat.
				return fn(path, d, &os.PathError{Op: "lstat", Path: path, Err: syscall.ENOENT})
			}
			return fn(path, d, werr)
		})
	}
	defer func() { walkDirFn = old }()

	// Ample free space so the guard would pass on a correctly-measured tree.
	oldStatfs := statfsAvail
	statfsAvail = func(path string) (int64, error) { return 1 << 40, nil } // 1 TiB
	defer func() { statfsAvail = oldStatfs }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err = preflightCaptureSize(srcDir, outExt4, pm, 0 /* auto */)
	if err != nil {
		t.Fatalf("ENOENT (vanished entry) must NOT trip the fail-closed guard; got: %v", err)
	}
}

// TestPreflightCaptureSize_NoErrorSkips_NoChange verifies that a tree with no
// unreadable entries produces no new error and behaves identically to before the
// fix: the guard passes for a small tree with ample space. This confirms the
// common case is not made noisy by the error-skip tracking.
func TestPreflightCaptureSize_NoErrorSkips_NoChange(t *testing.T) {
	srcDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "data.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	pm, err := patternmatcher.New(nil)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}

	// Ample space: projected image (~64 MiB) is well below 80 % of 1 TiB.
	old := statfsAvail
	statfsAvail = func(path string) (int64, error) { return 1 << 40, nil }
	defer func() { statfsAvail = old }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err = preflightCaptureSize(srcDir, outExt4, pm, 0 /* auto */)
	if err != nil {
		t.Fatalf("expected nil for clean tree with ample space (zero error-skips must not change behaviour), got: %v", err)
	}
}

// TestWorktreeToDisk_AutoCapture_FreeSpaceGuardTrips verifies that the auto
// guard (maxBytes=0) trips when the projected ext4 image exceeds the safe
// fraction of available space. It injects a tiny available-bytes value via the
// statfsAvail package variable so no real gigabytes need to be written.
//
// Falsifiability: if the production change is reverted (maxBytes=0 ⟹ explicit
// limit=0 check), WorktreeToDisk returns an error mentioning "limit 0 B" but
// NOT "projected" or "available" or "--capture-max", so the checks below go RED.
func TestWorktreeToDisk_AutoCapture_FreeSpaceGuardTrips(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()

	// 100 bytes of data → projected image = 100*2 + 64MiB ≫ 80% of 100 bytes.
	if err := os.WriteFile(filepath.Join(srcDir, "data.bin"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stub: report only 100 bytes available on the output filesystem.
	old := statfsAvail
	statfsAvail = func(path string) (int64, error) { return 100, nil }
	defer func() { statfsAvail = old }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err := WorktreeToDisk(ctx, srcDir, outExt4, 0) // 0 = auto
	if err == nil {
		t.Fatal("expected auto guard to trip when statfsAvail returns 100 bytes, got nil")
	}

	msg := err.Error()
	t.Logf("auto guard error: %s", msg)

	if !strings.Contains(msg, "projected") {
		t.Errorf("auto guard error must mention 'projected'; got: %q", msg)
	}
	if !strings.Contains(msg, "available") {
		t.Errorf("auto guard error must mention 'available'; got: %q", msg)
	}
	if !strings.Contains(msg, "--capture-max") {
		t.Errorf("auto guard error must mention '--capture-max'; got: %q", msg)
	}
	// Confirm no ext4 file was written (fail-fast guarantee).
	if _, statErr := os.Stat(outExt4); statErr == nil {
		t.Error("ext4 image should not exist after auto guard rejection")
	}
}

// TestPreflightCaptureSize_HeadroomFactor2_Calibration pins the ×2 multiplier
// in the auto-mode projection formula (total*imageSizeHeadroomFactor+imageMinSizeBytes).
//
// Approach (b): calibrate statfsAvail so safeAvail sits STRICTLY BETWEEN
// total*1+imageMinSizeBytes and total*2+imageMinSizeBytes.
//
// A 100 MiB sparse file carries apparent size = 100 MiB in d.Info().Size()
// (lstat st_size) without writing real bytes:
//
//	total    = 100 MiB = 104_857_600 bytes
//	×2 proj  = 104_857_600*2 + 67_108_864 = 276_824_064 bytes  (~264 MiB)
//	×1 proj  = 104_857_600*1 + 67_108_864 = 171_966_464 bytes  (~164 MiB)
//	avail    = 250_000_000  →  safeAvail(0.8) = 200_000_000 bytes  (200 MB)
//
//	×2 formula: 276_824_064 > 200_000_000 → guard trips     ✓ (expected)
//	×1 formula: 171_966_464 < 200_000_000 → guard passes    ✗ (mutation survives → test RED)
//
// Break M7b (drop ×2, keep +min) and M15 (imageSizeHeadroomFactor 2 → 1)
// both reduce the projection to total*1+min, which passes the guard — making
// this test fail. The ×2 term is thus genuinely defended.
func TestPreflightCaptureSize_HeadroomFactor2_Calibration(t *testing.T) {
	srcDir := t.TempDir()

	// Sparse file: apparent size 100 MiB, no real disk bytes consumed.
	// os.File.Truncate sets st_size (logical file size) without writing data;
	// filepath.WalkDir uses lstat, so d.Info().Size() returns the logical size.
	const apparentSize = 100 * 1024 * 1024 // 100 MiB = 104_857_600 bytes
	f, err := os.Create(filepath.Join(srcDir, "sparse.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(apparentSize); err != nil {
		f.Close()
		t.Fatalf("Truncate: %v", err)
	}
	f.Close()

	// Confirm the walk sees the full logical size, not actual disk blocks.
	fi, err := os.Lstat(filepath.Join(srcDir, "sparse.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != apparentSize {
		t.Skipf("lstat Size()=%d, want %d — sparse files may not be supported here; skipping", fi.Size(), int64(apparentSize))
	}

	pm, err := patternmatcher.New(nil)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}

	// avail = 250 MB → safeAvail = 200 MB.
	// Projected with ×2: 276_824_064 > 200_000_000 → guard must trip.
	// Projected with ×1: 171_966_464 < 200_000_000 → guard would pass (mutation survives).
	old := statfsAvail
	statfsAvail = func(path string) (int64, error) { return 250_000_000, nil }
	defer func() { statfsAvail = old }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err = preflightCaptureSize(srcDir, outExt4, pm, 0 /* auto */)
	if err == nil {
		t.Fatal("guard must trip: ×2 projected image (276 MB) exceeds 80% of 250 MB (200 MB); " +
			"would pass only if imageSizeHeadroomFactor is reduced to 1")
	}
	if !strings.Contains(err.Error(), "projected") {
		t.Errorf("error should mention 'projected'; got: %q", err.Error())
	}
}

// TestPreflightCaptureSize_SymlinkCounted verifies that symlinks contribute
// their apparent size (lstat st_size = target-path length) to the measured
// total, and that excluding them from measurement changes the guard verdict
// (M16 falsifiability).
//
// Setup:
//
//	regular file  = 100 bytes
//	symlink       = 50-byte target path  →  d.Info().Size() == 50
//	maxBytes      = 120
//
//	With symlink measured:    total = 150 > 120 → guard trips     ✓ (expected)
//	Without symlink measured: total = 100 ≤ 120 → guard passes    ✗ (M16 mutation → test RED)
//
// The symlink points to a non-existent path; lstat succeeds on the symlink
// itself and returns the target-string length as st_size.
func TestPreflightCaptureSize_SymlinkCounted(t *testing.T) {
	srcDir := t.TempDir()

	// Regular file contributing 100 bytes to the total.
	if err := os.WriteFile(filepath.Join(srcDir, "data.bin"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlink with a 50-byte target string. The target need not exist:
	// filepath.WalkDir calls lstat on the symlink, not the target.
	const symlinkTarget = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" // exactly 50 bytes
	if len(symlinkTarget) != 50 {
		t.Fatalf("symlinkTarget length = %d, want 50 (test bug)", len(symlinkTarget))
	}
	if err := os.Symlink(symlinkTarget, filepath.Join(srcDir, "link")); err != nil {
		t.Fatal(err)
	}

	// Confirm the symlink's apparent size matches the target-string length.
	fi, err := os.Lstat(filepath.Join(srcDir, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 50 {
		t.Skipf("symlink lstat Size()=%d, want 50 — platform may not report target-string length; skipping", fi.Size())
	}

	pm, err := patternmatcher.New(nil)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}

	// maxBytes=120: total(100+50=150) > 120 → guard must trip.
	// If symlinks are excluded (M16): total(100) ≤ 120 → guard passes → test RED.
	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err = preflightCaptureSize(srcDir, outExt4, pm, 120)
	if err == nil {
		t.Fatal("guard must trip: regular(100) + symlink(50) = 150 exceeds maxBytes(120); " +
			"would pass only if symlinks are excluded from measurement")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "limit") {
		t.Errorf("unexpected error message (want 'too large' or 'limit'); got: %q", err.Error())
	}
}

// errDirEntry wraps a real fs.DirEntry but makes Info() return a specific error.
// Used by TestPreflightCaptureSize_InfoError_* to exercise the d.Info() error branch
// without relying on OS-level permission changes.
type errDirEntry struct {
	fs.DirEntry
	infoErr error
}

func (e errDirEntry) Info() (fs.FileInfo, error) { return nil, e.infoErr }

// TestPreflightCaptureSize_StatfsPath_MeasuresOutputDir defends M8: statfsAvail
// must be called with filepath.Dir(outExt4), not with srcDir or any other path.
//
// srcDir (the user's worktree) and Dir(outExt4) (the store / $TMPDIR) are
// routinely on different mounts. If statfs measures the wrong filesystem the
// guard silently reports wrong headroom — the disk-margin analysis in
// docs/site/guides/docker-in-sandbox.md §disk-space guard relies on it measuring the output
// filesystem.
//
// Falsifiability: mutate the statfsAvail call target from filepath.Dir(outExt4)
// to srcDir — the gotPath assertion below goes RED immediately.
func TestPreflightCaptureSize_StatfsPath_MeasuresOutputDir(t *testing.T) {
	srcDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "data.bin"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	pm, err := patternmatcher.New(nil)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}

	// Use a separate TempDir for the output so Dir(outExt4) is distinct from
	// srcDir (even though both happen to be on the same mount in CI, the test
	// asserts the exact path passed, not that the answer differs).
	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	wantStatfsPath := filepath.Clean(filepath.Dir(outExt4))

	var gotPath string
	old := statfsAvail
	statfsAvail = func(path string) (int64, error) {
		gotPath = filepath.Clean(path)
		return 1 << 40, nil // ample space — guard passes
	}
	defer func() { statfsAvail = old }()

	// auto mode (maxBytes=0) — this is the only branch that calls statfsAvail.
	if err := preflightCaptureSize(srcDir, outExt4, pm, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotPath != wantStatfsPath {
		t.Errorf("statfsAvail called with path %q; want %q (filepath.Dir(outExt4))\n"+
			"srcDir=%q — if these differ, the guard measured the wrong filesystem",
			gotPath, wantStatfsPath, filepath.Clean(srcDir))
	}
}

// TestPreflightCaptureSize_InfoError_EACCES_FailClosed defends M9: the d.Info()
// error branch must fail closed on a non-vanished error (EACCES). The entry
// exists on disk but its size is unmeasurable — the total is an undercount and
// the guard cannot guarantee safety.
//
// Injection: wrap the walkDirFn to replace the real DirEntry for a specific file
// with an errDirEntry whose Info() returns EACCES. This simulates a file whose
// metadata is restricted without requiring actual OS-level permission changes.
//
// Falsifiability: make the d.Info() error branch silently return nil (skip without
// recording) — errSkipCount stays zero, the guard passes, this test goes RED.
func TestPreflightCaptureSize_InfoError_EACCES_FailClosed(t *testing.T) {
	srcDir := t.TempDir()

	// A visible file that will have its d.Info() call intercepted.
	targetName := "restricted.bin"
	if err := os.WriteFile(filepath.Join(srcDir, targetName), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	pm, err := patternmatcher.New(nil)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}

	// Wrap walkDirFn: for targetName, replace d with an errDirEntry that returns
	// EACCES from Info(). werr remains nil so the werr branch is not exercised.
	old := walkDirFn
	walkDirFn = func(root string, fn fs.WalkDirFunc) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if filepath.Base(path) == targetName && werr == nil {
				eacces := &os.PathError{Op: "lstat", Path: path, Err: syscall.EACCES}
				return fn(path, errDirEntry{d, eacces}, werr)
			}
			return fn(path, d, werr)
		})
	}
	defer func() { walkDirFn = old }()

	oldStatfs := statfsAvail
	statfsAvail = func(path string) (int64, error) { return 1 << 40, nil } // ample space
	defer func() { statfsAvail = oldStatfs }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err = preflightCaptureSize(srcDir, outExt4, pm, 0 /* auto */)
	if err == nil {
		t.Fatal("EACCES in d.Info() must trip the fail-closed guard (unmeasurable entry exists on disk), got nil")
	}
	msg := err.Error()
	t.Logf("fail-closed error: %s", msg)
	if !strings.Contains(msg, "could not be read") {
		t.Errorf("error should mention 'could not be read'; got: %q", msg)
	}
}

// TestPreflightCaptureSize_InfoError_ENOENT_NotFailClosed verifies that ENOENT
// from d.Info() does NOT trip the fail-closed guard — the same carve-out that
// applies in the werr arm (tested by TestPreflightCaptureSize_VanishedEntry_NotFailClosed)
// must apply symmetrically in the d.Info() arm.
//
// ENOENT from d.Info() means the entry vanished between ReadDir and the Info call;
// it contributes zero bytes, so there is no undercount and the guard must not reject.
//
// Falsifiability: remove the isVanishedEntry carve-out from the d.Info() error
// branch — the ENOENT is treated as an unmeasurable skip, errSkipCount becomes 1,
// the guard fails closed, and this test goes RED.
func TestPreflightCaptureSize_InfoError_ENOENT_NotFailClosed(t *testing.T) {
	srcDir := t.TempDir()

	// A file that will "vanish" via an injected ENOENT in the d.Info() position.
	vanishName := "vanished-info.txt"
	if err := os.WriteFile(filepath.Join(srcDir, vanishName), []byte("will appear to vanish"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A normally readable file so the walk has something to measure.
	if err := os.WriteFile(filepath.Join(srcDir, "visible.txt"), []byte("visible"), 0o644); err != nil {
		t.Fatal(err)
	}

	pm, err := patternmatcher.New(nil)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}

	// Wrap walkDirFn: for vanishName, replace d with an errDirEntry whose Info()
	// returns ENOENT — simulating the entry disappearing between ReadDir and Info.
	old := walkDirFn
	walkDirFn = func(root string, fn fs.WalkDirFunc) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if filepath.Base(path) == vanishName && werr == nil {
				enoent := &os.PathError{Op: "lstat", Path: path, Err: syscall.ENOENT}
				return fn(path, errDirEntry{d, enoent}, werr)
			}
			return fn(path, d, werr)
		})
	}
	defer func() { walkDirFn = old }()

	oldStatfs := statfsAvail
	statfsAvail = func(path string) (int64, error) { return 1 << 40, nil } // ample space
	defer func() { statfsAvail = oldStatfs }()

	outExt4 := filepath.Join(t.TempDir(), "wt.ext4")
	err = preflightCaptureSize(srcDir, outExt4, pm, 0 /* auto */)
	if err != nil {
		t.Fatalf("ENOENT in d.Info() must NOT trip the fail-closed guard (vanished entry, no undercount); got: %v", err)
	}
}
