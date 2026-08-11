package supervisor

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// PidAlive returns true if pid belongs to a running process on this host.
//
// It sends signal 0 to the process — a no-op that merely checks existence —
// and interprets the result:
//   - nil error → process exists and we can signal it.
//   - syscall.EPERM → process exists but belongs to a different UID.
//   - syscall.ESRCH → process does not exist.
//
// pid <= 0 always returns false.
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		// On Linux FindProcess never errors, but be safe.
		return false
	}
	sigErr := p.Signal(syscall.Signal(0))
	if sigErr == nil {
		return true
	}
	if sigErr == syscall.EPERM {
		// Process exists but is owned by another user.
		return true
	}
	// syscall.ESRCH: process does not exist.
	return false
}

// CleanupStaleFiles removes the supervisor.pid and supervisor.sock artifacts
// that were written to stateDir by a now-dead supervisor process. Errors on
// individual removals are silently ignored (best-effort).
func CleanupStaleFiles(stateDir string) {
	_ = os.Remove(PidfilePath(stateDir))
	_ = os.Remove(SockPath(stateDir))
}

// stateDirOf extracts the parent directory from sockPath. Returns "" if
// sockPath is empty.
func stateDirOf(sockPath string) string {
	if sockPath == "" {
		return ""
	}
	return filepath.Dir(sockPath)
}

// sockDialTimeout is the maximum time allowed to establish a connection to
// the supervisor's Unix-domain socket during liveness cross-check.
const sockDialTimeout = 500 * time.Millisecond

// sockConnectable returns true when a Unix-domain socket at sockPath accepts a
// connection within sockDialTimeout. The connection is closed immediately; no
// data is exchanged. An empty sockPath always returns false.
func sockConnectable(sockPath string) bool {
	if sockPath == "" {
		return false
	}
	conn, err := net.DialTimeout("unix", sockPath, sockDialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// CheckAndReconcile inspects the supervisor state recorded as (pid, sockPath)
// and reports whether a live supervisor is running.
//
//   - pid <= 0 → no supervisor recorded → returns (false, nil).
//   - pid > 0, PidAlive(pid), and sockPath empty → live (no socket to check) →
//     returns (true, nil).
//   - pid > 0, PidAlive(pid), and sockConnectable(sockPath) → live →
//     returns (true, nil).
//   - pid > 0, PidAlive(pid), sockPath non-empty, but socket NOT connectable →
//     PID was recycled by an unrelated process; treat as stale → cleans up and
//     returns (false, nil).
//   - pid > 0 and !PidAlive(pid) → stale supervisor → removes stale
//     supervisor.pid and supervisor.sock files (best-effort) and returns
//     (false, nil).
//
// The caller is responsible for clearing SupervisorPID and SupervisorSock on
// the store record when CheckAndReconcile returns (false, nil) and pid was > 0.
func CheckAndReconcile(pid int, sockPath string) (alive bool, _ error) {
	if pid <= 0 {
		return false, nil
	}
	if PidAlive(pid) {
		// Cross-check: if a socket path is recorded the PID must also own a
		// listening socket. A recycled PID passes the signal-0 test but won't
		// own the supervisor socket.
		if sockPath != "" && !sockConnectable(sockPath) {
			// PID alive but socket dead → stale (PID reuse).
			stateDir := stateDirOf(sockPath)
			if stateDir != "" {
				CleanupStaleFiles(stateDir)
			}
			return false, nil
		}
		return true, nil
	}
	// Stale supervisor: remove its artifacts so the next spawn has a clean slate.
	stateDir := stateDirOf(sockPath)
	if stateDir != "" {
		CleanupStaleFiles(stateDir)
	}
	return false, nil
}
