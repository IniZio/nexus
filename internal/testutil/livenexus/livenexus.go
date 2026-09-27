// Package livenexus provides an isolated live-test harness for nexus integration tests.
// Every live test touching sandboxes, herdr worktrees, or sessions MUST use this harness.
// Isolation: XDG_STATE_HOME in fresh /var/tmp, dedicated herdr systemd unit, prod image
// cache hard-linked read-only, worktrees under <base>/worktrees. Cleanup runs on panic
// or failure (t.Cleanup), fails on leaked procs or prod-invariant changes, never touches
// prod resources. Run/RunHerdr refuse "prune". NEXUS_KERNEL_PATH forwarded unchanged.
package livenexus

import (
	"context"
	"crypto/rand"
	sha256pkg "crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// prodStateRoot resolves the nexus state root without harness overrides (collision guard).
func prodStateRoot() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "nexus"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("livenexus: resolve home: %w", err)
	}
	return filepath.Join(home, ".local", "state", "nexus"), nil
}

// prodHerdrSocket returns the default herdr socket path in the current environment.
func prodHerdrSocket() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

// validateStateRoot returns an error when root matches the prod state path.
func validateStateRoot(root string) error {
	prod, err := prodStateRoot()
	if err != nil {
		return err
	}
	if filepath.Clean(root) == filepath.Clean(prod) {
		return fmt.Errorf("livenexus: isolated state root %q equals prod root %q; refusing", root, prod)
	}
	return nil
}

// validateSocket returns an error when socketPath matches the prod herdr socket.
func validateSocket(socketPath string) error {
	prod := prodHerdrSocket()
	if filepath.Clean(socketPath) == filepath.Clean(prod) {
		return fmt.Errorf("livenexus: isolated socket %q equals prod socket %q; refusing", socketPath, prod)
	}
	return nil
}

// envSnapshot is a point-in-time view of prod-side resources for leak detection.
type envSnapshot struct {
	prodPS         string // output of `nexus ps` against prod socket
	herdrSessions  string // sorted names under ~/.config/herdr/sessions
	herdrWorktrees string // sorted names under ~/.herdr/worktrees/nexus
	gitBranches    string // output of `git -C <nexus-repo> branch --list`
	systemdUnits   string // output of `systemctl --user list-units --all nl-* --no-legend`
	liveProcs      string // PIDs referencing the harness base dir (per-harness, not global)
	prodVaultSum   string // sha256 checksum listing of ~/.local/share/nexus/vault
	credsDirSet    bool   // true when CREDENTIALS_DIRECTORY was pinned before any nexus call
}

// prodVaultDir returns the prod vault directory path.
func prodVaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "nexus", "vault")
}

// checksumDir returns a sorted "<name> <sha256>" listing for every file under dir.
func checksumDir(dir string) string {
	var lines []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		h := sha256pkg.Sum256(data)
		rel, _ := filepath.Rel(dir, path)
		lines = append(lines, fmt.Sprintf("%s %s", rel, hex.EncodeToString(h[:])))
		return nil
	})
	return strings.Join(lines, "\n")
}

// captureEnvSnapshot records prod-side state; base/session scope fields to avoid
// false positives from parallel harnesses (""→field skipped).
func captureEnvSnapshot(prodBin, base, session string) envSnapshot {
	home, _ := os.UserHomeDir()
	var liveProcs string
	if base != "" {
		pids := findProcsReferencingPath(base)
		if len(pids) > 0 {
			parts := make([]string, len(pids))
			for i, p := range pids {
				parts[i] = strconv.Itoa(p)
			}
			liveProcs = strings.Join(parts, " ")
		}
	}
	var herdrSessions, systemdUnits string
	if session != "" {
		sessDir := filepath.Join(home, ".config", "herdr", "sessions", session)
		if _, err := os.Stat(sessDir); err == nil {
			herdrSessions = "exists"
		}
		out, _ := runCapture("systemctl", "--user", "list-units", "--all", session+".service", "--no-legend")
		if strings.TrimSpace(out) != "" {
			systemdUnits = out
		}
	}
	ps, _ := runCapture(prodBin, "ps")
	branches, _ := runCapture("git", "-C", mainRepoPath(), "branch", "--list")
	return envSnapshot{
		prodPS:         ps,
		herdrSessions:  herdrSessions,
		herdrWorktrees: listDirEntries(filepath.Join(home, ".herdr", "worktrees", "nexus")),
		gitBranches:    branches,
		systemdUnits:   systemdUnits,
		liveProcs:      liveProcs,
		prodVaultSum:   checksumDir(prodVaultDir()),
	}
}

