// Package livenexus provides an isolated live-test harness for nexus integration tests.
//
// Every live test that touches sandboxes, herdr worktrees, or herdr sessions MUST
// use this harness. It guarantees:
//
//   - Nexus state (XDG_STATE_HOME) lives in a fresh /var/tmp directory, never the
//     prod ~/.local/state/nexus tree.
//   - A dedicated herdr server runs under a unique systemd unit, so no command
//     reaches the prod herdr session.
//   - Prod image cache is hard-linked read-only into the test root (no writable
//     symlink into prod state; new images written by the test land in the test root).
//   - Herdr worktree checkouts go under <base>/worktrees, not ~/.herdr/worktrees.
//   - Cleanup order: rm each tracked sandbox → wait for supervisor PIDs → stop
//     herdr unit → remove test root → kill+report any leaked processes.
//   - Cleanup runs even when the test fails or panics (via t.Cleanup).
//   - Cleanup fails the test if any process referencing the test root survives.
//   - Cleanup compares before/after prod invariants and fails on any difference.
//   - Cleanup never touches any resource it did not create.
//   - Run refuses any argv containing "prune" and refuses to run when the resolved
//     state root matches the prod path.
//
// Kernel: NEXUS_KERNEL_PATH is forwarded unchanged from the host environment so
// the real kernel binary is reused read-only. All other nexus data (store,
// bindings, caches) starts empty inside the isolated state root.
//
// Herdr config: XDG_CONFIG_HOME is overridden to <base>/config so that the
// harness herdr server loads a harness-owned copy of the nexus plugin built
// from this worktree. Its shim respects NEXUS_BIN, ensuring panes run inside
// the guest VM rather than on the host. The session socket lives at
// <base>/config/herdr/sessions/<session>/herdr.sock.
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

// prodStateRoot resolves the nexus state root that the current environment uses
// without any override from this harness. Used for the collision guard.
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

// envSnapshot captures a point-in-time view of prod-side resources for leak detection.
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

// checksumDir returns a sorted newline-separated listing of "<name> <sha256>" for
// every regular file under dir. Used to detect prod-vault mutations.
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
	// WalkDir already returns entries in lexical order; no sort needed.
	return strings.Join(lines, "\n")
}

// captureEnvSnapshot records current prod-side environment state.
// base and session scope the per-harness fields to avoid false-positive
// violations caused by concurrent harnesses running in parallel.
// base="" → liveProcs not checked; session="" → session fields not checked.
func captureEnvSnapshot(nexusBin, base, session string) envSnapshot {
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
		out := runCapture("systemctl", "--user", "list-units", "--all", session+".service", "--no-legend")
		if strings.TrimSpace(out) != "" {
			systemdUnits = out
		}
	}
	return envSnapshot{
		prodPS:         runCapture(nexusBin, "ps"),
		herdrSessions:  herdrSessions,
		herdrWorktrees: listDirEntries(filepath.Join(home, ".herdr", "worktrees", "nexus")),
		gitBranches:    runCapture("git", "-C", "/home/newman/magic/nexus", "branch", "--list"),
		systemdUnits:   systemdUnits,
		liveProcs:      liveProcs,
		prodVaultSum:   checksumDir(prodVaultDir()),
	}
}

// checkSnapshot compares the stored before-snapshot against after, returning one
// error string per changed field.
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
	diff("git branches in /home/newman/magic/nexus", h.preSnap.gitBranches, after.gitBranches)
	diff("systemd nl-* units", h.preSnap.systemdUnits, after.systemdUnits)
	diff("live procs /var/tmp/nxl-*", h.preSnap.liveProcs, after.liveProcs)
	diff("prod vault dir checksum", h.preSnap.prodVaultSum, after.prodVaultSum)
	return errs
}

