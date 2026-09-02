//go:build linux

package main

// /tmp tmpfs auto-resizer for the guest auto-resize subsystem (AR-GA-AC6,
// D-DC-22).
//
// CORRECTION (D-DC-32). This comment used to open by claiming that "buildkit
// uses /tmp as scratch", and called the resizer the most load-bearing feature
// in the slice on that basis. THAT IS FALSE FOR NEXUS3, and it is why the
// sizing policy below protects less than it appears to.
//
// The claim came in verbatim from OLD-nexus as HB-P10, whose provenance line
// reads "confirmed-live (OLD source read)" — it was verified against OLD
// source and never re-checked here. In nexus3, buildkitd's --root is
// inGuestBuildkitState = "/var/lib/buildkit" (internal/core/agent/
// buildkit_linux.go:24, passed at :108). With a cache disk attached that is a
// persistent ext4 mount; with none, buildkit mounts its OWN 4 GiB tmpfs
// there (:65). Neither is /tmp. The only /tmp in the whole build path is
// bkLogPath, a log file (:101). The export path deliberately prefers the
// cache disk and falls back to the ROOTFS /tmp, explicitly refusing to fall
// back to a RAM tmpfs (:193-210) — a previous 4 GiB RAM-backed scratch there
// caused hollow, zero-length exports on small builders.
//
// So the cap, the floor and this goroutine are not defending nested builds.
// What /tmp actually holds is ordinary temp files plus whatever an in-guest
// coding agent puts there — and on 2026-09-02 an agent filled it with four
// git worktrees and a pnpm store (1.1 GiB of a 1.4 GiB tmpfs) and hard-blocked
// while /workspace had 33 GiB free.
//
// TWO PROPERTIES MAKE THAT WORSE THAN A FULL DISK, and neither is obvious
// from the policy below:
//
//   - tmpfs pages are never advertised as free, so virtio-balloon
//     FreePageReporting can never reclaim them. Bytes written here leave the
//     governor's budget permanently. The only backstop is ZRAM, itself RAM.
//   - The grow-only rule RATCHETS. When the balloon lowers MemTotal, the cap
//     does not come down — resizeTmpfsOnce returns nil whenever target is
//     within hysteresis of the current cap, and there is no shrink path. A
//     guest observed at cap 1393 MiB against live MemTotal 2276 MiB was at
//     61% of the guest, not the intended 50%, and the fraction rises every
//     time the governor reclaims.
//
// D-DC-32 supersedes D-DC-22: /tmp moves to a disk-backed scratch device for
// worktree/coding-agent sandboxes. This file is deliberately NOT deleted —
// resizeTmpfsOnce already no-ops when /tmp is not a tmpfs (isTmpfsMounted
// below, covered by TestResizeTmpfsOnce_NonTmpfsIsNoOp), so the policy goes
// DORMANT rather than away, and a sandbox with no scratch disk behaves
// exactly as it does today.
//
// Policy (from motive.md §Axis-1 item 6 and the ticket):
//   - Size /tmp at 50% of current live MemTotal (not the boot ceiling).
//   - Hard cap: 2 GiB (tmpfsAbsoluteCapBytes).
//   - Grow-only: remount only when the target exceeds the current cap by ≥ 64 MiB.
//   - Poll interval: 10 s.
//
// Sizing against live MemTotal (not the ceiling) means /tmp grows
// incrementally with the VM as the governor admits more memory rather than
// jumping to 50% of the 8 GiB ceiling at boot (which would pre-reserve
// 4 GiB of RAM-backed tmpfs and worsen OOM pressure). The 64 MiB hysteresis
// avoids churn when consecutive measurements are close.
//
// TBD-DC-9 (boot ceiling seam) settlement: nexus3 uses the kernel cmdline
// (option B — same seam as --workspace-mount=). The host (AR-DRV / AR-CLI)
// must emit `--mem-ceiling=<memMaxBytes>` in the kernel cmdline; the agent
// parses it in main.go and passes it to startResizeServices. The /tmp resizer
// itself does NOT use the ceiling — it always sizes against live MemTotal —
// but the ceiling is available for the AR-DRV governor. See main.go for the
// --mem-ceiling= parser and the report for the host-side contract.
//
// Ported from OLD packages/nexus/cmd/nexus-guest-agent/tmp_resize.go.

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// tmpfsMemFractionNum / Den: 50% of live MemTotal.
	tmpfsMemFractionNum uint64 = 50
	tmpfsMemFractionDen uint64 = 100

	// tmpfsAbsoluteCapBytes: hard upper bound on /tmp regardless of MemTotal.
	// A RAM-backed tmpfs tracking the ceiling would consume 4 GiB+ of the RAM
	// the governor is protecting; cap it here.
	tmpfsAbsoluteCapBytes uint64 = 2 << 30 // 2 GiB

	// tmpfsAbsoluteFloorBytes: minimum /tmp size regardless of MemTotal.
	// The base-image disk-backed /tmp was ≈5959 MiB; the 50%-of-MemTotal formula
	// on a 512 MiB sandbox would yield only ≈242 MiB — a 24× scratch regression.
	// The floor prevents this starvation on small sandboxes.
	// IMPORTANT: tmpfs is sized, not preallocated. A 1 GiB floor on a 512 MiB
	// guest costs nothing until bytes are actually written into /tmp. Do NOT
	// remove this floor to "fix" apparent over-sizing on small guests.
	tmpfsAbsoluteFloorBytes uint64 = 1 << 30 // 1 GiB

	// tmpResizeGrowMarginBytes: minimum delta needed to trigger a remount.
	// Avoids churn when the ceiling and current cap are already close.
	tmpResizeGrowMarginBytes uint64 = 64 << 20 // 64 MiB

	// tmpResizeInterval: poll frequency for the resizer goroutine.
	tmpResizeInterval = 10 * time.Second
)

