// AR-DISK: disk grow axis for the nexus3 per-sandbox governor.
//
// This file implements the disk AxisEvaluator. It attaches to the Governor
// via RegisterAxis and must NOT edit memory.go, loop.go, or govern_test.go —
// that seam prevents parallel-slice collision on safety-critical code.
//
// Control law (constants verified against OLD disk_resize.go:26-45):
//   - grow trigger: DiskUsed/DiskTotal > 0.80
//   - step:         +16 GiB
//   - ceiling:      100 GiB (Bounds.DiskMaxBytes if lower)
//   - grow cooldown: 30 s
//   - boot delay:    15 s
//   - grow-only:     disk shrink is never performed
//
// # Sparse-image pool-check design
//
// nexus3 uses sparse ext4 backing files. A naïve host free-space check against
// the sparse file's apparent size (os.FileInfo.Size) always passes because that
// size equals the logical disk capacity, not the bytes actually written on the
// host filesystem. The backing file can grow silently until the host runs out
// of blocks, reintroducing the host-OOM wall this governor exists to prevent.
//
// Correct approach — to be implemented in [resize.DiskResizer.GrowDisk]:
//  1. Stat the backing file. Use syscall.Stat_t.Blocks (512-byte block count)
//     to obtain actual host consumption: actualBytes = stat.Blocks * 512.
//  2. Statfs the directory containing the backing file to obtain host free
//     space: hostFree = statfs.Bavail * statfs.Bsize.
//  3. Verify: (target - actualBytes) <= hostFree before truncating.
//
// Limits:
//   - TOCTOU: another process can fill the host between the check and the
//     ftruncate. Mitigate with O_SYNC or immediate fallocation, not fully
//     preventable at the filesystem level.
//   - Concurrent VMs on the same host partition share the same free-space
//     budget; the check is not atomic across them.
//   - stat.Blocks counts 512-byte filesystem allocation units, not the Bsize
//     blocks reported by statfs; both sides of the comparison must use
//     consistent units.
//
// The governor's job is the DECISION; pool enforcement is GrowDisk's job.
// This file documents the constraint so the driver implementation is correct.
package govern

