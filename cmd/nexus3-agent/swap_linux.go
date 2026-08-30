package main

// Hot-swap (self-exec) support for the in-guest PID-1 agent.
//
// FD inheritance across execve
// ─────────────────────────────
// syscall.Exec replaces the current process image (PID is preserved).
// File descriptors without FD_CLOEXEC are inherited by the new image.
// Go's net package sets SOCK_CLOEXEC on sockets it creates, so the
// vsock listeners would normally be closed by the kernel on exec.
//
// To preserve them we:
//  1. Extract the underlying socket fd via SyscallConn().Control().
//  2. dup(2) the fd to a fresh, stable fd number.
//  3. Clear FD_CLOEXEC on the dup so it survives exec.
//  4. Pass the stable fd numbers as NEXUS3_CTRL_FD and NEXUS3_DATA_FD
//     in the new process's environment.
//
// The new process image reads these env vars at startup and calls
// net.FileListener(os.NewFile(fd, …)) to reclaim the already-bound
// vsock ports — no EADDRINUSE, no kernel rebind window.
//
// The gRPC control-plane connections accepted by the old server all die
// when exec fires (accepted connections have their own fds but the
// grpc.Server is not restarted; those fds are not inherited).  The host
// sees a connection reset, treats it as "restart in progress", then polls
// Ping / AgentInfo until the new agent answers.
//
// Staged-binary filesystem placement
// ────────────────────────────────────
// The staged binary MUST live on the SAME filesystem as the install path
// (/sbin/nexus3-agent on the rootfs ext4 partition).  Using /tmp would
// cause os.Rename to cross the tmpfs→ext4 device boundary and fail with
// EXDEV.  The staging path is /sbin/.nexus3-agent.upgrade (same directory
// as the install path, guaranteed same device).
//
// Rollback
// ─────────
// Before overwriting the install path we rename it to
// /sbin/.nexus3-agent.prev as a backup.  On any error AFTER the
// install rename (dup failure, exec failure) we attempt to rename .prev
// back to the install path so the agent stays functional.

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// agentInstallPath is the canonical in-guest path of the agent binary.
const agentInstallPath = "/sbin/nexus3-agent"

// agentStagingPath is where the host-pushed replacement binary is written.
// Must be on the SAME filesystem as agentInstallPath (same directory) to
// avoid EXDEV on os.Rename.  The leading dot makes it non-executable by name
// and signals it is transient.
const agentStagingPath = "/sbin/.nexus3-agent.upgrade"

// agentBackupPath is where the old binary is saved before overwriting, for
// rollback on exec/dup failure.
const agentBackupPath = "/sbin/.nexus3-agent.prev"

// ─────────────────────────────────────────────────────────────────────────────
// swapFns — injectable seams for testing
// ─────────────────────────────────────────────────────────────────────────────

// swapFns holds the injectable seams used by performSwap.
// Production code uses defaultSwapFns; tests replace them.
type swapFns struct {
	// statSize returns the size of the file at path.
	statSize func(path string) (int64, error)
	// fsyncPath calls fsync on the file at path.
	// Called on the staged binary and on the install directory after rename
	// to flush both data and directory metadata to durable storage.
	fsyncPath func(path string) error
	// renameAtomic renames src to dst.
	renameAtomic func(src, dst string) error
	// dupListenerFd extracts and dups the underlying socket fd of l,
	// clears FD_CLOEXEC, and returns the stable fd number.
	dupListenerFd func(l net.Listener) (int, error)
	// execSelf replaces the current process image.
	execSelf func(argv []string, env []string) error
}

var defaultSwapFns = swapFns{
	statSize:      realStatSize,
	fsyncPath:     realFsyncPath,
	renameAtomic:  os.Rename,
	dupListenerFd: realDupListenerFd,
	execSelf:      realExecSelf,
}

func realStatSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func realFsyncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("fsync open %q: %w", path, err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync %q: %w", path, err)
	}
	return nil
}

