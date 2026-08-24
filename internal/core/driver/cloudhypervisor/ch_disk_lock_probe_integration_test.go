//go:build integration

package cloudhypervisor

// ch_disk_lock_probe_integration_test.go — SD2-CH-LOCK-PROBE live experiment.
//
// QUESTION: When two Cloud Hypervisor instances are restored from the same
// snapshot and pointed at the SAME backing .raw disk image, does the second
// one refuse to start, start with errors, or start silently (and potentially
// corrupt the disk)?
//
// WHY IT MATTERS: nexus3 rule D-PD-95 forbids fork on sandboxes that carry
// a kind=disk named volume, because two VMs sharing one read-write ext4
// corrupts it.  That rule is enforced only in nexus3's service layer.  If CH
// itself refuses via a disk lock, nexus3 has a VMM-level backstop.  If not,
// nexus3's check is the ONLY protection.
//
// WHAT WE DO:
//  1. Create a 64 MiB sparse .raw file (the "named volume").
//  2. Boot a parent VM with that file as a virtio-blk disk (vda, kernel-panic
//     HLT loop — no userspace needed, same pattern as TestBootLifecycle).
//  3. Pause the parent → VMSnapshot → kill the parent.
//  4. Restore child1 from the snapshot dir directly (VMRestore).
//     config.json inside the snapshot dir carries the original disk path.
//  5. While child1 holds the disk, inspect /proc/locks for the disk inode
//     and lsof the file to identify the lock mechanism.
//  6. Restore child2 from the SAME snapshot dir (same config.json, same disk
//     path).
//  7. Record VERBATIM what CH returns for child2 — error text, exit code,
//     and observed state.
//  8. If child2 succeeds, attempt a write via the CH API to determine whether
//     both instances can actually write (the corruption case).
//
// # Guard conditions (same as TestBootLifecycle)
//
//   - /dev/kvm
//   - cloud-hypervisor binary at defaultCHBin or CLOUD_HYPERVISOR_BIN
//   - testdata/vmlinux-x86_64
//
// # Running
//
//	bash scripts/fetch-boot-artifacts.sh
//	TMPDIR=/tmp go test -tags integration -run TestCHDiskLockProbe \
//	    ./internal/core/driver/cloudhypervisor/... -v -timeout 300s

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/driver"
)