// checkSnapshot returns one error string per field that changed since preSnap.
func (h *Harness) checkSnapshot(after envSnapshot) []string {
	var errs []string
	diff := func(name, before, afterVal string) {
		if before != afterVal {
			errs = append(errs, fmt.Sprintf("%s changed after cleanup:\nbefore: %q\nafter:  %q", name, before, afterVal))
		}
	}
	diff("nexus ps", h.preSnap.prodPS, after.prodPS)
	diff("herdr sessions", h.preSnap.herdrSessions, after.herdrSessions)
	diff("herdr worktrees/nexus", h.preSnap.herdrWorktrees, after.herdrWorktrees)
	diff("git branches in "+mainRepoPath(), h.preSnap.gitBranches, after.gitBranches)
	diff("systemd nl-* units", h.preSnap.systemdUnits, after.systemdUnits)
	diff("live procs /var/tmp/nxl-*", h.preSnap.liveProcs, after.liveProcs)
	diff("prod vault dir checksum", h.preSnap.prodVaultSum, after.prodVaultSum)
	return errs
}

// runCapture runs a command and returns trimmed combined output and any error.
func runCapture(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// listDirEntries returns newline-joined entry names under dir, or "" if missing.
func listDirEntries(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, "\n")
}

// hardLinkImagesDir populates dst from prod image cache: sha256/ blobs are
// hard-linked (read-only); other files (disk images) are sparse-copied.
func hardLinkImagesDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "locks" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		dstPath := filepath.Join(dst, rel)
		if _, existErr := os.Lstat(dstPath); existErr == nil {
			return nil
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if parts[0] == "sha256" {
			if linkErr := os.Link(path, dstPath); linkErr != nil {
				if os.IsExist(linkErr) {
					return nil
				}
				return linkErr
			}
			return nil
		}
		return copyFileSparse(path, dstPath)
	})
}

// copyFileSparse copies src to dst using cp --sparse=always to preserve holes.
func copyFileSparse(src, dst string) error {
	out, err := exec.Command("cp", "--sparse=always", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp --sparse %s -> %s: %w\n%s", src, dst, err, out)
	}
	return nil
}

// checksumLinkedProdFiles returns sha256 checksums for hard-linked files in sha256Dir; blobs >100 MiB skipped.
func checksumLinkedProdFiles(sha256Dir string) map[string]string {
	const maxChecksumSize = 100 * 1024 * 1024
	sums := make(map[string]string)
	_ = filepath.WalkDir(sha256Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.Size() > maxChecksumSize {
			return nil
		}
		sum, checksumErr := fileChecksum(path)
		if checksumErr != nil {
			return nil
		}
		sums[path] = sum
		return nil
	})
	return sums
}

