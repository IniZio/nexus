package livenexus

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeT captures Fatal/Fatalf calls so we can assert refusals without killing
// the outer test.
type fakeT struct {
	fatalMsg string
	failed   bool
}

func (f *fakeT) Helper()                       {}
func (f *fakeT) Fatalf(format string, a ...any) {
	f.failed = true
	f.fatalMsg = format
	panic("fakeT.Fatalf") // stop execution like the real t.Fatalf
}
func (f *fakeT) Logf(format string, a ...any) {}
func (f *fakeT) Cleanup(fn func())             {}

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
		socketPath:  "/var/tmp/test-config/herdr/sessions/nl-abc/herdr.sock",
		sessionName: "nl-abc",
		nexusBin:    "nexus",
	}
	env := h.Env()

	want := map[string]string{
		"XDG_STATE_HOME":    "/var/tmp/test-state",
		"XDG_DATA_HOME":     "/var/tmp/test-data",
		"HERDR_SOCKET_PATH": "/var/tmp/test-config/herdr/sessions/nl-abc/herdr.sock",
		"TMPDIR":            "/var/tmp",
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
		t:    t,
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
		filepath.Join(src, "nexus-builder-test.ext4"):          "fake-ext4-content",
		filepath.Join(src, "sha256", "abc123", "artifact"):     "fake-artifact",
		filepath.Join(src, "sha256", "abc123", "meta.json"):    `{"digest":"sha256:abc123"}`,
		filepath.Join(src, "locks", "sha256", "abc123.lock"):   "lock",
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

	// Builder ext4 should be hard-linked (same inode as src).
	srcInfo, err := os.Stat(filepath.Join(src, "nexus-builder-test.ext4"))
	if err != nil {
		t.Fatal(err)
	}
	dstInfo, err := os.Stat(filepath.Join(dst, "nexus-builder-test.ext4"))
	if err != nil {
		t.Fatalf("expected hard-linked ext4 in dst: %v", err)
	}
	if !os.SameFile(srcInfo, dstInfo) {
		t.Error("dst ext4 should share an inode with src (hard link), not a copy")
	}

	// locks/ subtree must NOT appear in dst.
	if _, err := os.Stat(filepath.Join(dst, "locks")); err == nil {
		t.Error("locks/ dir must not be hard-linked into test root")
	}

	// sha256 content should be present.
	if _, err := os.Stat(filepath.Join(dst, "sha256", "abc123", "artifact")); err != nil {
		t.Errorf("sha256 content missing in dst: %v", err)
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
func TestSnapshotDetectsLeak(t *testing.T) {
	snap := captureEnvSnapshot(resolveNexusBin())

	h := &Harness{t: t, nexusBin: resolveNexusBin(), preSnap: snap}

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

func splitEnv(e string) (key, val string, ok bool) {
	for i, c := range e {
		if c == '=' {
			return e[:i], e[i+1:], true
		}
	}
	return "", "", false
}