import (
	"context"
	"log/slog"
	"time"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// Disk axis control-law constants.
// All values are verified against OLD-nexus disk_resize.go:26-45.
const (
	// diskGiB is 1 gibibyte in bytes (convenience multiplier).
	diskGiB = int64(1 << 30)

	// diskGrowThreshold: grow when DiskUsedBytes/DiskTotalBytes > 0.80.
	// Leaves 20% buffer so the governor reacts before the guest hits ENOSPC.
	// Source: OLD disk_resize.go:32.
	diskGrowThreshold = 0.80

	// diskGrowStep: fixed increment per grow event.
	// Source: OLD disk_resize.go:35 (diskGrowIncrement = 16 GiB).
	diskGrowStep = 16 * diskGiB

	// diskDefaultMax: hard ceiling when Bounds.DiskMaxBytes is zero.
	// Source: OLD disk_resize.go:40 (diskMaxBytes = 100 GiB).
	// This ceiling applies PER AXIS: each named-volume disk and the workspace
	// disk each independently cap at 100 GiB (or Bounds.DiskMaxBytes when set).
	diskDefaultMax = 100 * diskGiB

	// diskGrowCooldown: minimum interval between grow events.
	// Source: OLD disk_resize.go:44.
	diskGrowCooldown = 30 * time.Second

	// diskBootDelay: quiet period after VM start before first disk evaluation.
	// Source: OLD disk_resize.go:46 (diskResizeBootDelay = 15 s).
	diskBootDelay = 15 * time.Second
)

// DiskAxis is the disk grow axis of the nexus3 per-sandbox governor.
//
// It is grow-only (disk shrink is not possible at runtime). One DiskAxis
// manages exactly one disk identified by diskIndex (0-based index into
// ExtraDisks; the workspace disk is appended last by create.go:383).
//
// Construct with NewDiskAxis; the constructor registers the axis with the
// Governor automatically. All state is accessed only from the Governor's Run
// goroutine — no locking is required.
type DiskAxis struct {
	g         *Governor
	resizer   resize.DiskResizer
	diskIndex int

	// startTime is when the axis was constructed (g.clock.Now() at NewDiskAxis).
	// Used to enforce diskBootDelay.
	startTime time.Time

	// lastGrow is the time of the most recent successful GrowDisk call.
	// Zero value means no grow has occurred yet.
	lastGrow time.Time

	// growErrLogged suppresses repeated govern.disk.grow_failed log lines when
	// GrowDisk fails permanently (e.g. CH HTTP 400 on Direct:true disks). Set on
	// the first error; cleared on the next successful grow. Follows the same
	// latch pattern as Governor.pollErrLogged in loop.go — log once, stay silent
	// until the condition clears. Prevents ≈17k lines/day/sandbox log spam at
	// the default 5-second poll interval.
	growErrLogged bool
}

// NewDiskAxis constructs a DiskAxis and registers it with g.
//
// diskIndex is the 0-based index into ExtraDisks for the workspace disk that
// this axis manages. It must match the index passed to DiskResizer.GrowDisk.
//
// Must be called before Governor.Run. Panics if resizer is nil.
func NewDiskAxis(g *Governor, resizer resize.DiskResizer, diskIndex int) *DiskAxis {
	if resizer == nil {
		panic("govern.NewDiskAxis: resizer must not be nil")
	}
	a := &DiskAxis{
		g:         g,
		resizer:   resizer,
		diskIndex: diskIndex,
		startTime: g.clock.Now(),
	}
	g.RegisterAxis(a)
	return a
}

// Evaluate implements [AxisEvaluator]. It is called by the Governor's poll
// loop after every accepted telemetry sample.
//
// Disk-supported gate (Trap 1): DiskTotalBytes == 0 is indistinguishable from
// "statfs failed" without DiskSupported. A zero total reads as 0% used —
// perfectly healthy — and would silently suppress growth exactly when the disk
// is filling. This mirrors how the memory axis gates on MemPSISupported.
func (a *DiskAxis) Evaluate(ctx context.Context) {
	s := a.g.latest

	// Per-disk sample lookup. Two paths:
	//
	// (A) DiskStats populated (new guest agent, ≥2 disks): find the entry whose
	//     Index matches a.diskIndex. If DiskStats is non-empty but no entry
	//     matches, the host registered a disk index the guest didn't report —
	//     either the Resizable flag wasn't emitted (old host→new guest skew) or
	//     the index is wrong. Treat as "telemetry unavailable" (no grow) and log
	//     once to aid diagnosis. The log-once latch reuses growErrLogged so no
	//     new state is needed; it is cleared on the next successful grow.
	//
	// (B) DiskStats empty (old guest agent): fall back to the legacy single-disk
	//     fields. Only the workspace disk is reported this way; named-volume axes
	//     at other indices are silently idle (safe: no spurious grow).
	used := s.DiskUsedBytes
	total := s.DiskTotalBytes
	supported := s.DiskSupported
	if len(s.DiskStats) > 0 {
		found := false
		for _, ds := range s.DiskStats {
			if ds.Index == a.diskIndex {
				used = ds.UsedBytes
				total = ds.TotalBytes
				supported = ds.Supported
				found = true
				break
			}
		}
		if !found {
			// DiskStats non-empty but our index absent: guest didn't report this
			// disk (e.g. old guest agent without Resizable support, or index skew).
			// Treat as unsupported — suppress grow, log once.
			if !a.growErrLogged {
				a.growErrLogged = true
				slog.Warn("govern.disk.index_not_in_sample",
					"diskIndex", a.diskIndex,
					"diskStatsLen", len(s.DiskStats),
				)
			}
			return
		}
	}

	// Trap 1: never compute a usage ratio or take any action when the guest
	// reports that disk telemetry is unavailable.
	if !supported {
		return
	}

	// Boot delay: let the guest settle before the first disk evaluation.
	now := a.g.clock.Now()
	if now.Sub(a.startTime) < diskBootDelay {
		return
	}

	// Guard division-by-zero (DiskSupported==true but DiskTotalBytes==0 is
	// unexpected; treat as unsupported to avoid a spurious grow decision).
	if total == 0 {
		return
	}

	ratio := float64(used) / float64(total)
	if ratio <= diskGrowThreshold {
		// Below threshold — disk is healthy; grow-only so no shrink action.
		return
	}

	// Cooldown between grow events.
	if !a.lastGrow.IsZero() && now.Sub(a.lastGrow) < diskGrowCooldown {
		return
	}

	// Ceiling: min(diskDefaultMax, Bounds.DiskMaxBytes) when DiskMaxBytes > 0.
	ceiling := diskDefaultMax
	if a.g.bounds.DiskMaxBytes > 0 && a.g.bounds.DiskMaxBytes < ceiling {
		ceiling = a.g.bounds.DiskMaxBytes
	}

	current := int64(total)
	if current >= ceiling {
		slog.Warn("govern.disk.hard_max",
			"diskIndex", a.diskIndex,
			"currentBytes", current,
			"ceilingBytes", ceiling,
		)
		return
	}

	target := current + diskGrowStep
	if target > ceiling {
		target = ceiling
	}

	// GrowDisk is responsible for host-side pool verification (see package doc
	// for the sparse-aware pool-check design that the driver must implement).
	if err := a.resizer.GrowDisk(ctx, a.diskIndex, target); err != nil {
		// Rate-limit: log only on the first failure (e.g. CH HTTP 400 for
		// Direct:true disks that cannot be resized via the API). Subsequent
		// failures are silently suppressed until a grow succeeds. Follows the
		// same latch pattern as Governor.pollErrLogged in loop.go.
		if !a.growErrLogged {
			a.growErrLogged = true
			slog.Error("govern.disk.grow_failed",
				"diskIndex", a.diskIndex,
				"fromBytes", current,
				"toBytes", target,
				"err", err,
			)
		}
		return
	}

	a.growErrLogged = false // clear latch on success
	a.lastGrow = now
	slog.Info("govern.disk.grew",
		"diskIndex", a.diskIndex,
		"fromBytes", current,
		"toBytes", target,
	)
}