// fileChecksum returns the hex-encoded SHA-256 digest of path.
func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256pkg.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// findProcsReferencingPath returns PIDs whose /proc/*/cmdline contains path.
func findProcsReferencingPath(path string) []int {
	entries, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	var pids []int
	for _, entry := range entries {
		data, err := os.ReadFile(entry)
		if err != nil {
			continue
		}
		if !strings.Contains(string(data), path) {
			continue
		}
		pidStr := filepath.Base(filepath.Dir(entry))
		if pid, err := strconv.Atoi(pidStr); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// waitProcsExit blocks until no processes referencing path remain, or timeout elapses.
func waitProcsExit(path string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(findProcsReferencingPath(path)) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Harness is an isolated nexus+herdr environment for one test.
type Harness struct {
	t              *testing.T
	stateRoot      string // XDG_STATE_HOME — <isolated>/state
	configHome     string // XDG_CONFIG_HOME — <isolated>/config
	dataHome       string // XDG_DATA_HOME  — <isolated>/data
	credsDir       string
	kernelPath     string
	sessionName    string
	socketPath     string
	nexusBin       string // worktree-built binary used to drive isolated sandboxes
	prodNexusBin   string // installed prod nexus from PATH used for prod-state snapshots
	base           string // /var/tmp/nxl-* root, for leak scanning
	worktreeDir    string // <base>/worktrees — herdr worktree checkouts go here
	herdrPluginDir string

	mu      sync.Mutex
	handles []string

	preSnap        envSnapshot
	linkedProdSums map[string]string
}

// New creates an isolated harness for t; fails immediately if isolation cannot be guaranteed.
func New(t *testing.T) *Harness {
	t.Helper()

	base, err := os.MkdirTemp("/var/tmp", "nxl-")
	if err != nil {
		t.Fatalf("livenexus: create base dir: %v", err)
	}

	prodNexus, _ := exec.LookPath("nexus")

	nexusBin, resolveErr := resolveNexusBin()
	if resolveErr != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: %v", resolveErr)
	}

	stateRoot := filepath.Join(base, "state")
	configHome := filepath.Join(base, "config")
	dataHome := filepath.Join(base, "data")
	credsDir := filepath.Join(base, "creds")
	worktreeDir := filepath.Join(base, "worktrees")
	for _, d := range []string{stateRoot, configHome, dataHome, credsDir, worktreeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			_ = os.RemoveAll(base)
			t.Fatalf("livenexus: mkdir %s: %v", d, err)
		}
	}

	vaultKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, vaultKey); err != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: generate vault key: %v", err)
	}
	vaultKeyPath := filepath.Join(credsDir, "nexus-vault-key")
	if err := os.WriteFile(vaultKeyPath, vaultKey, 0o600); err != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: write vault key: %v", err)
	}

	if err := validateStateRoot(filepath.Join(stateRoot, "nexus")); err != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("%v", err)
	}

	session := "nl-" + randHex(8)
	socketPath := filepath.Join(configHome, "herdr", "sessions", session, "herdr.sock")

	if err := validateSocket(socketPath); err != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("%v", err)
	}

	herdrPluginDir := filepath.Join(base, "herdr-plugin")
	if pluginErr := setupHarnessPlugin(herdrPluginDir, configHome, nexusBin); pluginErr != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: setup harness plugin: %v", pluginErr)
	}

	kernelPath := resolveProdKernelPath()

	nexusStateRoot := filepath.Join(stateRoot, "nexus")
	if mkErr := os.MkdirAll(nexusStateRoot, 0o700); mkErr != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: mkdir nexus state: %v", mkErr)
	}

	prodNexusState, _ := prodStateRoot()
	prodImagesDir := filepath.Join(prodNexusState, "images")
	if _, statErr := os.Stat(prodImagesDir); statErr == nil {
		testImagesDir := filepath.Join(nexusStateRoot, "images")
		if mkErr := os.MkdirAll(testImagesDir, 0o700); mkErr != nil {
			_ = os.RemoveAll(base)
			t.Fatalf("livenexus: mkdir test images: %v", mkErr)
		}
		if linkErr := hardLinkImagesDir(prodImagesDir, testImagesDir); linkErr != nil {
			t.Logf("livenexus: hard-link prod images: %v (tests will re-pull/rebuild)", linkErr)
		}
	}

	prodSha256Dir := filepath.Join(prodNexusState, "images", "sha256")
	linkedSums := checksumLinkedProdFiles(prodSha256Dir)

	preSnap := captureEnvSnapshot(prodNexus, base, session)
	preSnap.credsDirSet = true

	h := &Harness{
		t:              t,
		stateRoot:      stateRoot,
		configHome:     configHome,
		dataHome:       dataHome,
		credsDir:       credsDir,
		kernelPath:     kernelPath,
		sessionName:    session,
		socketPath:     socketPath,
		nexusBin:       nexusBin,
		prodNexusBin:   prodNexus,
		base:           base,
		worktreeDir:    worktreeDir,
		herdrPluginDir: herdrPluginDir,
		preSnap:        preSnap,
		linkedProdSums: linkedSums,
	}

	h.startHerdr(t, base)

	t.Cleanup(func() {
		h.cleanup(base)
	})

	return h
}

