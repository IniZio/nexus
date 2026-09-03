package main

// Hot-swap (self-exec) support for the in-guest PID-1 agent.
//
// Re-listen after exec
// ─────────────────────
// syscall.Exec replaces the current process image (PID is preserved).
// The vsock listeners are created by mdlayher/vsock via SOCK_CLOEXEC, so
// the kernel closes them automatically during exec — the port is freed
// before the new process image starts.  The new image calls vsock.Listen()
// on the same ports immediately.  No EADDRINUSE is possible: two processes
// cannot simultaneously hold the same port because exec atomically replaces
// the image.  The brief rebind window (exec→new Listen) is acceptable —
// the host already expects a connection reset during swap (documented in
// the RestartAgent proto: "a connection reset means the swap was initiated")
// and polls AgentInfo until the new agent answers.
//
// Cold-boot init skipping
// ─────────────────────────
// The new process must skip init steps that only run once (filesystem
// mounts, sshd, workspace disk mounts, boot tasks) because those services
// are already running.  NEXUS3_HOT_SWAP=1 is set in the exec env to signal
// this.  The new process reads it at startup and skips those steps.
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
// install rename (exec failure) we attempt to rename .prev back to the
// install path so the agent stays functional.

import (
	"fmt"
	"os"
	"syscall"
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
	// chmodExec sets path to 0755 (world-readable, owner-executable).
	// Called after installing the staged binary so syscall.Exec succeeds.
	chmodExec func(path string) error
	// execSelf replaces the current process image.
	execSelf func(argv []string, env []string) error
}

var defaultSwapFns = swapFns{
	statSize:     realStatSize,
	fsyncPath:    realFsyncPath,
	renameAtomic: os.Rename,
	chmodExec:    realChmodExec,
	execSelf:     realExecSelf,
}

func realChmodExec(path string) error {
	return os.Chmod(path, 0755)
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
//  6. Build env with NEXUS3_HOT_SWAP=1 so the new process skips cold-boot init.
//  7. syscall.Exec → does not return on success.
//
// On any error before Exec, returns the error (the gRPC handler returns it
// as a gRPC status error and the connection survives).
// On error after the install rename (step 7), the old binary is restored
// from .prev so the agent remains functional.
//
// The vsock listeners (ctrl and data) need not be inherited: they are created
// with SOCK_CLOEXEC by mdlayher/vsock, so the kernel closes them during exec,
// freeing the ports for the new process to rebind via vsock.Listen.
// ─────────────────────────────────────────────────────────────────────────────
func performSwap(
	fns swapFns,
	stagedPath string,
	expectedBytes int64,
	installPath string,
	backupPath string,
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

	// ── Step 4.5: ensure the installed binary is executable ───────────────
	// The Copy RPC writes the staged file with 0644 (no exec bit).  Without
	// this chmod syscall.Exec fails with EACCES even though the file is in
	// place.  Fail-closed: if chmod fails, rollback and return the error.
	if err := fns.chmodExec(installPath); err != nil {
		_ = fns.renameAtomic(backupPath, installPath) // rollback
		return fmt.Errorf("swap: chmod install binary: %w", err)
	}

	// ── Step 5: fsync install directory (directory entry durability) ──────
	if err := fns.fsyncPath(installDir); err != nil {
		// Non-fatal for correctness (rename is atomic); log-worthy but proceed.
		_ = err // best-effort
	}

	// ── Step 6: build env ─────────────────────────────────────────────────
	// NEXUS3_HOT_SWAP=1 signals the new process image to skip cold-boot init
	// (filesystem mounts, sshd, workspace disk mounts, boot tasks) that
	// already ran under the old image.  The vsock listeners are NOT passed as
	// fds: mdlayher/vsock creates them with SOCK_CLOEXEC, so exec closes them
	// automatically, freeing the ports for the new image to rebind.
	env := os.Environ()
	env = append(env, "NEXUS3_HOT_SWAP=1")

	// ── Step 7: exec — does not return on success ─────────────────────────
	// argv[0] = installPath (the canonical agent path, now updated on disk).
	// argv[1:] = original kernel cmdline args (workspace-mount, mem-ceiling, …).
	argv := append([]string{installPath}, os.Args[1:]...)
	if err := fns.execSelf(argv, env); err != nil {
		// exec failed; the old image is still running.  Attempt rollback so
		// the binary on disk matches what is running.
		_ = fns.renameAtomic(backupPath, installPath)
		return fmt.Errorf("swap: exec: %w", err)
	}
	// Unreachable: execSelf either replaces the process image or returns an error.
	return nil
}

