//go:build linux

package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// systemdUserProbe reports whether the systemd user manager is reachable.
// Replaced in tests to control the spawn path without forking.
var systemdUserProbe = defaultSystemdUserProbe

// defaultSystemdUserProbe checks for $XDG_RUNTIME_DIR/systemd/private, which
// systemd creates when the user manager is running. The stat is cheap and
// avoids spawning a subprocess.
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
// SandboxRef is either a 32-char hex ID or a "project/name" handle.
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

// execSystemdRunFunc is the signature for the systemd-run executor.
// The seam is replaced in tests.
type execSystemdRunFunc func(sdArgs []string, logFile *os.File) error

// execSystemdRun is the package-level seam for running systemd-run.
// Tests replace it to capture the generated argv without forking.
var execSystemdRun execSystemdRunFunc = defaultExecSystemdRun

func defaultExecSystemdRun(sdArgs []string, logFile *os.File) error {
	cmd := exec.Command("systemd-run", sdArgs...)
	cmd.Stdin = nil
	// Scope units inherit stdio from the calling process; the supervisor
	// therefore writes to logFile just as in the direct-fork path.
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}

// buildSystemdScopeArgs returns the argv for systemd-run to launch exe+args
// in a transient user scope unit named after sandboxRef.
//
// OOMScoreAdjust=0 prevents the supervisor and its VM children from being
// preferred OOM victims. The controller unit sets OOMScoreAdjust=500, which
// all its cgroup descendants inherit; placing the supervisor in its own scope
// both removes that inherited bias and lets the value be set explicitly.
func buildSystemdScopeArgs(unit, exe string, args []string) []string {
	sdArgs := []string{
		"--user",
		"--scope",
		"--collect",
		"--unit=" + unit,
		"-p", "OOMScoreAdjust=0",
		"--",
		exe,
	}
	return append(sdArgs, args...)
}

// spawnViaSystemdScope registers a transient systemd user scope unit for the
// supervisor, then runs systemd-run synchronously. systemd-run exits once the
// scope is registered and the supervisor process is running in its own cgroup;
// the caller polls supervisor.pid for the ready signal.
//
// Only call this when cfg.Ephemeral is false and CacheDiskLeaseFiles is empty:
// file descriptors (watchdog pipe, lease FDs) cannot be forwarded through
// systemd-run.
func spawnViaSystemdScope(exe string, args []string, logFile *os.File, sandboxRef string) error {
	unit := supervisorScopeUnit(sandboxRef)

	// If a scope with this name is in a failed state (e.g. left over from a
	// previous crash), reset it so systemd-run can reuse the name.
	_ = exec.Command("systemctl", "--user", "reset-failed", unit+".scope").Run()

	sdArgs := buildSystemdScopeArgs(unit, exe, args)
	if err := execSystemdRun(sdArgs, logFile); err != nil {
		return fmt.Errorf("systemd-run --scope %s: %w", unit, err)
	}
	return nil
}