func (h *Harness) startHerdr(t *testing.T, base string) {
	t.Helper()
	herdrBin, err := resolveHerdrBin()
	if err != nil {
		t.Fatalf("livenexus: %v", err)
	}

	setenvArgs := make([]string, 0, len(h.Env())+10)
	setenvArgs = append(setenvArgs,
		"--user", "--unit="+h.sessionName,
		"-p", "StandardInput=null",
		"-p", "MemoryHigh=8G",
		"-p", "MemoryMax=10G",
		"-p", "OOMScoreAdjust=1000",
	)
	for _, kv := range h.Env() {
		setenvArgs = append(setenvArgs, "--setenv="+kv)
	}
	setenvArgs = append(setenvArgs, herdrBin, "--session", h.sessionName, "server")

	cmd := exec.Command("systemd-run", setenvArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: systemd-run herdr server: %v\n%s", err, out)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(h.socketPath); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(h.socketPath); err != nil {
		_ = exec.Command("systemctl", "--user", "stop", h.sessionName+".service").Run()
		_ = os.RemoveAll(base)
		t.Fatalf("livenexus: herdr socket %s not ready after 20s", h.socketPath)
	}
}

// sweepIsolatedSandboxes errors on sandboxes in the isolated root not caught by tracked teardown.
func (h *Harness) sweepIsolatedSandboxes(ctx context.Context) {
	cmd := exec.CommandContext(ctx, h.nexusBin, "ps")
	cmd.Env = h.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "HANDLE" || strings.Contains(fields[1], "sandbox") {
			continue
		}
		h.t.Errorf("livenexus: sweep found leaked sandbox %q — Teardown must remove it", fields[0])
		h.teardownHandle(ctx, fields[0])
	}
}

