package daemonclient

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadRunningDaemonPID(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	runDir := filepath.Join(xdg, "nexus")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "daemon-7874.pid"), []byte("12345\n"), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	pid, err := readRunningDaemonPID(7874)
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	if pid != 12345 {
		t.Fatalf("expected pid 12345, got %d", pid)
	}
}

func TestReadRunningDaemonPIDFallsBackToLegacyPath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	runDir := filepath.Join(xdg, "nexus")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "daemon.pid"), []byte("22222\n"), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	pid, err := readRunningDaemonPID(7874)
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	if pid != 22222 {
		t.Fatalf("expected pid 22222, got %d", pid)
	}
}

func TestShouldRestartRunningDaemonWhenBinaryNewer(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	runDir := filepath.Join(xdg, "nexus")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "daemon-7874.pid"), []byte("999999"), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	daemonBin := filepath.Join(t.TempDir(), "nexus-daemon")
	if err := os.WriteFile(daemonBin, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write daemon binary: %v", err)
	}
	older := time.Now().Add(-1 * time.Hour)
	newer := time.Now().Add(1 * time.Hour)
	if err := os.Chtimes(daemonBin, newer, newer); err != nil {
		t.Fatalf("set daemon binary mtime: %v", err)
	}

	origCommandLine := daemonProcessCommandLineFn
	origStartedAt := daemonProcessStartedAtFn
	t.Cleanup(func() {
		daemonProcessCommandLineFn = origCommandLine
		daemonProcessStartedAtFn = origStartedAt
	})

	daemonProcessCommandLineFn = func(pid int) (string, error) {
		return daemonBin, nil
	}
	daemonProcessStartedAtFn = func(pid int) (time.Time, error) {
		return older, nil
	}

	restart, err := shouldRestartRunningDaemon(7874, daemonBin)
	if err != nil {
		t.Fatalf("shouldRestartRunningDaemon returned error: %v", err)
	}
	if !restart {
		t.Fatalf("expected restart=true when binary is newer")
	}
}

func TestShouldRestartRunningDaemonWhenCommandMismatches(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	runDir := filepath.Join(xdg, "nexus")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "daemon-7874.pid"), []byte("999998"), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	daemonBin := filepath.Join(t.TempDir(), "nexus-daemon")
	if err := os.WriteFile(daemonBin, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write daemon binary: %v", err)
	}

	origCommandLine := daemonProcessCommandLineFn
	origStartedAt := daemonProcessStartedAtFn
	t.Cleanup(func() {
		daemonProcessCommandLineFn = origCommandLine
		daemonProcessStartedAtFn = origStartedAt
	})

	daemonProcessCommandLineFn = func(pid int) (string, error) {
		return "/tmp/go-build-old/daemon", nil
	}
	daemonProcessStartedAtFn = func(pid int) (time.Time, error) {
		return time.Now(), nil
	}

	restart, err := shouldRestartRunningDaemon(7874, daemonBin)
	if err != nil {
		t.Fatalf("shouldRestartRunningDaemon returned error: %v", err)
	}
	if !restart {
		t.Fatalf("expected restart=true when command line does not include daemon binary")
	}
}

func TestShouldRestartRunningDaemonWhenStartTimeUnknown(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	runDir := filepath.Join(xdg, "nexus")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "daemon-7874.pid"), []byte("999997"), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	daemonBin := filepath.Join(t.TempDir(), "nexus-daemon")
	if err := os.WriteFile(daemonBin, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write daemon binary: %v", err)
	}

	origCommandLine := daemonProcessCommandLineFn
	origStartedAt := daemonProcessStartedAtFn
	t.Cleanup(func() {
		daemonProcessCommandLineFn = origCommandLine
		daemonProcessStartedAtFn = origStartedAt
	})

	daemonProcessCommandLineFn = func(pid int) (string, error) {
		return daemonBin, nil
	}
	daemonProcessStartedAtFn = func(pid int) (time.Time, error) {
		return time.Time{}, os.ErrInvalid
	}

	restart, err := shouldRestartRunningDaemon(7874, daemonBin)
	if err != nil {
		t.Fatalf("shouldRestartRunningDaemon returned error: %v", err)
	}
	if !restart {
		t.Fatalf("expected restart=true when process start time cannot be read")
	}
}

func TestLocalDriverWorktreeRoot_DetectsEnabledRepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nexus"), 0o755); err != nil {
		t.Fatalf("mkdir .nexus: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repo, ".nexus", "workspace.json"),
		[]byte(`{"version":1,"internalFeatures":{"localDriver":true}}`),
		0o644,
	); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	nested := filepath.Join(repo, "packages", "nexus")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	gotRoot, ok := LocalDriverWorktreeRoot(nested)
	if !ok {
		t.Fatalf("expected local-driver worktree root detection")
	}
	if gotRoot != canonicalPath(repo) {
		t.Fatalf("expected root %q, got %q", canonicalPath(repo), gotRoot)
	}
}

func TestSelectPortForWorktreeRoot_UsesStablePreferredPortWhenFree(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	repo := t.TempDir()
	port, err := SelectPortForWorktreeRoot(repo)
	if err != nil {
		t.Fatalf("select port: %v", err)
	}
	want := preferredLocalDriverPort(canonicalPath(repo))
	if port != want {
		t.Fatalf("expected preferred port %d, got %d", want, port)
	}
}

func TestSelectPortForWorktreeRoot_ProbesWhenPreferredOwnedByOtherWorktree(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	runDir := filepath.Join(xdg, "nexus")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	repoA := t.TempDir()
	repoB := t.TempDir()
	preferred := preferredLocalDriverPort(canonicalPath(repoA))
	if err := writeDaemonOwner(runDir, preferred, repoB); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	selected, err := SelectPortForWorktreeRoot(repoA)
	if err != nil {
		t.Fatalf("select port: %v", err)
	}
	if selected == preferred {
		t.Fatalf("expected probing away from preferred occupied owner port %d", preferred)
	}
}
