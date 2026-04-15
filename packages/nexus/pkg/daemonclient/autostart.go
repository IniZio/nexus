// Package daemonclient provides helpers for locating, checking liveness of,
// and auto-starting the nexus workspace daemon from the CLI.
package daemonclient

import (
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
)

var daemonProcessCommandLineFn = daemonProcessCommandLine
var daemonProcessStartedAtFn = daemonProcessStartedAt

const (
	defaultDaemonPort   = 63987
	localDriverPortBase = 64100
	localDriverPortSpan = 900
)

// RunDir returns the platform directory used for daemon runtime files
// (PID file, token file, log file).
// It respects $XDG_RUNTIME_DIR if set; otherwise uses ~/.config/nexus/run.
func RunDir() (string, error) {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "nexus"), nil
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "nexus", "run"), nil
}

// PreferredPort resolves the daemon port for the current process context.
// Explicit NEXUS_DAEMON_PORT overrides all auto-selection.
func PreferredPort() int {
	if v := strings.TrimSpace(os.Getenv("NEXUS_DAEMON_PORT")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			return p
		}
	}
	root, ok := LocalDriverWorktreeRoot(".")
	if !ok {
		return defaultDaemonPort
	}
	port, err := SelectPortForWorktreeRoot(root)
	if err != nil {
		return defaultDaemonPort
	}
	return port
}

// LocalDriverWorktreeRoot returns the repository root when .nexus/workspace.json
// enables internalFeatures.localDriver.
func LocalDriverWorktreeRoot(startPath string) (string, bool) {
	startPath = strings.TrimSpace(startPath)
	if startPath == "" {
		startPath = "."
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return "", false
	}
	current := filepath.Clean(abs)
	for {
		cfgPath := filepath.Join(current, ".nexus", "workspace.json")
		if st, err := os.Stat(cfgPath); err == nil && !st.IsDir() {
			cfg, _, loadErr := config.LoadWorkspaceConfig(current)
			if loadErr == nil && cfg.InternalFeatures.LocalDriver {
				return canonicalPath(current), true
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", false
}

// SelectPortForWorktreeRoot chooses a daemon port for the given worktree root.
// It prefers a deterministic hash-derived port and linearly probes the reserved range.
func SelectPortForWorktreeRoot(worktreeRoot string) (int, error) {
	canonical := canonicalPath(worktreeRoot)
	if canonical == "" {
		return defaultDaemonPort, nil
	}
	runDir, err := RunDir()
	if err != nil {
		return defaultDaemonPort, err
	}
	preferred := preferredLocalDriverPort(canonical)
	for offset := 0; offset < localDriverPortSpan; offset++ {
		candidate := localDriverPortBase + ((preferred - localDriverPortBase + offset) % localDriverPortSpan)
		owner := strings.TrimSpace(readDaemonOwner(runDir, candidate))
		if IsRunning(candidate) {
			if owner == canonical {
				return candidate, nil
			}
			continue
		}
		if owner != "" && owner != canonical {
			continue
		}
		return candidate, nil
	}
	return preferred, nil
}

func preferredLocalDriverPort(canonicalRoot string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(canonicalRoot))
	return localDriverPortBase + int(h.Sum32()%localDriverPortSpan)
}

func canonicalPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	resolved := abs
	if real, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(real) != "" {
		resolved = real
	}
	return filepath.Clean(resolved)
}

func ownerFilePath(runDir string, port int) string {
	return filepath.Join(runDir, fmt.Sprintf("daemon-%d.owner", port))
}

func readDaemonOwner(runDir string, port int) string {
	data, err := os.ReadFile(ownerFilePath(runDir, port))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeDaemonOwner(runDir string, port int, worktreeRoot string) error {
	root := canonicalPath(worktreeRoot)
	if strings.TrimSpace(root) == "" {
		return nil
	}
	return os.WriteFile(ownerFilePath(runDir, port), []byte(root+"\n"), 0o644)
}

// defaultWorkspaceDir returns the default directory for workspace storage.
// It respects $XDG_DATA_HOME if set; otherwise falls back to ~/.nexus/workspaces.
func defaultWorkspaceDir() string {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "nexus", "workspaces")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nexus", "workspaces")
}

// DefaultDataDir returns the absolute path for daemon persistent data
// ($XDG_DATA_HOME/nexus or ~/.local/share/nexus), matching config.DefaultConfig.
func DefaultDataDir() (string, error) {
	if d := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); d != "" {
		return filepath.Join(d, "nexus"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("daemon data dir: %w", err)
	}
	return filepath.Join(home, ".local", "share", "nexus"), nil
}

// TokenPath returns the legacy path of the per-user daemon token file (runtime dir).
func TokenPath() (string, error) {
	d, err := RunDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "token"), nil
}