// cleanup is registered with t.Cleanup and runs even on test failure or panic.
func (h *Harness) cleanup(base string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if h.t.Failed() || os.Getenv("NEXUS_LIVE_KEEP_LOGS") == "1" {
		if dest := preserveLogs(base); dest != "" {
			h.t.Logf("livenexus: logs preserved at %s", dest)
		}
	}

	h.mu.Lock()
	tracked := append([]string(nil), h.handles...)
	h.mu.Unlock()
	for _, handle := range tracked {
		h.teardownHandle(ctx, handle)
	}

	h.sweepIsolatedSandboxes(ctx)

	_ = exec.Command("systemctl", "--user", "stop", h.sessionName+".service").Run()
	_ = exec.Command("herdr", "--session", h.sessionName, "session", "delete", h.sessionName).Run()
	sessionDir := filepath.Join(h.configHome, "herdr", "sessions", h.sessionName)
	_ = os.RemoveAll(sessionDir)

	waitProcsExit(base, 60*time.Second)

	after := captureEnvSnapshot(h.prodNexusBin, base, h.sessionName)

	_ = os.RemoveAll(base)

	leakedPIDs := findProcsReferencingPath(base)
	for _, pid := range leakedPIDs {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if len(leakedPIDs) > 0 {
		h.t.Errorf("livenexus: leaked %d process(es) referencing %s after cleanup (killed): %v",
			len(leakedPIDs), base, leakedPIDs)
	}

	for _, e := range h.checkSnapshot(after) {
		h.t.Errorf("livenexus: prod isolation violation — %s", e)
	}

	for path, want := range h.linkedProdSums {
		got, err := fileChecksum(path)
		if err != nil {
			h.t.Errorf("livenexus: prod integrity: checksum %s: %v", path, err)
			continue
		}
		if got != want {
			h.t.Errorf("livenexus: prod integrity violation — %s modified during test", path)
		}
	}
}

// preserveLogs copies *.log files to /var/tmp/nxl-logs/<basename>/; returns dest or "".
func preserveLogs(base string) string {
	dest := filepath.Join("/var/tmp", "nxl-logs", filepath.Base(base))
	var copied int
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".log") {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		dstPath := filepath.Join(dest, rel)
		if mkErr := os.MkdirAll(filepath.Dir(dstPath), 0o755); mkErr != nil {
			return nil
		}
		src, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer src.Close()
		dst, createErr := os.Create(dstPath)
		if createErr != nil {
			return nil
		}
		defer dst.Close()
		if _, cpErr := io.Copy(dst, src); cpErr != nil {
			return nil
		}
		copied++
		return nil
	})
	if err != nil || copied == 0 {
		return ""
	}
	return dest
}

func (h *Harness) teardownHandle(ctx context.Context, handle string) {
	args := []string{"sandbox", "rm", handle}
	cmd := exec.CommandContext(ctx, h.nexusBin, args...)
	cmd.Env = h.Env()
	_ = cmd.Run()
}

// Env returns the environment slice for child processes running in the isolated state.
func (h *Harness) Env() []string {
	home, _ := os.UserHomeDir()
	localBin := filepath.Join(home, ".local", "bin")
	path := os.Getenv("PATH")
	if !strings.Contains(path, localBin) {
		path = localBin + ":" + path
	}

	skipKeys := map[string]bool{
		"XDG_STATE_HOME": true, "XDG_DATA_HOME": true, "XDG_CONFIG_HOME": true,
		"HERDR_SOCKET_PATH": true, "TMPDIR": true, "PATH": true,
		"CREDENTIALS_DIRECTORY":        true,
		"NEXUS_BIN":                    true,
		"NEXUS_HERDR_DOCKER_DISK_GIB":  true,
		"NEXUS_HERDR_GOCACHE_DISK_GIB": true,
		"NEXUS_HERDR_GOPATH_DISK_GIB":  true,
		"NEXUS_DISK_FLOOR_GIB":         true,
	}
	if h.kernelPath != "" {
		skipKeys["NEXUS_KERNEL_PATH"] = true
	}
	base := make([]string, 0, len(os.Environ())+10)
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if skipKeys[k] {
			continue
		}
		base = append(base, e)
	}
	env := append(base,
		"XDG_STATE_HOME="+h.stateRoot,
		"XDG_CONFIG_HOME="+h.configHome,
		"XDG_DATA_HOME="+h.dataHome,
		"HERDR_SOCKET_PATH="+h.socketPath,
		"TMPDIR=/var/tmp",
		"PATH="+path,
		"NEXUS_BIN="+h.nexusBin,
		"NEXUS_HERDR_DOCKER_DISK_GIB=2",
		"NEXUS_HERDR_GOCACHE_DISK_GIB=2",
		"NEXUS_HERDR_GOPATH_DISK_GIB=2",
		"NEXUS_DISK_FLOOR_GIB=2",
	)
	if h.credsDir != "" {
		env = append(env, "CREDENTIALS_DIRECTORY="+h.credsDir)
	}
	if h.kernelPath != "" {
		env = append(env, "NEXUS_KERNEL_PATH="+h.kernelPath)
	}
	return env
}

