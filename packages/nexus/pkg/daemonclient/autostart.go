// Package daemonclient provides helpers for locating, checking liveness of,
// and auto-starting the nexus workspace daemon from the CLI.
package daemonclient

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
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

// TokenPath returns the path of the per-user daemon token file.
func TokenPath() (string, error) {
	d, err := RunDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "token"), nil
}

// LoadOrCreateToken reads the daemon token from TokenPath, generating and
// persisting a new random token if none exists yet.
func LoadOrCreateToken() (string, error) {
	path, err := TokenPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		tok := string(data)
		if len(tok) >= 16 {
			return tok, nil
		}
	}
	// Generate a new token.
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	tok := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create run dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
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
// If token is empty, LoadOrCreateToken() is used to obtain one.
// The workspaceDir is passed as --workspace-dir to the daemon.
func EnsureRunning(port int, token, workspaceDir string) error {
	if IsRunning(port) {
		return nil
	}

	if token == "" {
		var err error
		token, err = LoadOrCreateToken()
		if err != nil {
			return fmt.Errorf("daemonclient: load token: %w", err)
		}
	}

	daemonBin, err := resolveDaemonBin()
	if err != nil {
		return fmt.Errorf("daemonclient: cannot find nexus-daemon binary: %w", err)
	}

	runDir, err := RunDir()
	if err != nil {
		return fmt.Errorf("daemonclient: run dir: %w", err)
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
		home, _ := os.UserHomeDir()
		workspaceDir = filepath.Join(home, "nexus-workspaces")
	}

	cmd := exec.Command(daemonBin,
		"--port", strconv.Itoa(port),
		"--token", token,
		"--workspace-dir", workspaceDir,
	)
	// Detach from the calling process: new session, no controlling terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daemonclient: start daemon: %w", err)
	}

	pidPath := filepath.Join(runDir, "daemon.pid")
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)

	// Detach from our process table so we don't wait for it.
	_ = cmd.Process.Release()

	return pollHealthz(port, 5*time.Second)
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