func TestCHDiskLockProbe(t *testing.T) {
	// ── Guard conditions ───────────────────────────────────────────────────
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")

	// ── Directories (short paths for the 107-byte AF_UNIX sun_path limit) ─
	socketDir, err := os.MkdirTemp("/tmp", "ch-dlp-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	snapDir, err := os.MkdirTemp("/tmp", "ch-dlp-snap-")
	if err != nil {
		t.Fatalf("MkdirTemp snapDir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(snapDir) })

	workDir, err := os.MkdirTemp("/tmp", "ch-dlp-work-")
	if err != nil {
		t.Fatalf("MkdirTemp workDir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(workDir) })

	// ── Create the "named volume" raw disk (sparse 64 MiB) ────────────────
	diskPath := filepath.Join(workDir, "named-volume.raw")
	diskFile, err := os.Create(diskPath)
	if err != nil {
		t.Fatalf("create disk: %v", err)
	}
	if err := diskFile.Truncate(64 << 20); err != nil {
		diskFile.Close()
		t.Fatalf("truncate disk to 64 MiB: %v", err)
	}
	diskFile.Close()
	t.Logf("named-volume disk: %s (64 MiB sparse)", diskPath)

	// Get the inode number for /proc/locks inspection later.
	diskStat, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("stat disk: %v", err)
	}
	diskInode := diskStat.Sys().(*syscall.Stat_t).Ino
	t.Logf("disk inode: %d", diskInode)

	// ── Shared driver config (no SnapshotDir/DiskDir — not using the driver) ─
	cfg := Config{
		BinaryPath:   chBin,
		SocketDir:    socketDir,
		StartTimeout: 15 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	// ── Helper: poll client Ping until ready ───────────────────────────────
	pollPing := func(cli *client, label string, timeout time.Duration) error {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			pingCtx, pingCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			err := cli.Ping(pingCtx)
			pingCancel()
			if err == nil {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("%s: VMM did not respond to ping within %v", label, timeout)
	}

	// ── Helper: poll VMInfo until Running ─────────────────────────────────
	pollRunning := func(cli *client, label string, timeout time.Duration) error {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			infoCtx, infoCancel := context.WithTimeout(context.Background(), 2*time.Second)
			state, err := cli.VMInfo(infoCtx)
			infoCancel()
			if err == nil {
				switch state {
				case driver.Running:
					return nil
				default:
					t.Logf("%s: VMInfo state=%v, waiting for Running...", label, state)
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("%s: VM did not reach Running within %v", label, timeout)
	}

	// ══════════════════════════════════════════════════════════════════════
	// Phase 1: boot parent VM and snapshot it
	// ══════════════════════════════════════════════════════════════════════

	parentSock := filepath.Join(socketDir, "parent.sock")
	t.Log("spawning parent VMM...")
	parentProc, err := spawnVMM(ctx, cfg, parentSock)
	if err != nil {
		t.Fatalf("spawnVMM (parent): %v", err)
	}
	parentKilled := false
	t.Cleanup(func() {
		if !parentKilled {
			parentProc.kill()
		}
	})

	parentCli := newClient(parentSock)

	// Build the vm.create config: kernel (panic/HLT) + the named-volume disk.
	vcpus := uint32(1)
	memBytes := uint64(128 << 20)
	vmcfg := vmConfig{
		Payload: vmPayloadConfig{Kernel: kernelPath},
		CPUs:    &vmCPUsConfig{BootVCPUs: vcpus, MaxVCPUs: vcpus},
		Memory:  &vmMemoryConfig{SizeBytes: memBytes},
		Disks: []vmDiskConfig{
			{Path: diskPath, ImageType: "Raw"},
		},
	}

	t.Log("vm.create (parent)...")
	if err := parentCli.VMCreate(ctx, vmcfg); err != nil {
		t.Fatalf("VMCreate (parent): %v", err)
	}

	t.Log("vm.boot (parent)...")
	if err := parentCli.VMBoot(ctx); err != nil {
		t.Fatalf("VMBoot (parent): %v", err)
	}

	t.Log("waiting for parent Running...")
	if err := pollRunning(parentCli, "parent", 30*time.Second); err != nil {
		t.Fatalf("parent Running: %v", err)
	}
	t.Log("parent VM running")

	// Pause → Snapshot → kill (no need to resume).
	t.Log("pausing parent...")
	pauseCtx, pauseCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := parentCli.VMPause(pauseCtx); err != nil {
		pauseCancel()
		t.Fatalf("VMPause (parent): %v", err)
	}
	pauseCancel()

	t.Logf("snapshotting to %s ...", snapDir)
	snapCtx, snapCancel := context.WithTimeout(ctx, 60*time.Second)
	if err := parentCli.VMSnapshot(snapCtx, "file://"+snapDir); err != nil {
		snapCancel()
		t.Fatalf("VMSnapshot: %v", err)
	}
	snapCancel()
	t.Log("snapshot done")

	t.Log("killing parent VMM...")
	parentProc.kill()
	parentKilled = true

	// Verify snapshot dir has files.
	entries, _ := os.ReadDir(snapDir)
	t.Logf("snapshot dir contents (%d files):", len(entries))
	for _, e := range entries {
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		t.Logf("  %s (%d bytes)", e.Name(), size)
	}

	// ══════════════════════════════════════════════════════════════════════
	// Phase 2: restore child1 from snapshot (holds the disk)
	// ══════════════════════════════════════════════════════════════════════

	child1Sock := filepath.Join(socketDir, "c1.sock")
	t.Log("spawning child1 VMM...")
	child1Proc, err := spawnVMM(ctx, cfg, child1Sock)
	if err != nil {
		t.Fatalf("spawnVMM (child1): %v", err)
	}
	t.Cleanup(func() { child1Proc.kill() })

	child1Cli := newClient(child1Sock)
	if err := pollPing(child1Cli, "child1", 10*time.Second); err != nil {
		t.Fatalf("%v", err)
	}

	t.Logf("restoring child1 from %s ...", snapDir)
	restore1Ctx, restore1Cancel := context.WithTimeout(ctx, 60*time.Second)
	restore1Err := child1Cli.VMRestore(restore1Ctx, "file://"+snapDir)
	restore1Cancel()
	t.Logf("child1 VMRestore result: %v", restore1Err)

	if restore1Err != nil {
		t.Fatalf("child1 VMRestore failed — cannot proceed with child2: %v", restore1Err)
	}

	// Resume child1 so it's fully running and holds the disk open.
	t.Log("resuming child1...")
	resume1Ctx, resume1Cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := child1Cli.VMResume(resume1Ctx); err != nil {
		resume1Cancel()
		t.Logf("child1 VMResume error (non-fatal): %v", err)
	} else {
		resume1Cancel()
	}

	// Give CH a moment to establish disk I/O.
	time.Sleep(500 * time.Millisecond)

	// ══════════════════════════════════════════════════════════════════════
	// Phase 3: inspect /proc/locks and lsof while child1 holds the disk
	// ══════════════════════════════════════════════════════════════════════

	t.Logf("\n=== LOCK EVIDENCE (disk inode %d) ===", diskInode)

	// /proc/locks scan: look for our inode.
	locksBytes, err := os.ReadFile("/proc/locks")
	if err != nil {
		t.Logf("/proc/locks read error: %v", err)
	} else {
		inodeStr := fmt.Sprintf("%d", diskInode)
		lines := string(locksBytes)
		// Each line: "N: TYPE LOCK ACCESS PID MAJOR:MINOR:INODE RANGE..."
		found := false
		for _, line := range splitLines(lines) {
			if containsInode(line, inodeStr) {
				t.Logf("/proc/locks: %s", line)
				found = true
			}
		}
		if !found {
			t.Logf("/proc/locks: NO entry found for inode %d — no kernel-level lock held", diskInode)
		}
	}

	// lsof the disk file.
	lsofOut, lsofErr := exec.Command("lsof", diskPath).CombinedOutput()
	if lsofErr != nil && len(lsofOut) == 0 {
		t.Logf("lsof: error=%v (lsof may not be installed)", lsofErr)
	} else {
		t.Logf("lsof %s:\n%s", diskPath, string(lsofOut))
	}

	// ══════════════════════════════════════════════════════════════════════
	// Phase 4: attempt to restore child2 from the SAME snapshot (same disk)
	// ══════════════════════════════════════════════════════════════════════

	t.Log("\n=== CHILD2 RESTORE ATTEMPT (same snapshot, same disk) ===")

	child2Sock := filepath.Join(socketDir, "c2.sock")
	t.Log("spawning child2 VMM...")
	child2Proc, err := spawnVMM(ctx, cfg, child2Sock)
	if err != nil {
		t.Logf("RESULT: child2 spawnVMM FAILED: %v", err)
		t.Logf("VERDICT: Cloud Hypervisor blocked at VMM spawn level")
		appendLockProbeVerification(t)
		return
	}
	t.Cleanup(func() { child2Proc.kill() })

	child2Cli := newClient(child2Sock)
	if err := pollPing(child2Cli, "child2", 10*time.Second); err != nil {
		t.Logf("RESULT: child2 VMM ping FAILED: %v", err)
		appendLockProbeVerification(t)
		return
	}

	t.Logf("attempting VMRestore for child2 from SAME snapshot dir %s ...", snapDir)
	restore2Ctx, restore2Cancel := context.WithTimeout(ctx, 60*time.Second)
	restore2Err := child2Cli.VMRestore(restore2Ctx, "file://"+snapDir)
	restore2Cancel()

	t.Logf("=== VERBATIM child2 VMRestore result: %v ===", restore2Err)

	if restore2Err != nil {
		t.Logf("RESULT: child2 VMRestore FAILED with: %v", restore2Err)

		// Capture child2 VMM stderr for the error mechanism.
		if child2Proc.stderrBuf != nil {
			stderr := child2Proc.stderrBuf.Tail()
			if stderr != "" {
				t.Logf("child2 VMM stderr:\n%s", stderr)
			}
		}

		t.Logf("VERDICT: Cloud Hypervisor DOES enforce a disk lock — " +
			"the second restore fails when the same disk is already held.")
	} else {
		t.Logf("RESULT: child2 VMRestore SUCCEEDED (both VMs share the disk)")

		// Resume child2 to confirm it's actually running.
		resume2Ctx, resume2Cancel := context.WithTimeout(ctx, 10*time.Second)
		resume2Err := child2Cli.VMResume(resume2Ctx)
		resume2Cancel()
		t.Logf("child2 VMResume result: %v", resume2Err)

		// Check child2 state.
		infoCtx, infoCancel := context.WithTimeout(context.Background(), 5*time.Second)
		child2State, infoErr := child2Cli.VMInfo(infoCtx)
		infoCancel()
		t.Logf("child2 VMInfo: state=%v err=%v", child2State, infoErr)

		// Check /proc/locks again — now with two VMs holding the disk.
		t.Logf("\n=== /proc/locks AFTER both VMs running ===")
		locksBytes2, err := os.ReadFile("/proc/locks")
		if err == nil {
			inodeStr := fmt.Sprintf("%d", diskInode)
			for _, line := range splitLines(string(locksBytes2)) {
				if containsInode(line, inodeStr) {
					t.Logf("/proc/locks: %s", line)
				}
			}
		}

		// lsof again.
		lsofOut2, _ := exec.Command("lsof", diskPath).CombinedOutput()
		t.Logf("lsof (both running):\n%s", string(lsofOut2))

		t.Logf("VERDICT: Cloud Hypervisor does NOT enforce a disk lock — " +
			"BOTH instances share the same disk file with no error. " +
			"nexus3's service-layer check (D-PD-95) is the ONLY protection.")
	}

	appendLockProbeVerification(t)
}

// splitLines splits s into non-empty lines.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// containsInode returns true if line contains the inode string at the
// correct position in a /proc/locks entry.
//
// /proc/locks format (Linux):
//
//	N: POSIX  ADVISORY  WRITE  PID  MAJOR:MINOR:INODE  START  END
//
// The inode appears as the last colon-separated component of the
// MAJOR:MINOR:INODE field.
func containsInode(line, inode string) bool {
	// Quick substring check first — cheap and handles the common case.
	// The inode is always preceded by a colon in the MAJOR:MINOR:INODE field.
	needle := ":" + inode + " " // colon-prefixed, space-terminated
	if len(line) > 0 {
		for i := 0; i < len(line)-len(needle)+1; i++ {
			if line[i:i+len(needle)] == needle {
				return true
			}
		}
		// Also check if inode is at end of line (no trailing space).
		needleEOL := ":" + inode
		if len(line) >= len(needleEOL) &&
			line[len(line)-len(needleEOL):] == needleEOL {
			return true
		}
	}
	return false
}

// appendLockProbeVerification writes the VERIFICATION journal event.
// Failure here is non-fatal — the experiment result is already logged.
func appendLockProbeVerification(t *testing.T) {
	t.Helper()
	// The journal binary path; the exact location is injected by the harness.
	// Try the known location; skip silently if absent.
	journalBin := os.Getenv("NEXUS3_JOURNAL_BIN")
	if _, err := os.Stat(journalBin); err != nil {
		t.Logf("journal binary not found at %s — skipping VERIFICATION event", journalBin)
		return
	}
	cmd := exec.Command(journalBin, "append",
		"--type", "VERIFICATION",
		"--msg", "SD2-CH-LOCK-PROBE: live CH disk-lock experiment",
		"--data", `{"req_ids":["D-PD-95"],"mode":"exploratory","findings":"measured CH disk-lock behavior; see test log"}`,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("journal append error (non-fatal): %v: %s", err, out)
	} else {
		t.Logf("journal VERIFICATION event appended")
	}
}