func (h *Harness) CredsDir() string     { return h.credsDir }
func (h *Harness) VaultKeyPath() string { return filepath.Join(h.credsDir, "nexus-vault-key") }

// Run executes nexusBin in the isolated environment; refuses "prune" argv or prod state root.
func (h *Harness) Run(ctx context.Context, args ...string) (string, error) {
	for _, a := range args {
		if strings.Contains(a, "prune") {
			return "", fmt.Errorf("livenexus: Run refuses argv containing 'prune': %v", args)
		}
	}
	nexusStateRoot := filepath.Join(h.stateRoot, "nexus")
	if err := validateStateRoot(nexusStateRoot); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, h.nexusBin, args...)
	cmd.Env = h.Env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunHerdr executes herdr with args inside the isolated environment. Refuses "prune" argv.
func (h *Harness) RunHerdr(ctx context.Context, args ...string) (string, error) {
	for _, a := range args {
		if strings.Contains(a, "prune") {
			return "", fmt.Errorf("livenexus: RunHerdr refuses argv containing 'prune': %v", args)
		}
	}
	herdrBin, err := resolveHerdrBin()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, herdrBin, args...)
	cmd.Env = h.Env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Track registers handle for teardown in cleanup. Only tracked handles are removed.
func (h *Harness) Track(handle string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handles = append(h.handles, handle)
}

// SessionName returns the herdr session name for this harness.
func (h *Harness) SessionName() string { return h.sessionName }

// SocketPath returns the HERDR_SOCKET_PATH for this harness.
func (h *Harness) SocketPath() string { return h.socketPath }

// StateRoot returns XDG_STATE_HOME for this harness.
func (h *Harness) StateRoot() string { return h.stateRoot }
func (h *Harness) DataHome() string  { return h.dataHome }

// SnapshotLogs copies *.log files to /var/tmp/nxl-logs/<base>/; call before teardown.
func (h *Harness) SnapshotLogs() {
	if !h.t.Failed() && os.Getenv("NEXUS_LIVE_KEEP_LOGS") != "1" {
		return
	}
	if dest := preserveLogs(h.base); dest != "" {
		h.t.Logf("livenexus: logs snapshot at %s", dest)
	}
}

// NexusBin returns the nexus binary path.
func (h *Harness) NexusBin() string { return h.nexusBin }

// WorktreeDir returns the isolated herdr worktree checkout directory.
func (h *Harness) WorktreeDir() string { return h.worktreeDir }

func resolveHerdrBin() (string, error) {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("herdr binary not found (HERDR_BIN_PATH unset, not in PATH)")
}

func worktreeRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("livenexus: worktree root %s: no go.mod: %w", root, err)
	}
	return root, nil
}

func parseMainWorktreePath(porcelain string) (string, error) {
	for _, line := range strings.Split(porcelain, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			return strings.TrimSpace(p), nil
		}
	}
	return "", fmt.Errorf("livenexus: no worktree line in git worktree list output")
}

func mainRepoPath() string {
	if p := os.Getenv("NEXUS_LIVE_MAIN_REPO"); p != "" {
		return p
	}
	root, err := worktreeRoot()
	if err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", root, "worktree", "list", "--porcelain").Output()
	if err == nil {
		if p, pErr := parseMainWorktreePath(string(out)); pErr == nil {
			return p
		}
	}
	return root
}

func cutEnv(e string) (key, val string, ok bool) {
	for i, c := range e {
		if c == '=' {
			return e[:i], e[i+1:], true
		}
	}
	return "", "", false
}