// realDupListenerFd extracts the underlying socket fd from l via SyscallConn,
// dups it to a new fd, and clears FD_CLOEXEC so the fd survives execve.
func realDupListenerFd(l net.Listener) (int, error) {
	type rawConner interface {
		SyscallConn() (syscall.RawConn, error)
	}
	rc, ok := l.(rawConner)
	if !ok {
		return -1, fmt.Errorf("listener %T does not implement SyscallConn", l)
	}
	rawConn, err := rc.SyscallConn()
	if err != nil {
		return -1, fmt.Errorf("SyscallConn: %w", err)
	}
	var dupFd int
	var innerErr error
	if ctrlErr := rawConn.Control(func(fd uintptr) {
		dupFd, innerErr = unix.Dup(int(fd))
		if innerErr != nil {
			return
		}
		// Clear FD_CLOEXEC so the dup'd fd survives execve.
		flags, err := unix.FcntlInt(uintptr(dupFd), unix.F_GETFD, 0)
		if err != nil {
			innerErr = fmt.Errorf("F_GETFD: %w", err)
			return
		}
		flags &^= unix.FD_CLOEXEC
		if _, err := unix.FcntlInt(uintptr(dupFd), unix.F_SETFD, flags); err != nil {
			innerErr = fmt.Errorf("F_SETFD clear CLOEXEC: %w", err)
		}
	}); ctrlErr != nil {
		return -1, fmt.Errorf("Control: %w", ctrlErr)
	}
	if innerErr != nil {
		return -1, innerErr
	}
	return dupFd, nil
}

func realExecSelf(argv []string, env []string) error {
	return syscall.Exec(argv[0], argv, env)
}