// ReadDaemonToken reads the secret the workspace daemon uses for JWT auth.
// It prefers $XDG_DATA_HOME/nexus/token (or ~/.local/share/nexus/token), then
// falls back to the legacy TokenPath location for transitional compatibility.
func ReadDaemonToken() (string, error) {
	dataDir, err := DefaultDataDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, "token")
	data, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(data))
		if tok != "" {
			return tok, nil
		}
	}
	legacy, legErr := TokenPath()
	if legErr != nil {
		if err != nil {
			return "", fmt.Errorf("read daemon token: %w", err)
		}
		return "", fmt.Errorf("read daemon token: %w", legErr)
	}
	data, err = os.ReadFile(legacy)
	if err != nil {
		return "", fmt.Errorf("read daemon token: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("read daemon token: empty file")
	}
	return tok, nil
}

// IsRunning returns true if the daemon is accepting requests on the given port.
func IsRunning(port int) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/healthz", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// EnsureRunning starts the daemon in the background if it is not already
// listening on port. It locates the daemon binary next to the current
// executable (or via $PATH), writes a PID file to RunDir(), and polls
// /healthz for up to 5 seconds before returning.
//
// tokenForDaemon, if non-empty, is passed as --token (e.g. NEXUS_DAEMON_TOKEN).
// If empty, the daemon loads or creates the token in its data directory.
// workspaceDir is passed as --workspace-dir to the daemon (empty uses default).
func EnsureRunning(port int, workspaceDir string, tokenForDaemon string) error {
	return EnsureRunningForWorktree(port, workspaceDir, tokenForDaemon, "")
}