func (h *Harness) ExtraEnv() []string {
	keys := map[string]bool{
		"XDG_STATE_HOME":               true,
		"XDG_DATA_HOME":                true,
		"XDG_CONFIG_HOME":              true,
		"CREDENTIALS_DIRECTORY":        true,
		"NEXUS_KERNEL_PATH":            true,
		"TMPDIR":                       true,
		"NEXUS_DISK_FLOOR_GIB":         true,
		"NEXUS_HERDR_DOCKER_DISK_GIB":  true,
		"NEXUS_HERDR_GOCACHE_DISK_GIB": true,
		"NEXUS_HERDR_GOPATH_DISK_GIB":  true,
	}
	var out []string
	for _, e := range h.Env() {
		k, _, ok := cutEnv(e)
		if ok && keys[k] {
			out = append(out, e)
		}
	}
	return out
}

func probeVaultSupport(bin string) bool {
	out, _ := exec.Command(bin, "help").CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == "vault" {
			return true
		}
	}
	return false
}

// buildNexusBin builds the nexus binary into base and returns its path.
func buildNexusBin(base string) (string, error) {
	root, err := worktreeRoot()
	if err != nil {
		return "", err
	}
	out := filepath.Join(base, "nexus")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/nexus")
	cmd.Dir = root
	if buildOut, buildErr := cmd.CombinedOutput(); buildErr != nil {
		return "", fmt.Errorf("livenexus: go build nexus: %w\n%s", buildErr, buildOut)
	}
	if !probeVaultSupport(out) {
		return "", fmt.Errorf("livenexus: built nexus binary at %s lacks vault support", out)
	}
	return out, nil
}

const nexusE2EBin = "/var/tmp/nexus-e2e-bin/nexus"

// nexusBinOnce guards the single per-process build of the nexus binary.
var (
	nexusBinOnce sync.Once
	nexusBinPath string
	nexusBinErr  error
)

// resolveNexusBin returns the nexus binary (NEXUS_BIN if set; else built once per process).
func resolveNexusBin() (string, error) {
	if p := os.Getenv("NEXUS_BIN"); p != "" {
		if !probeVaultSupport(p) {
			return "", fmt.Errorf("livenexus: NEXUS_BIN=%s lacks vault support", p)
		}
		return p, nil
	}
	nexusBinOnce.Do(func() {
		dir := filepath.Dir(nexusE2EBin)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			nexusBinErr = fmt.Errorf("livenexus: mkdir nexus-e2e-bin: %w", err)
			return
		}
		root, err := worktreeRoot()
		if err != nil {
			nexusBinErr = err
			return
		}
		tmp := fmt.Sprintf("%s/nexus.tmp.%d", dir, os.Getpid())
		cmd := exec.Command("go", "build", "-o", tmp, "./cmd/nexus")
		cmd.Dir = root
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			nexusBinErr = fmt.Errorf("livenexus: go build nexus: %w\n%s", buildErr, out)
			return
		}
		if err := os.Rename(tmp, nexusE2EBin); err != nil {
			_ = os.Remove(tmp)
			nexusBinErr = fmt.Errorf("livenexus: rename nexus bin: %w", err)
			return
		}
		if !probeVaultSupport(nexusE2EBin) {
			nexusBinErr = fmt.Errorf("livenexus: built nexus binary at %s lacks vault support", nexusE2EBin)
			return
		}
		nexusBinPath = nexusE2EBin
	})
	return nexusBinPath, nexusBinErr
}