// ─────────────────────────────────────────────────────────────────────────────
// performSwap is the core hot-swap operation.
// It is called from the RestartAgent gRPC handler (control.go) after
// pre-flight checks (active sessions, force flag) have already passed.
//
// Steps:
//  1. Verify staged binary size == expectedBytes (fail-closed).
//  2. Fsync staged binary + its directory (data durability before rename).
//  3. Backup old install binary → .prev (enables rollback).
//  4. Atomically rename staged binary over agentInstallPath.
//  5. Fsync install directory (directory entry durability).
//  6. Dup both vsock listener fds; clear FD_CLOEXEC.
//  7. Build env with NEXUS3_CTRL_FD and NEXUS3_DATA_FD set.
//  8. syscall.Exec → does not return on success.
//
// On any error before Exec, returns the error (the gRPC handler returns it
// as a gRPC status error and the connection survives).
// On error after the install rename (steps 6+), the old binary is restored
// from .prev so the agent remains functional.
// ─────────────────────────────────────────────────────────────────────────────
func performSwap(
	fns swapFns,
	stagedPath string,
	expectedBytes int64,
	installPath string,
	backupPath string,
	ctrlLis, dataLis net.Listener,
) error {
	// ── Step 1: size guard ────────────────────────────────────────────────
	size, err := fns.statSize(stagedPath)
	if err != nil {
		return fmt.Errorf("swap: stat staged binary %q: %w", stagedPath, err)
	}
	if size != expectedBytes {
		return fmt.Errorf("swap: size mismatch for %q: got %d bytes, expected %d",
			stagedPath, size, expectedBytes)
	}

	// ── Step 2: fsync staged binary + parent directory ────────────────────
	// Ensures data is on disk before the rename makes it reachable as the
	// agent binary.  Parent dir fsync flushes the new directory entry.
	if err := fns.fsyncPath(stagedPath); err != nil {
		return fmt.Errorf("swap: fsync staged binary: %w", err)
	}
	installDir := "/sbin" // same dir for both staging and install
	if err := fns.fsyncPath(installDir); err != nil {
		return fmt.Errorf("swap: fsync install dir (pre-rename): %w", err)
	}

	// ── Step 3: backup old binary for rollback ────────────────────────────
	// Best-effort: ignore ENOENT (first upgrade, no existing binary).
	_ = fns.renameAtomic(installPath, backupPath)

	// ── Step 4: atomic install ────────────────────────────────────────────
	if err := fns.renameAtomic(stagedPath, installPath); err != nil {
		// Restore backup so the agent keeps running.
		_ = fns.renameAtomic(backupPath, installPath)
		return fmt.Errorf("swap: rename staged→install: %w", err)
	}

	// ── Step 5: fsync install directory (directory entry durability) ──────
	if err := fns.fsyncPath(installDir); err != nil {
		// Non-fatal for correctness (rename is atomic); log-worthy but proceed.
		_ = err // best-effort
	}

	// ── Step 6: dup listener fds ──────────────────────────────────────────
	// Fail-closed: if we can't pass the fds we must not exec (the new image
	// would try vsock.Listen on already-bound ports → EADDRINUSE).
	ctrlFd, err := fns.dupListenerFd(ctrlLis)
	if err != nil {
		_ = fns.renameAtomic(backupPath, installPath) // rollback
		return fmt.Errorf("swap: dup ctrl listener fd: %w", err)
	}
	dataFd, err := fns.dupListenerFd(dataLis)
	if err != nil {
		unix.Close(ctrlFd)                            // clean up the ctrl dup
		_ = fns.renameAtomic(backupPath, installPath) // rollback
		return fmt.Errorf("swap: dup data listener fd: %w", err)
	}

	// ── Step 7: build env ─────────────────────────────────────────────────
	env := os.Environ()
	env = append(env,
		"NEXUS3_CTRL_FD="+strconv.Itoa(ctrlFd),
		"NEXUS3_DATA_FD="+strconv.Itoa(dataFd),
	)

	// ── Step 8: exec — does not return on success ─────────────────────────
	// argv[0] = installPath (the canonical agent path, now updated on disk).
	// argv[1:] = original kernel cmdline args (workspace-mount, mem-ceiling, …).
	argv := append([]string{installPath}, os.Args[1:]...)
	if err := fns.execSelf(argv, env); err != nil {
		// exec failed; the old image is still running.  Close the dup'd fds
		// and attempt rollback so the binary on disk matches what is running.
		unix.Close(ctrlFd)
		unix.Close(dataFd)
		_ = fns.renameAtomic(backupPath, installPath)
		return fmt.Errorf("swap: exec: %w", err)
	}
	// Unreachable: execSelf either replaces the process image or returns an error.
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// inheritedListeners checks for NEXUS3_CTRL_FD / NEXUS3_DATA_FD env vars
// set by a prior hot-swap exec.  If found, it wraps those fds as net.Listeners
// and removes the vars from the environment so they are not leaked to child
// processes started by the new agent.
//
// Returns (nil, nil, nil) when no inherited fds are present.
// ─────────────────────────────────────────────────────────────────────────────
func inheritedListeners() (ctrlLis, dataLis net.Listener, err error) {
	ctrlFdStr := os.Getenv("NEXUS3_CTRL_FD")
	dataFdStr := os.Getenv("NEXUS3_DATA_FD")
	if ctrlFdStr == "" || dataFdStr == "" {
		return nil, nil, nil
	}

	ctrlFd, err := strconv.Atoi(ctrlFdStr)
	if err != nil {
		return nil, nil, fmt.Errorf("swap: bad NEXUS3_CTRL_FD %q: %w", ctrlFdStr, err)
	}
	dataFd, err := strconv.Atoi(dataFdStr)
	if err != nil {
		return nil, nil, fmt.Errorf("swap: bad NEXUS3_DATA_FD %q: %w", dataFdStr, err)
	}

	ctrlFile := os.NewFile(uintptr(ctrlFd), "vsock-ctrl")
	if ctrlFile == nil {
		return nil, nil, fmt.Errorf("swap: NEXUS3_CTRL_FD %d is invalid", ctrlFd)
	}
	ctrlLis, err = net.FileListener(ctrlFile)
	ctrlFile.Close() // FileListener dups internally; close our copy.
	if err != nil {
		return nil, nil, fmt.Errorf("swap: FileListener ctrl fd %d: %w", ctrlFd, err)
	}

	dataFile := os.NewFile(uintptr(dataFd), "vsock-data")
	if dataFile == nil {
		ctrlLis.Close()
		return nil, nil, fmt.Errorf("swap: NEXUS3_DATA_FD %d is invalid", dataFd)
	}
	dataLis, err = net.FileListener(dataFile)
	dataFile.Close()
	if err != nil {
		ctrlLis.Close()
		return nil, nil, fmt.Errorf("swap: FileListener data fd %d: %w", dataFd, err)
	}

	// Scrub the vars so exec'd children don't inherit them.
	os.Unsetenv("NEXUS3_CTRL_FD")
	os.Unsetenv("NEXUS3_DATA_FD")

	return ctrlLis, dataLis, nil
}
