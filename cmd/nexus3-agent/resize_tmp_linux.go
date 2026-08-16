//go:build linux

package main

// /tmp tmpfs auto-resizer for the guest auto-resize subsystem (AR-GA-AC6,
// D-DC-22). This is probably the single most load-bearing feature in the
// slice: buildkit uses /tmp as scratch, and a tmpfs mounted at boot stays
// capped at its boot size unless remounted — so growing the VM from 4 GiB to
// 8 GiB delivers zero extra scratch space with RAM sitting idle. Without
// this, memory auto-resize silently fails to fix the nested-build OOM wall.
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