func resolveProdKernelPath() string {
	if p := os.Getenv("NEXUS_KERNEL_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "share", "nexus", "images", "kernel", "vmlinux-x86_64"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func setupHarnessPlugin(pluginDir, configHome, nexusBin string) error {
	root, err := worktreeRoot()
	if err != nil {
		return fmt.Errorf("setupHarnessPlugin: %w", err)
	}
	srcPlugin := filepath.Join(root, "plugins", "herdr")
	if _, err := os.Stat(srcPlugin); err != nil {
		return fmt.Errorf("setupHarnessPlugin: plugin src %s: %w", srcPlugin, err)
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return fmt.Errorf("setupHarnessPlugin: mkdir: %w", err)
	}
	if err := os.CopyFS(pluginDir, os.DirFS(srcPlugin)); err != nil {
		return fmt.Errorf("setupHarnessPlugin: copy: %w", err)
	}
	shimContent := fmt.Sprintf("#!/bin/sh\nexec \"${NEXUS_BIN:-%s}\" \"$@\"\n", nexusBin)
	if err := os.WriteFile(filepath.Join(pluginDir, "nexus-shim.sh"), []byte(shimContent), 0o755); err != nil {
		return fmt.Errorf("setupHarnessPlugin: write shim: %w", err)
	}
	binDir := filepath.Join(pluginDir, "bin")
	_ = filepath.WalkDir(binDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	// Install nexus-guest-shell into a harness-local bin dir so herdr launches
	// the guest-shell path on worktree panes (matching prod behaviour).
	harnessLocalBin := filepath.Join(filepath.Dir(configHome), "bin")
	if err := os.MkdirAll(harnessLocalBin, 0o755); err != nil {
		return fmt.Errorf("setupHarnessPlugin: mkdir harness bin: %w", err)
	}
	guestShellPath := filepath.Join(harnessLocalBin, "nexus-guest-shell")
	_ = os.Remove(guestShellPath)
	if err := os.Symlink(nexusBin, guestShellPath); err != nil {
		return fmt.Errorf("setupHarnessPlugin: symlink nexus-guest-shell: %w", err)
	}
	sidecarPath := guestShellPath + ".nexusbin"
	// Sidecar format: line 1 = real nexus binary path, line 2 = kernel path (empty OK).
	if err := os.WriteFile(sidecarPath, []byte(nexusBin+"\n\n"), 0o644); err != nil {
		return fmt.Errorf("setupHarnessPlugin: write nexus-guest-shell sidecar: %w", err)
	}

	herdrCfgDir := filepath.Join(configHome, "herdr")
	if err := os.MkdirAll(herdrCfgDir, 0o700); err != nil {
		return fmt.Errorf("setupHarnessPlugin: mkdir herdr cfg: %w", err)
	}
	herdrConfigTOML := "onboarding = false\n\n[terminal]\ndefault_shell = " + fmt.Sprintf("%q", guestShellPath) + "\n"
	if err := os.WriteFile(filepath.Join(herdrCfgDir, "config.toml"), []byte(herdrConfigTOML), 0o600); err != nil {
		return fmt.Errorf("setupHarnessPlugin: write config.toml: %w", err)
	}
	pluginsJSON, err := buildHarnessPluginsJSON(pluginDir, filepath.Join(pluginDir, "herdr-plugin.toml"))
	if err != nil {
		return fmt.Errorf("setupHarnessPlugin: build plugins.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(herdrCfgDir, "plugins.json"), pluginsJSON, 0o600); err != nil {
		return fmt.Errorf("setupHarnessPlugin: write plugins.json: %w", err)
	}
	return nil
}

func buildHarnessPluginsJSON(pluginDir, manifestPath string) ([]byte, error) {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".config", "herdr", "plugins.json"))
	if err != nil {
		return nil, fmt.Errorf("read real plugins.json: %w", err)
	}
	var plugins []map[string]any
	if err := json.Unmarshal(data, &plugins); err != nil {
		return nil, fmt.Errorf("parse plugins.json: %w", err)
	}
	var nexusEntry map[string]any
	for _, p := range plugins {
		if id, _ := p["plugin_id"].(string); id == "nexus" {
			nexusEntry = p
			break
		}
	}
	if nexusEntry == nil {
		return nil, fmt.Errorf("nexus plugin not found in plugins.json; install: herdr plugin install IniZio/nexus/plugins/herdr")
	}
	nexusEntry["plugin_root"] = pluginDir
	nexusEntry["manifest_path"] = manifestPath
	nexusEntry["source"] = map[string]any{"kind": "local"}
	delete(nexusEntry, "startup")
	return json.MarshalIndent([]map[string]any{nexusEntry}, "", "  ")
}
