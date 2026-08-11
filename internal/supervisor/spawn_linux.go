//go:build linux

package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// SpawnConfig carries the parameters for spawning a detached supervisor.
// It maps 1:1 with the flags accepted by `nexus3 __supervisor`.
type SpawnConfig struct {
	// Config is the supervisor configuration forwarded verbatim to the
	// subprocess as command-line flags.
	Config

	// Exe is the absolute path to the nexus3 binary to re-exec.
	// Defaults to os.Executable() when empty.
	Exe string

	// LogPath is the file where the supervisor's stdout/stderr are redirected.
	// When empty a temp file is created. The log is retained for diagnostics.
	LogPath string

	// ReadyTimeout is the maximum time to wait for supervisor.pid to appear.
	// Defaults to 5 minutes when zero.
	ReadyTimeout time.Duration
}

// SpawnDetached forks and detaches a supervisor process for the given sandbox.
// It returns the PID of the supervisor once supervisor.pid appears (READY).
//
// The subprocess is launched with Setsid:true so it is in a new session and
// survives the calling process's exit. It logs to LogPath (or a temp file).
//
// The caller is responsible for stopping the supervisor via StopSupervisor
// when it is no longer needed.
func SpawnDetached(cfg SpawnConfig) (int, error) {
	exe := cfg.Exe
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return 0, fmt.Errorf("spawn supervisor: resolve executable: %w", err)
		}
	}

	readyTimeout := cfg.ReadyTimeout
	if readyTimeout == 0 {
		readyTimeout = 5 * time.Minute
	}

	// Build the argv for `nexus3 __supervisor <flags>`.
	args := []string{
		HiddenSubcommand,
		"--sandbox-ref", cfg.SandboxRef,
		"--store-root", cfg.StoreRoot,
		"--state-dir", cfg.StateDir,
		"--ch-bin", cfg.CHBin,
		"--socket-dir", cfg.SocketDir,
		"--kernel", cfg.KernelPath,
		"--disk", cfg.DiskPath,
	}
	if cfg.CredsFile != "" {
		args = append(args, "--creds-file", cfg.CredsFile)
	}

	// Set up log file for supervisor stdout/stderr.
	logPath := cfg.LogPath
	if logPath == "" {
		logPath = cfg.StateDir + "/supervisor.log"
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("spawn supervisor: open log file %s: %w", logPath, err)
	}

	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("spawn supervisor: exec: %w", err)
	}
	// Reap the detached child when it eventually exits so it does not become
	// a zombie. Stdout/Stderr are file redirections so Wait does not block.
	go func() { _ = cmd.Wait() }()
	_ = logFile.Close()

	spawnPid := cmd.Process.Pid
	pidfile := PidfilePath(cfg.StateDir)
	deadline := time.Now().Add(readyTimeout)

	for time.Now().Before(deadline) {
		// Check if supervisor exited before writing pidfile.
		if err := syscall.Kill(spawnPid, 0); err != nil {
			return 0, fmt.Errorf("spawn supervisor: process exited before writing pidfile (pid %d)", spawnPid)
		}
		data, err := os.ReadFile(pidfile)
		if err == nil && len(data) > 0 {
			// Pidfile written: supervisor is ready.
			return spawnPid, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return 0, fmt.Errorf("spawn supervisor: timed out waiting for %s (pid %d)", pidfile, spawnPid)
}