// TMPFS_MAGIC is the Linux filesystem type magic number for tmpfs.
// unix.TMPFS_MAGIC is available on Linux kernels; declare it explicitly
// so tests can compare against the constant without a live statfs call.
const tmpfsMagic = int64(0x01021994)

// Injectable seams — real implementations by default; replaced in unit tests.
var (
	tmpMeminfoPath = "/proc/meminfo" // read via readMeminfoKB (resize_actuate_linux.go)
	tmpStatfsFunc  = func(path string, st *unix.Statfs_t) error { return unix.Statfs(path, st) }
	tmpMountFunc   = unix.Mount
	tmpResizePath  = "/tmp"
)

// startTmpfsResizer spawns a panic-guarded goroutine that runs resizeTmpfsOnce
// immediately, then repeats on tmpResizeInterval until ctx is cancelled.
// Because the 2 GiB cap is usually reached quickly, subsequent ticks are cheap
// no-ops. The loop also handles the case where MemTotal is not yet stable
// immediately at boot.
//
// Ported from OLD packages/nexus/cmd/nexus-guest-agent/tmp_resize.go:startTmpfsResizer.
func startTmpfsResizer(ctx context.Context, con *os.File) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				consoleLog(con, "nexus3-agent: tmp-resize: panic recovered: %v\n", r)
			}
		}()

		if err := resizeTmpfsOnce(con); err != nil {
			consoleLog(con, "nexus3-agent: tmp-resize: %v\n", err)
		}

		ticker := time.NewTicker(tmpResizeInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := resizeTmpfsOnce(con); err != nil {
					consoleLog(con, "nexus3-agent: tmp-resize: %v\n", err)
				}
			}
		}
	}()
}

// resizeTmpfsOnce checks whether /tmp needs to grow and issues a remount if so.
// Grow-only: skips the remount when within tmpResizeGrowMarginBytes of the
// current cap, and when /tmp is not a tmpfs (e.g. a real disk mount).
func resizeTmpfsOnce(con *os.File) error {
	if !isTmpfsMounted() {
		return nil // /tmp is not a tmpfs; nothing to do
	}
	target := computeTmpTargetBytes()
	if target == 0 {
		return nil // MemTotal unavailable; skip this tick
	}
	cur, err := currentTmpCapBytes()
	if err != nil {
		cur = 0 // treat as unknown; proceed
	}
	if target <= cur+tmpResizeGrowMarginBytes {
		return nil // within hysteresis band; skip remount
	}
	opts := fmt.Sprintf("mode=1777,size=%d", target)
	if err := tmpMountFunc("tmpfs", tmpResizePath, "tmpfs", unix.MS_REMOUNT, opts); err != nil {
		return fmt.Errorf("remount /tmp size=%d: %w", target, err)
	}
	consoleLog(con, "nexus3-agent: tmp-resize: /tmp grown %d MiB → %d MiB\n",
		cur>>20, target>>20)
	return nil
}

// computeTmpSizeBytes is the pure sizing formula: given totalKB (from
// /proc/meminfo MemTotal, in kibibytes), returns the desired /tmp size in bytes.
// Formula: max(tmpfsAbsoluteFloorBytes, min(50% of totalKB*1024, tmpfsAbsoluteCapBytes)),
// rounded down to a whole MiB.
// Returns 0 when totalKB is 0 (MemTotal unavailable — caller skips the tick).
func computeTmpSizeBytes(totalKB uint64) uint64 {
	if totalKB == 0 {
		return 0
	}
	// Convert kB → bytes before applying the fraction to avoid integer truncation.
	totalBytes := totalKB * 1024
	target := totalBytes * tmpfsMemFractionNum / tmpfsMemFractionDen
	if target > tmpfsAbsoluteCapBytes {
		target = tmpfsAbsoluteCapBytes
	}
	if target < tmpfsAbsoluteFloorBytes {
		target = tmpfsAbsoluteFloorBytes
	}
	const mib = 1 << 20
	return (target / mib) * mib // round down to whole MiB
}

// computeTmpTargetBytes reads live MemTotal and delegates to computeTmpSizeBytes.
// Returns 0 when MemTotal is unavailable.
func computeTmpTargetBytes() uint64 {
	_, total, err := readMeminfoKB(tmpMeminfoPath)
	if err != nil || total == 0 {
		return 0
	}
	return computeTmpSizeBytes(total)
}

// currentTmpCapBytes returns the current /tmp tmpfs size limit by reading the
// statfs block count and block size. Returns (0, error) on failure.
func currentTmpCapBytes() (uint64, error) {
	var st unix.Statfs_t
	if err := tmpStatfsFunc(tmpResizePath, &st); err != nil {
		return 0, err
	}
	return st.Blocks * uint64(st.Bsize), nil //nolint:gosec // Bsize positive
}

// isTmpfsMounted returns true when /tmp is currently a tmpfs filesystem.
func isTmpfsMounted() bool {
	var st unix.Statfs_t
	if err := tmpStatfsFunc(tmpResizePath, &st); err != nil {
		return false
	}
	return st.Type == tmpfsMagic
}
