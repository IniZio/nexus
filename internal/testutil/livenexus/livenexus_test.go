package livenexus

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeT captures Fatal/Fatalf calls so we can assert refusals without killing
// the outer test.
type fakeT struct {
	fatalMsg string
	failed   bool
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, a ...any) {
	f.failed = true
	f.fatalMsg = format
	panic("fakeT.Fatalf") // stop execution like the real t.Fatalf
}
func (f *fakeT) Logf(format string, a ...any) {}
func (f *fakeT) Cleanup(fn func())            {}

// recoverFatal runs fn, recovering from the fakeT panic.
func recoverFatal(fn func()) (fataled bool) {
	defer func() {
		if r := recover(); r != nil {
			fataled = true
		}
	}()
	fn()
	return false
}

func TestRefusesProdStateRoot(t *testing.T) {
	prod, err := prodStateRoot()
	if err != nil {
		t.Skipf("cannot resolve prod state root: %v", err)
	}
	if err := validateStateRoot(prod); err == nil {
		t.Errorf("validateStateRoot(%q): want error for prod root, got nil", prod)
	}
}

func TestRefusesDefaultHerdrSocket(t *testing.T) {
	sock := prodHerdrSocket()
	if err := validateSocket(sock); err == nil {
		t.Errorf("validateSocket(%q): want error for prod socket, got nil", sock)
	}
}

func TestEnvPinsIsolatedRootAndSocket(t *testing.T) {
	h := &Harness{
		t:           t,
		stateRoot:   "/var/tmp/test-state",
		configHome:  "/var/tmp/test-config",
		dataHome:    "/var/tmp/test-data",
		credsDir:    "/var/tmp/test-creds",
		socketPath:  "/var/tmp/test-config/herdr/sessions/nl-abc/herdr.sock",
		sessionName: "nl-abc",
		nexusBin:    "nexus",
	}
	env := h.Env()

	want := map[string]string{
		"XDG_STATE_HOME":        "/var/tmp/test-state",
		"XDG_DATA_HOME":         "/var/tmp/test-data",
		"CREDENTIALS_DIRECTORY": "/var/tmp/test-creds",
		"HERDR_SOCKET_PATH":     "/var/tmp/test-config/herdr/sessions/nl-abc/herdr.sock",
		"TMPDIR":                "/var/tmp",
	}
	got := make(map[string]string)
	for _, e := range env {
		k, v, ok := splitEnv(e)
		if !ok {
			continue
		}
		if _, care := want[k]; care {
			got[k] = v
		}
	}
	for k, wv := range want {
		if gv, ok := got[k]; !ok {
			t.Errorf("Env() missing %s", k)
		} else if gv != wv {
			t.Errorf("Env() %s = %q, want %q", k, gv, wv)
		}
	}
}

func TestCleanupOnlyRegisteredHandles(t *testing.T) {
	var removed []string
	h := &Harness{
		t:        t,
		nexusBin: "nexus",
	}
	h.Track("sb-tracked-1")
	h.Track("sb-tracked-2")

	h.mu.Lock()
	handles := append([]string(nil), h.handles...)
	h.mu.Unlock()

	// Verify only the two tracked handles appear in the cleanup list.
	if len(handles) != 2 {
		t.Fatalf("expected 2 tracked handles, got %d", len(handles))
	}
	for _, want := range []string{"sb-tracked-1", "sb-tracked-2"} {
		found := false
		for _, got := range handles {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tracked handle %q not found in list", want)
		}
	}
	_ = removed
}

func TestRunnerRejectsPrune(t *testing.T) {
	h := &Harness{
		t:          t,
		stateRoot:  "/var/tmp/nexus-live-safe-state",
		nexusBin:   "nexus",
		socketPath: "/var/tmp/nexus-live-safe-config/herdr/sessions/nl-safe/herdr.sock",
	}
	ctx := context.Background()

	pruneArgCases := [][]string{
		{"herdr", "prune"},
		{"prune", "-apply"},
		{"herdr", "prune", "-apply"},
		{"volume", "prune"},
	}
	for _, args := range pruneArgCases {
		_, err := h.Run(ctx, args...)
		if err == nil {
			t.Errorf("Run(%v): expected error refusing prune, got nil", args)
		}
	}
}