// EnsureRunningForWorktree starts or reuses the daemon for a specific worktree root.
// When worktreeRoot is non-empty, a daemon owner record is written per port.
func EnsureRunningForWorktree(port int, workspaceDir string, tokenForDaemon string, worktreeRoot string) error {
	daemonBin, err := resolveDaemonBin()
	if err != nil {
		return fmt.Errorf("daemonclient: cannot find nexus-daemon binary: %w", err)
	}

	runDir, err := RunDir()
	if err != nil {
		return fmt.Errorf("daemonclient: run dir: %w", err)
	}

	if IsRunning(port) {
		if root := canonicalPath(worktreeRoot); root != "" {
			owner := readDaemonOwner(runDir, port)
			if owner != "" && owner != root {
				return fmt.Errorf("daemonclient: daemon on :%d is owned by worktree %s", port, owner)
			}
		}
		restart, err := shouldRestartRunningDaemon(port, daemonBin)
		if err != nil {
			return fmt.Errorf("daemonclient: inspect running daemon: %w", err)
		}
		if !restart {
			if err := writeDaemonOwner(runDir, port, worktreeRoot); err != nil {
				return fmt.Errorf("daemonclient: write daemon owner: %w", err)
			}
			return nil
		}
		if stopErr := stopRunningDaemon(port); stopErr != nil {
			return fmt.Errorf("daemonclient: restart daemon: %w", stopErr)
		}
	}

	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("daemonclient: create run dir: %w", err)
	}

	logPath := filepath.Join(runDir, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logFile = io.Discard.(*os.File) // best-effort; fall back to /dev/null below
		logFile, _ = os.Open(os.DevNull)
	}

	if workspaceDir == "" {
		workspaceDir = defaultWorkspaceDir()
	}

	dataDir, err := DefaultDataDir()
	if err != nil {
		return fmt.Errorf("daemonclient: data dir: %w", err)
	}

	args := []string{
		"--port", strconv.Itoa(port),
		"--data-dir", dataDir,
		"--workspace-dir", workspaceDir,
	}
	if tokenForDaemon != "" {
		args = append(args, "--token", tokenForDaemon)
	}
	cmd := exec.Command(daemonBin, args...)
	// Detach from the calling process: new session, no controlling terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daemonclient: start daemon: %w", err)
	}

	_ = os.WriteFile(pidFilePath(runDir, port), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	_ = os.WriteFile(filepath.Join(runDir, "daemon.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	if err := writeDaemonOwner(runDir, port, worktreeRoot); err != nil {
		return fmt.Errorf("daemonclient: write daemon owner: %w", err)
	}

	// Detach from our process table so we don't wait for it.
	_ = cmd.Process.Release()

	if err := pollHealthz(port, 5*time.Second); err != nil {
		return err
	}
	if tokenForDaemon == "" {
		if err := pollDaemonToken(5 * time.Second); err != nil {
			return err
		}
	}
	return nil
}

func pollDaemonToken(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	interval := 50 * time.Millisecond
	for time.Now().Before(deadline) {
		if _, err := ReadDaemonToken(); err == nil {
			return nil
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("daemonclient: token file not available within %s", timeout)
}

// resolveDaemonBin finds the nexus-daemon binary. It first looks next to the
// current executable (co-installed), then falls back to $PATH.
func resolveDaemonBin() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "nexus-daemon")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	// Fall back to PATH.
	path, err := exec.LookPath("nexus-daemon")
	if err != nil {
		return "", fmt.Errorf("nexus-daemon not found next to %s or in $PATH", exe)
	}
	return path, nil
}

// pollHealthz polls /healthz until it returns 200 or the deadline passes.
func pollHealthz(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	interval := 100 * time.Millisecond
	client := &http.Client{Timeout: 400 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/healthz", port))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(interval)
		if interval < 500*time.Millisecond {
			interval += 100 * time.Millisecond
		}
	}
	return fmt.Errorf("daemonclient: daemon did not become ready within %s", timeout)
}

func shouldRestartRunningDaemon(port int, daemonBin string) (bool, error) {
	pid, err := readRunningDaemonPID(port)
	if err != nil {
		return false, err
	}
	if pid <= 0 {
		return false, nil
	}

	commandLine, err := daemonProcessCommandLineFn(pid)
	if err == nil {
		binName := filepath.Base(strings.TrimSpace(daemonBin))
		if binName != "" && !strings.Contains(commandLine, binName) {
			return true, nil
		}
	}

	binInfo, err := os.Stat(daemonBin)
	if err != nil {
		return false, nil
	}
	startedAt, err := daemonProcessStartedAtFn(pid)
	if err != nil {
		return true, nil
	}
	return binInfo.ModTime().After(startedAt.Add(time.Second)), nil
}

// Stop terminates the daemon that owns port.
//
// Resolution order for the target PID:
//  1. PID file (daemon-PORT.pid / daemon.pid) — preferred because it is
//     stable across port changes; the live process is verified before use.
//  2. lsof fallback — used when the PID file is absent or stale so that a
//     running daemon is never left orphaned.
//
// The function is a no-op when nothing is listening on port.
func Stop(port int) error {
	if !IsRunning(port) {
		return nil
	}

	// 1. Try the PID file first.
	if pid, err := readRunningDaemonPID(port); err == nil && pid > 0 {
		if proc, err := os.FindProcess(pid); err == nil {
			// Confirm the process is actually alive before trusting the file.
			if proc.Signal(syscall.Signal(0)) == nil {
				return killAndWait(proc, pid, port)
			}
		}
	}

	// 2. PID file absent / stale — resolve via lsof.
	pid, err := pidForPort(port)
	if err != nil || pid <= 0 {
		return fmt.Errorf("daemon on :%d is running but PID cannot be determined: %w", port, err)
	}
	proc, _ := os.FindProcess(pid)
	return killAndWait(proc, pid, port)
}

// killAndWait sends SIGTERM to proc, waits up to 5 s, then SIGKILL.
func killAndWait(proc *os.Process, pid, port int) error {
	_ = proc.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !IsRunning(port) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = proc.Signal(syscall.SIGKILL)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !IsRunning(port) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("daemon (pid %d) on :%d did not stop", pid, port)
}

// pidForPort returns the PID of the process listening on port using lsof.
func pidForPort(port int) (int, error) {
	out, err := exec.Command("lsof", "-i", fmt.Sprintf(":%d", port), "-sTCP:LISTEN", "-t").Output()
	if err != nil {
		return 0, fmt.Errorf("lsof: %w", err)
	}
	// lsof may return multiple PIDs (one per line); take the first.
	first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return strconv.Atoi(strings.TrimSpace(first))
}

func stopRunningDaemon(port int) error {
	pid, err := readRunningDaemonPID(port)
	if err != nil {
		return err
	}
	if pid <= 0 {
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && err != os.ErrProcessDone {
		return err
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !IsRunning(port) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = proc.Signal(syscall.SIGKILL)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !IsRunning(port) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("existing daemon (pid %d) did not exit", pid)
}

func readRunningDaemonPID(port int) (int, error) {
	runDir, err := RunDir()
	if err != nil {
		return 0, err
	}
	pidData, err := os.ReadFile(pidFilePath(runDir, port))
	if err == nil {
		return strconv.Atoi(strings.TrimSpace(string(pidData)))
	}
	if !os.IsNotExist(err) {
		return 0, err
	}
	pidData, err = os.ReadFile(filepath.Join(runDir, "daemon.pid"))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(pidData)))
}

func pidFilePath(runDir string, port int) string {
	return filepath.Join(runDir, fmt.Sprintf("daemon-%d.pid", port))
}

func daemonProcessCommandLine(pid int) (string, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DaemonProcessStartedAt returns the time the process with the given PID started.
func DaemonProcessStartedAt(pid int) (time.Time, error) {
	return daemonProcessStartedAt(pid)
}

func daemonProcessStartedAt(pid int) (time.Time, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return time.Time{}, err
	}
	startedAtRaw := strings.TrimSpace(string(out))
	if startedAtRaw == "" {
		return time.Time{}, fmt.Errorf("empty process start time")
	}
	startedAt, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", startedAtRaw, time.Local)
	if err != nil {
		startedAt, err = time.ParseInLocation("Mon Jan _2 15:04:05 2006", startedAtRaw, time.Local)
		if err != nil {
			return time.Time{}, err
		}
	}
	return startedAt, nil
}
