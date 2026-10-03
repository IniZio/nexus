//go:build linux

package supervisor

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const (
	sandboxSliceName          = "nexus-sandboxes.slice"
	sandboxSliceMemMaxPercent = 75
	sandboxSliceMemHighPct    = 70
)

// ensureSandboxSlice sets the slice memory caps once per process. Replaced in tests.
var ensureSandboxSlice = defaultEnsureSandboxSlice

// readMemTotalBytes returns host MemTotal in bytes. Replaced in tests.
var readMemTotalBytes = defaultReadMemTotalBytes

var sandboxSliceOnce sync.Once

func defaultReadMemTotalBytes() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			break
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
}

// sandboxSliceCaps returns MemoryMax and MemoryHigh as percentages of total.
func sandboxSliceCaps(total int64) (max, high int64) {
	return total / 100 * sandboxSliceMemMaxPercent, total / 100 * sandboxSliceMemHighPct
}

func defaultEnsureSandboxSlice() error {
	total, err := readMemTotalBytes()
	if err != nil {
		return err
	}
	max, high := sandboxSliceCaps(total)
	out, err := exec.Command("systemctl", "--user", "set-property", "--runtime", sandboxSliceName,
		fmt.Sprintf("MemoryMax=%d", max), fmt.Sprintf("MemoryHigh=%d", high)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

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
		"--slice=" + sandboxSliceName,
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
	sandboxSliceOnce.Do(func() {
		if err := ensureSandboxSlice(); err != nil {
			slog.Warn("supervisor.slice_cap_failed", "slice", sandboxSliceName, "err", err)
		}
	})
	_ = exec.Command("systemctl", "--user", "reset-failed", unit+".scope").Run()
	return execSystemdRun(buildSystemdScopeArgs(unit, exe, args), logFile)
}