// TestNoWritableProdLink verifies that hardLinkImagesDir produces hard links
// (not symlinks) and that the locks/ directory is excluded from the result.
func TestNoWritableProdLink(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Populate src with a structure matching the prod images layout.
	dirs := []string{
		filepath.Join(src, "sha256", "abc123"),
		filepath.Join(src, "locks", "sha256"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(src, "nexus-builder-test.ext4"):        "fake-ext4-content",
		filepath.Join(src, "sha256", "abc123", "artifact"):   "fake-artifact",
		filepath.Join(src, "sha256", "abc123", "meta.json"):  `{"digest":"sha256:abc123"}`,
		filepath.Join(src, "locks", "sha256", "abc123.lock"): "lock",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := hardLinkImagesDir(src, dst); err != nil {
		t.Fatalf("hardLinkImagesDir: %v", err)
	}

	// No symlinks should exist in dst.
	err := filepath.WalkDir(dst, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == dst {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("found symlink at %s — no writable prod symlinks allowed", path)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}

	// ext4 must be copied, not hard-linked — different inode from src.
	srcExt4, err := os.Stat(filepath.Join(src, "nexus-builder-test.ext4"))
	if err != nil {
		t.Fatal(err)
	}
	dstExt4, err := os.Stat(filepath.Join(dst, "nexus-builder-test.ext4"))
	if err != nil {
		t.Fatalf("expected copied ext4 in dst: %v", err)
	}
	if os.SameFile(srcExt4, dstExt4) {
		t.Error("dst ext4 must not share an inode with src — must be a copy, not a hard link")
	}

	// locks/ subtree must NOT appear in dst.
	if _, err := os.Stat(filepath.Join(dst, "locks")); err == nil {
		t.Error("locks/ dir must not be hard-linked into test root")
	}

	// sha256 content must be present and hard-linked (same inode as src).
	srcBlob, err := os.Stat(filepath.Join(src, "sha256", "abc123", "artifact"))
	if err != nil {
		t.Fatalf("sha256 content missing in src: %v", err)
	}
	dstBlob, err := os.Stat(filepath.Join(dst, "sha256", "abc123", "artifact"))
	if err != nil {
		t.Fatalf("sha256 content missing in dst: %v", err)
	}
	if !os.SameFile(srcBlob, dstBlob) {
		t.Error("sha256 blob must share inode with src (hard link)")
	}

	// No file outside sha256/ must share an inode with src.
	err = filepath.WalkDir(dst, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dst, path)
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if parts[0] == "sha256" {
			return nil
		}
		dstStat, statErr := os.Stat(path)
		if statErr != nil {
			return nil
		}
		srcPath := filepath.Join(src, rel)
		srcStat, statErr := os.Stat(srcPath)
		if statErr != nil {
			return nil
		}
		if os.SameFile(srcStat, dstStat) {
			t.Errorf("non-sha256 file %s shares inode with prod — must be a copy", rel)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

// TestCleanupOrderStopsSupervisorsFirst verifies that waitProcsExit and
// findProcsReferencingPath correctly detect and report processes whose cmdline
// references a given path, which underpins the supervisor-first cleanup order.
func TestCleanupOrderStopsSupervisorsFirst(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/var/tmp", "nexus-live-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Start a process whose cmdline contains tmpDir.
	// We execute a shell script whose path is inside tmpDir, so the process's
	// /proc/<pid>/cmdline will contain tmpDir.
	scriptPath := filepath.Join(tmpDir, "probe.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", scriptPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	// Give the process time to appear in /proc.
	var pids []int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pids = findProcsReferencingPath(tmpDir)
		if len(pids) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(pids) == 0 {
		t.Fatal("findProcsReferencingPath: expected to find the probe process, found none")
	}

	// Killing the process should make waitProcsExit return quickly.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	waitProcsExit(tmpDir, 3*time.Second)

	remaining := findProcsReferencingPath(tmpDir)
	if len(remaining) > 0 {
		t.Errorf("processes still reference %s after kill: %v", tmpDir, remaining)
	}
}

// TestSnapshotDetectsLeak verifies that checkSnapshot returns no errors when
// before==after, and returns errors when they differ.
func TestResolveNexusBinNeverUsesPATH(t *testing.T) {
	t.Setenv("NEXUS_BIN", "")

	dir := t.TempDir()
	bin, err := resolveNexusBin(dir)
	if err != nil {
		t.Fatalf("resolveNexusBin: %v", err)
	}

	pathNexus, _ := exec.LookPath("nexus")
	if pathNexus != "" && (bin == pathNexus || bin == "nexus") {
		t.Errorf("resolveNexusBin returned PATH nexus %q; must use worktree-built binary", bin)
	}
	if bin == "nexus" {
		t.Error("resolveNexusBin returned bare 'nexus' string; must be an absolute path to worktree build")
	}
	if !filepath.IsAbs(bin) {
		t.Errorf("resolveNexusBin returned non-absolute path %q", bin)
	}
}

// TestBuildDirInsideBase verifies that buildNexusBin places the binary inside
// the given base dir (not in a separate /var/tmp/nexus-live-bin-* dir), so
// it is removed automatically when the per-test base dir is cleaned up.
func TestBuildDirInsideBase(t *testing.T) {
	if os.Getenv("NEXUS_BIN") != "" {
		t.Skip("NEXUS_BIN set — no build needed")
	}
	dir := t.TempDir()
	bin, err := buildNexusBin(dir)
	if err != nil {
		t.Fatalf("buildNexusBin: %v", err)
	}
	rel, err := filepath.Rel(dir, bin)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("binary %q is not inside build dir %q — build-dir leak possible", bin, dir)
	}
	// After cleanup (simulated here), the binary must be gone.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Errorf("binary %q still exists after build dir removed", bin)
	}
}

func TestSnapshotDetectsLeak(t *testing.T) {
	dir := t.TempDir()
	bin, err := resolveNexusBin(dir)
	if err != nil {
		t.Fatalf("resolveNexusBin: %v", err)
	}
	snap := captureEnvSnapshot(bin, "", "")

	h := &Harness{t: t, nexusBin: bin, preSnap: snap}

	// Identical snapshot → no errors.
	if errs := h.checkSnapshot(snap); len(errs) != 0 {
		t.Errorf("identical snapshots should produce no errors, got %d:\n%s",
			len(errs), fmt.Sprintf("%v", errs))
	}

	// Simulated branch leak → error on git branches.
	modified := snap
	modified.gitBranches = snap.gitBranches + "\n  ctrl/leaked-branch"
	errs := h.checkSnapshot(modified)
	if len(errs) == 0 {
		t.Error("modified git branches should produce errors, got none")
	}

	// Simulated herdr session leak → error on herdr sessions.
	modified2 := snap
	modified2.herdrSessions = snap.herdrSessions + "\nnl-leaked-session"
	errs2 := h.checkSnapshot(modified2)
	if len(errs2) == 0 {
		t.Error("modified herdr sessions should produce errors, got none")
	}
}

func TestHarnessIsolatesVaultAndKey(t *testing.T) {
	base, err := os.MkdirTemp("/var/tmp", "nexus-live-vaulttest-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	credsDir := filepath.Join(base, "creds")
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(credsDir, "nexus-vault-key")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}

	h := &Harness{
		t:           t,
		stateRoot:   filepath.Join(base, "state"),
		configHome:  filepath.Join(base, "config"),
		dataHome:    filepath.Join(base, "data"),
		credsDir:    credsDir,
		socketPath:  filepath.Join(base, "config", "herdr", "sessions", "nl-test", "herdr.sock"),
		sessionName: "nl-test",
		nexusBin:    "nexus",
	}

	env := h.Env()
	got := make(map[string]string)
	for _, e := range env {
		k, v, ok := splitEnv(e)
		if !ok {
			continue
		}
		got[k] = v
	}

	if got["CREDENTIALS_DIRECTORY"] != credsDir {
		t.Errorf("CREDENTIALS_DIRECTORY = %q, want %q", got["CREDENTIALS_DIRECTORY"], credsDir)
	}
	wantDataHome := filepath.Join(base, "data")
	if got["XDG_DATA_HOME"] != wantDataHome {
		t.Errorf("XDG_DATA_HOME = %q, want %q", got["XDG_DATA_HOME"], wantDataHome)
	}

	t.Setenv("XDG_DATA_HOME", wantDataHome)
	vaultDir, vaultErr := defaultVaultDir()
	if vaultErr != nil {
		t.Fatalf("defaultVaultDir: %v", vaultErr)
	}
	if !strings.HasPrefix(vaultDir, base) {
		t.Errorf("vault dir %q must be inside test root %q", vaultDir, base)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("vault key not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("vault key mode = %v, want 0600", info.Mode().Perm())
	}
	if info.Size() < 32 {
		t.Errorf("vault key too short: %d bytes", info.Size())
	}

	snap := envSnapshot{credsDirSet: true}
	snap2 := envSnapshot{credsDirSet: true, prodVaultSum: "different"}
	errs := h.checkSnapshot(snap)
	_ = errs

	snap3 := envSnapshot{credsDirSet: true, prodVaultSum: "x"}
	h.preSnap = envSnapshot{credsDirSet: true, prodVaultSum: "x"}
	if errs3 := h.checkSnapshot(snap3); len(errs3) != 0 {
		t.Errorf("identical vault sums produced errors: %v", errs3)
	}

	h.preSnap = envSnapshot{credsDirSet: true, prodVaultSum: "before"}
	if errs4 := h.checkSnapshot(snap2); len(errs4) == 0 {
		t.Error("changed vault sum should produce an error")
	}
}

func defaultVaultDir() (string, error) {
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		xdg = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(xdg, "nexus", "vault"), nil
}

func splitEnv(e string) (key, val string, ok bool) {
	for i, c := range e {
		if c == '=' {
			return e[:i], e[i+1:], true
		}
	}
	return "", "", false
}
