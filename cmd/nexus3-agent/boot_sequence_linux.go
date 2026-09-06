package main

// boot_sequence_linux.go — extracted PID-1 cold-boot init sequence.
//
// The init sequence is extracted so the hot-swap gating can be tested
// without running main().  The key invariant: when hotSwap=true (the process
// image was replaced via syscall.Exec by a prior RestartAgent call), the
// cold-boot steps MUST be skipped to avoid:
//   - mountGuestFS: re-mounting devtmpfs/proc/sys/cgroupv2//tmp after they
//     are already mounted causes EBUSY (some mounts) or silently stacks a
//     second layer (tmpfs on /tmp).
//   - setupNetwork: a second SIOCSIFADDR on the same virtio-net interface
//     returns EEXIST.
//   - startSSHD: a second sshd on port 22 fails with EADDRINUSE.
//   - MountWorkspace: the workspace disk is already mounted read-write;
//     re-mounting it fails with EBUSY → consoleFatal → os.Exit(1) with
//     isPid1=true → kernel panic.
//   - runBootTasks: runs /etc/nexus3/startup a second time (double-dockerd, …).

import (
	"github.com/IniZio/nexus3/internal/core/agent"
)

// bootConfig holds the injectable step functions for the PID-1 boot sequence.
// Production code populates this with real functions in main(); tests inject
// recorders that capture which steps were called.
type bootConfig struct {
	// mountGuestFS mounts devtmpfs, proc, sys, cgroupv2, /tmp (tmpfs).
	mountGuestFS func()
	// initPid1Env applies /etc/environment to the agent process environment.
	initPid1Env func()
	// setupNetwork configures the virtio-net interface.
	setupNetwork func()
	// startSSHD starts the in-guest sshd.
	startSSHD func()
	// wipeScratchDisk wipes, reformats, and mounts the scratch disk at /tmp.
	// dev is the guest block device path (e.g. "/dev/vdc").
	// Called only when isPid1, !hotSwap, and a scratch disk is attached.
	wipeScratchDisk func(dev string) error
	// mountWorkspace mounts workspace and shadow disks from wsMounts.
	// Returns an error that the caller handles (consoleFatal in production).
	mountWorkspace func(mounts []agent.GuestMount) error
	// runBootTasks runs /etc/nexus3/startup and other image boot hooks.
	runBootTasks func()
}

// runColdBootInit executes the PID-1 init steps that must be skipped on a
// hot-swap re-exec.  When hotSwap is true every cold-boot step is skipped.
//
// recorder, if non-nil, is called with each step name as it is about to
// execute.  This is the test hook: passing a recorder lets tests assert which
// steps ran (or were correctly skipped).
//
// Returns an error only from mountWorkspace (which is fatal in production
// but checkable in unit tests).
func runColdBootInit(
	hotSwap bool,
	isPid1 bool,
	wsMounts []agent.GuestMount,
	scratchDev string,
	cfg bootConfig,
	recorder func(string),
) error {
	record := func(name string) {
		if recorder != nil {
			recorder(name)
		}
	}

	// ── PID-1-only cold-boot steps ────────────────────────────────────────
	if isPid1 && !hotSwap {
		record("mountGuestFS")
		cfg.mountGuestFS()
		if scratchDev != "" && cfg.wipeScratchDisk != nil {
			record("wipeScratchDisk")
			if err := cfg.wipeScratchDisk(scratchDev); err != nil {
				return err
			}
		}
		record("initPid1Env")
		cfg.initPid1Env()
		record("setupNetwork")
		cfg.setupNetwork()
		record("startSSHD")
		cfg.startSSHD()
	}

	// ── Workspace mount (runs regardless of isPid1, skipped on hot-swap) ──
	// This is the critical guard: MountWorkspace on an already-mounted disk
	// returns EBUSY → consoleFatal → os.Exit(1) → kernel panic (PID 1).
	if !hotSwap && len(wsMounts) > 0 {
		record("mountWorkspace")
		if err := cfg.mountWorkspace(wsMounts); err != nil {
			return err
		}
	}

	// ── Boot tasks (PID-1 only, skipped on hot-swap) ──────────────────────
	if isPid1 && !hotSwap {
		record("runBootTasks")
		cfg.runBootTasks()
	}

	return nil
}