// runCapture runs a command and returns trimmed combined output; never fails the
// test — command errors are silently ignored so snapshot capture is always safe.
func runCapture(name string, args ...string) string {
	out, _ := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// listDirEntries returns a newline-joined sorted list of directory entry names,
// or "" when the directory does not exist.
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

// hardLinkImagesDir populates dst with the prod image cache, skipping locks/.
//
// Only files under sha256/ are hard-linked: those are content-addressed blobs
// opened read-only by the OCI store, so sharing their inode is safe.
// Everything else (*.ext4, *.img, and any other file a VM or builder may open
// read-write) is copied sparse via `cp --sparse=always` so test writes never
// mutate prod inodes.
func hardLinkImagesDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			// skip the locks/ subtree — per-test locking must use the test root
			if d.Name() == "locks" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		dstPath := filepath.Join(dst, rel)
		// Skip if already present.
		if _, existErr := os.Lstat(dstPath); existErr == nil {
			return nil
		}
		// Only hard-link immutable content-addressed blobs under sha256/.
		// The OCI content store opens these blobs read-only; sharing the inode
		// is safe. All other files (disk images, ext4 volumes) may be opened
		// read-write by test VMs or builders and must be copied.
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

// copyFileSparse copies src to dst using cp --sparse=always, which preserves
// holes in disk images without reading or writing unnecessary zero blocks.
func copyFileSparse(src, dst string) error {
	out, err := exec.Command("cp", "--sparse=always", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp --sparse %s -> %s: %w\n%s", src, dst, err, out)
	}
	return nil
}

// checksumLinkedProdFiles returns sha256 checksums for small files under
// sha256Dir that hardLinkImagesDir hard-links. Large blobs (>maxChecksumSize)
// are skipped to bound runtime; they are content-addressed so their names
// already encode integrity.
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

// findProcsReferencingPath scans /proc for processes whose cmdline contains path.
// Returns a slice of PIDs (may be empty).
func findProcsReferencingPath(path string) []int {
	entries, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	var pids []int
	for _, entry := range entries {
		data, err := os.ReadFile(entry)
		if err != nil {
			continue
		}
		// cmdline is NUL-separated; treat as a single byte slice for Contains
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
	nexusBin       string
	base           string // /var/tmp/nxl-* root, for leak scanning
	worktreeDir    string // <base>/worktrees — herdr worktree checkouts go here
	herdrPluginDir string

	mu      sync.Mutex
	handles []string

	preSnap        envSnapshot
	linkedProdSums map[string]string
}

// New creates an isolated harness for t. It:
//  1. Creates a temp dir under /var/tmp.
//  2. Hard-links the prod image cache read-only into the test root.
//  3. Starts a herdr server in its own systemd user unit.
//  4. Registers cleanup in t.Cleanup (runs even on test failure or panic).
//
// Fails the test immediately when isolation cannot be guaranteed.
func New(t *testing.T) *Harness {
	t.Helper()

	base, err := os.MkdirTemp("/var/tmp", "nxl-")
	if err != nil {
		t.Fatalf("livenexus: create base dir: %v", err)
	}

	// Resolve (or build) the nexus binary inside base so it is cleaned up with it.
	nexusBin, resolveErr := resolveNexusBin(base)
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

	// Hard-link the prod image cache into the test root.
	// Hard links are instant (no data copy), let the test read cached images, and
	// ensure test writes (new cache entries) land in the isolated root — prod files
	// are never modified or deleted by the test, and there is no writable symlink
	// into prod state.
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

	preSnap := captureEnvSnapshot(nexusBin, base, session)
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

	// Build --setenv flags from the full isolated env (h.Env()) so the herdr
	// server inherits XDG_STATE_HOME, XDG_DATA_HOME, CREDENTIALS_DIRECTORY,
	// NEXUS_KERNEL_PATH, NEXUS_BIN, PATH, TMPDIR, etc. without duplicating the list.
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

// sweepIsolatedSandboxes removes all nexus sandboxes visible in the isolated
// state root. This is a safety net for sandboxes whose herdr workspace was
// removed by the backend teardown but whose nexus supervisor was never told to
// stop. It only touches sandboxes inside h's isolated XDG_STATE_HOME.
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

// cleanup is registered with t.Cleanup and runs even when the test fails or panics.
// Order:
//  0. Preserve logs (before any teardown so supervisor state dirs still exist).
//  1. rm every tracked sandbox handle.
//  2. safety sweep: rm any sandboxes in the isolated root not caught by step 1.
//  3. wait for supervisor PIDs referencing this root to exit.
//  4. stop the herdr systemd unit and remove its session dir.
//  5. remove the test root directory.
//  6. kill+report any process still referencing the (now-deleted) root path.
//  7. compare before/after prod invariants; fail on any difference.
func (h *Harness) cleanup(base string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 0. Preserve logs before teardown: supervisor state dirs (and their logs)
	// are deleted by service.Remove inside teardownHandle. Snapshotting here
	// ensures per-workspace supervisor.log files are captured even on failure.
	if h.t.Failed() || os.Getenv("NEXUS_LIVE_KEEP_LOGS") == "1" {
		if dest := preserveLogs(base); dest != "" {
			h.t.Logf("livenexus: logs preserved at %s", dest)
		}
	}

	// 1. Remove all tracked sandbox handles.
	h.mu.Lock()
	tracked := append([]string(nil), h.handles...)
	h.mu.Unlock()
	for _, handle := range tracked {
		h.teardownHandle(ctx, handle)
	}

	// 2. Safety sweep: rm any nexus sandboxes in the isolated root that were not
	h.sweepIsolatedSandboxes(ctx)

	// 3. Stop herdr unit first so its nexus child processes release the store root.
	_ = exec.Command("systemctl", "--user", "stop", h.sessionName+".service").Run()
	_ = exec.Command("herdr", "--session", h.sessionName, "session", "delete", h.sessionName).Run()
	sessionDir := filepath.Join(h.configHome, "herdr", "sessions", h.sessionName)
	_ = os.RemoveAll(sessionDir)

	// nexus __supervisor processes hold a reference to the store root; they must
	// exit before os.RemoveAll succeeds cleanly.
	waitProcsExit(base, 60*time.Second)

	// 4. Capture after-snapshot before removing base: the nexus binary lives
	// inside base, so it must still exist when we run `nexus ps`.
	after := captureEnvSnapshot(h.nexusBin, base, h.sessionName)

	// 5. Remove the test root.
	_ = os.RemoveAll(base)

	// 6. Kill+report any process still referencing our root path.
	leakedPIDs := findProcsReferencingPath(base)
	for _, pid := range leakedPIDs {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if len(leakedPIDs) > 0 {
		h.t.Errorf("livenexus: leaked %d process(es) referencing %s after cleanup (killed): %v",
			len(leakedPIDs), base, leakedPIDs)
	}

	// 7. Compare before/after prod invariants.
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

// preserveLogs copies all regular *.log files under base into
// /var/tmp/nxl-logs/<basename-of-base>/ preserving relative paths.
// Returns the destination directory on success, or "" on failure.
// Never deletes anything.
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

// Env returns the environment slice that child processes must use to run
// inside the isolated state. It includes XDG_STATE_HOME, XDG_CONFIG_HOME,
// XDG_DATA_HOME, HERDR_SOCKET_PATH, TMPDIR, and PATH.
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
		"CREDENTIALS_DIRECTORY": true,
		"NEXUS_BIN":             true,
		// Volume size / disk-floor overrides: harness sets small values below.
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

func (h *Harness) CredsDir() string    { return h.credsDir }
func (h *Harness) VaultKeyPath() string { return filepath.Join(h.credsDir, "nexus-vault-key") }

// Run executes nexusBin with args inside the isolated environment.
// It refuses any argv containing "prune" and refuses to run when the
// resolved state root matches prod.
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

// RunHerdr executes herdr with args inside the isolated environment.
// It refuses any argv containing "prune".
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

// Track registers handle for cleanup. In t.Cleanup, only tracked handles are
// removed — never a wildcard or list-based sweep.
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

// SnapshotLogs copies all *.log files under the harness base directory to
// /var/tmp/nxl-logs/<base-name>/ and logs the destination. Call this from
// a test's t.Cleanup BEFORE any teardown that deletes supervisor state dirs,
// so that per-workspace supervisor.log files are preserved on failure.
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

// WorktreeDir returns the directory for herdr worktree checkouts within the
// isolated test root. Pass this to backend.Config.WorktreeDir so that worktree
// git checkouts land here and not under ~/.herdr/worktrees.
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

func probeVaultSupport(bin string) bool {
	out, err := exec.Command(bin, "vault", "ls").CombinedOutput()
	s := string(out)
	// If the exec itself failed (binary missing, not executable, crashed) and the
	// output doesn't contain the "unknown command" strings, the binary can't be
	// probed — treat as no vault support.
	if err != nil && !strings.Contains(s, "unknown command") && !strings.Contains(s, "command not found") {
		return false
	}
	return !strings.Contains(s, "unknown command") && !strings.Contains(s, "command not found")
}

// buildNexusBin builds the nexus binary into base (which is the per-test
// isolated root). It returns the path to the binary. The binary is cleaned
// up automatically when base is removed in t.Cleanup.
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
// It always runs go build (incremental — cheap when nothing changed) so that
// code changes made between test runs are never missed.
var (
	nexusBinOnce sync.Once
	nexusBinPath string
	nexusBinErr  error
)

// resolveNexusBin returns the nexus binary path. When NEXUS_BIN is set it is
// used directly. Otherwise the binary is built once per test process via
// sync.Once: go build writes to a temp file then atomically renames it onto
// nexusE2EBin so concurrent test packages already executing the old inode are
// unaffected ("text file busy" avoided).
func resolveNexusBin(_ string) (string, error) {
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
		// Build into a temp file so the rename onto nexusE2EBin is atomic.
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
	herdrCfgDir := filepath.Join(configHome, "herdr")
	if err := os.MkdirAll(herdrCfgDir, 0o700); err != nil {
		return fmt.Errorf("setupHarnessPlugin: mkdir herdr cfg: %w", err)
	}
	if err := os.WriteFile(filepath.Join(herdrCfgDir, "config.toml"), []byte("onboarding = false\n"), 0o600); err != nil {
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
