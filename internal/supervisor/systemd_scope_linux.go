//go:build linux

package supervisor

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// systemdUserProbe reports whether the systemd user manager is reachable.
// Replaced in tests to control the spawn path without forking.
var systemdUserProbe = defaultSystemdUserProbe

func defaultSystemdUserProbe() bool {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return false
	}
	_, err := os.Stat(runtimeDir + "/systemd/private")
	return err == nil
}

// supervisorScopeUnit derives the systemd unit name for a sandbox supervisor.
// Characters not valid in a systemd unit name are replaced with '_'.
func supervisorScopeUnit(sandboxRef string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.', r == '@', r == ':':
			return r
		default:
			return '_'
		}
	}, sandboxRef)
	return "nexus-sb-" + safe
}

// execSystemdRunFunc starts systemd-run non-blocking. The caller reaps the
// returned cmd via cmd.Wait() in a goroutine. Replaced in tests.
type execSystemdRunFunc func(sdArgs []string, logFile *os.File) (*exec.Cmd, error)

var execSystemdRun execSystemdRunFunc = defaultExecSystemdRun

func defaultExecSystemdRun(sdArgs []string, logFile *os.File) (*exec.Cmd, error) {
	cmd := exec.Command("systemd-run", sdArgs...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// buildSystemdScopeArgs builds the argv for systemd-run to launch exe+args
// in a transient user scope unit named after unit.
// OOMScoreAdjust is not a valid scope property and is omitted.
func buildSystemdScopeArgs(unit, exe string, args []string) []string {
	sdArgs := []string{
		"--user",
		"--scope",
		"--collect",
		"--unit=" + unit,
		"--",
		exe,
	}
	return append(sdArgs, args...)
}

// spawnViaSystemdScope registers a transient systemd user scope unit and
// starts the supervisor inside it. systemd-run execs the target so
// cmd.Process.Pid is the supervisor's PID; the caller polls supervisor.pid.
func spawnViaSystemdScope(exe string, args []string, logFile *os.File, sandboxRef string) (*exec.Cmd, error) {
	unit := supervisorScopeUnit(sandboxRef)
	_ = exec.Command("systemctl", "--user", "reset-failed", unit+".scope").Run()
	return execSystemdRun(buildSystemdScopeArgs(unit, exe, args), logFile)
}
