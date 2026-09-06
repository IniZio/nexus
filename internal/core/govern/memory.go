// Package govern implements the per-sandbox auto-resize governor loop for
// nexus3. It is single-tenant: one Governor per supervisor process, owning
// one sandbox. Multi-axis (CPU, disk) is planned for later waves; this file
// carries the MEMORY axis only (D-DC-18).
//
// All constants in this file are verified verbatim against OLD-nexus
// memory_resize.go (packages/nexus/internal/engine/workspace/memory_resize.go).
// TestMemoryControlLawConstants is an anti-regression guard: it fails loudly
// if any constant drifts (e.g. grow threshold back to 0.15, consecutive back to 2).
package govern

import (
	"context"
	"log/slog"
	"time"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// Control law constants.
//
// Source column refers to OLD-nexus memory_resize.go line numbers.
// These are the corrected values per the 2026-08-14 audit (D-DC-23).
const (
	// memoryResizeBootDelay is the quiet period after VM start before the
	// governor begins sampling. Matches OLD memoryResizeBootDelay.
	// Source: OLD memory_resize.go:43.
	memoryResizeBootDelay = 10 * time.Second

	// memoryEvalInterval is the nominal poll interval when no pressure is seen.
	// Source: OLD memory_resize.go:42 memoryResizeEvalInterval = 5s.
	memoryEvalInterval = 5 * time.Second

	// memoryPressurePollInterval is the fast-poll interval used once the
	// previous sample shows pressure. Not in OLD (which used event-driven push).
	// nexus3 uses adaptive polling (D-DC-10); 2s reduces actuation lag while
	// the VM is thrashing.
	memoryPressurePollInterval = 2 * time.Second

	// memoryGrowConsecutive = 1: EAGER — one starved sample suffices.
	// Reacting to a single starved sample rather than waiting for a run of them
	// keeps a fast allocator (node/Vite/storybook spinning up) from OOM-killing
	// before the governor responds.
	// Source: OLD memory_resize.go:51 (corrected from the 0.15/2 draft).
	memoryGrowConsecutive = 1

	// memoryShrinkConsecutive = 5: conservative, so the VM never flaps.
	// Source: OLD memory_resize.go:52.
	memoryShrinkConsecutive = 5

	// memoryGrowCooldown / memoryShrinkCooldown gate the inter-resize interval.
	// Source: OLD memory_resize.go:53-54.
	memoryGrowCooldown   = 60 * time.Second
	memoryShrinkCooldown = 120 * time.Second

	// defaultGrowThreshold = 0.20 (not 0.15).
	// MemAvailable stays flat under cache pressure then falls off a cliff; 0.20
	// buys headroom to absorb that cliff before the OOM killer fires.
	// Source: OLD memory_resize.go:60-63 (corrected — the earlier draft cited 0.15 incorrectly).
	defaultGrowThreshold = 0.20

	// criticalGrowThreshold = 0.08: MemAvailable backstop for the cooldown
	// bypass. When the ratio is this low the sandbox is on the edge of OOM.
	// Source: OLD memory_resize.go:64-66.
	criticalGrowThreshold = 0.08

	// defaultShrinkThreshold = 0.45: fraction of MemTotal above which the VM
	// has comfortable headroom and can return memory.
	// Source: OLD memory_resize.go:67.
	defaultShrinkThreshold = 0.45

	// PSI thresholds (percent of the last 10s spent stalled on memory).
	// PSI is the LEADING signal — it rises while the kernel thrashes reclaim,
	// before MemAvailable collapses.
	// Source: OLD memory_resize.go:68-76.
	psiGrowPressure     = 10.0
	psiCriticalPressure = 10.0

	// defaultSwapPressureRatio = 0.20: SwapUsed/MemTotal ratio above which the
	// grow signal fires when SwapUsed has ALSO INCREASED since the previous
	// sample (D-RAM-07, flow-gated by D-RAM-10).
	//
	// zram converts memory pressure into CPU cost and manufactures MemAvailable
	// headroom by compressing guest pages into guest RAM. MemAvailable cannot
	// observe this; PSI avg10 drops to zero when zram is idle but loaded. The
	// signal requires BOTH a ratio above this threshold AND an increase in
	// SwapUsed since the previous sample (stock→flow conversion, D-RAM-10):
	// a stable high SwapUsed is already accounted for in the guest's working
	// set and does not warrant a further grow; only freshly accumulated swap
	// indicates new pressure. The live measurement that motivated D-RAM-07
	// showed 46% (710 MiB of 1.5 GiB), well above this threshold.
	//
	// D-RAM-10 (2026-08-31): the original stock-only check was a monotonic
	// ratchet — every sample with SwapUsed > 20% fired a grow regardless of
	// whether swap was accumulating or stable, driving unbounded 256 MiB/min
	// growth. Chosen approach: prevSwapUsed tracking in Governor (stock→flow
	// via sample delta) rather than /proc/vmstat pswpin/pswpout, because
	// pswpin counts raw swap-in pages and does not distinguish zram from disk
	// swap, while the delta of the SwapUsed field (derived from the same
	// /proc/meminfo the rest of the governor already reads) is already in the
	// Sample, requires no new kernel file, and directly measures the quantity
	// the signal intends to track.
	//
	// Gated on SwapTotalBytes > 0 — pre-D-RAM-07 agents leave it zero.
	defaultSwapPressureRatio = 0.20


	// minGrowStepBytes: floor on each grow increment (256 MiB).
	// Source: OLD spec §4.3, D-DC-23 corrected value.
	minGrowStepBytes int64 = 256 * 1024 * 1024

	// minShrinkStepBytes: floor on each shrink increment (512 MiB).
	// Source: OLD memory_resize.go:71.
	minShrinkStepBytes int64 = 512 * 1024 * 1024

	// memHotplugAlignBytes is Cloud Hypervisor's memory-hotplug block
	// granularity: every desired_ram passed to PUT /api/v1/vm.resize MUST be an
	// exact multiple of this value or CH rejects the call with HTTP 500.
	//
	// Root cause (live evidence, sandbox sb-06G4EHTE1NZR56F89BJK6N3E90): the
	// deficit-scaled growStep/shrinkStep produce arbitrary byte targets. Every
	// SUCCESSFUL resize observed on the wire was an exact multiple of 256 MiB
	// (805306368, 1073741824, 1342177280, 1610612736, 2147483648, 4294967296);
	// every FAILING resize was a non-256-MiB deficit target (1091000320,
	// 1112324096, 843703500, 1116993536, 1373887283 → all "vm.resize:
	// unexpected status 500"). After an idle-shrink to the 512 MiB floor every
	// unaligned regrow 500'd, the guest never got memory back, and the in-guest
	// process was OOM-killed. Aligning every resize target to this block size is
	// the fix.
	//
	// 256 MiB is deliberately NOT reusing minGrowStepBytes (which happens to be
	// the same value); the two constants encode different constraints (grow-step
	// floor vs. hotplug alignment) and must be free to diverge.
	//
	// EMPIRICAL, not read from CH: CH exposes no block-granularity field in the
	// vm.info / VmConfig subset nexus3 parses (see driver client.go —
	// vmInfoResponse carries only "state"). 256 MiB is the observed floor at
	// which every live resize succeeded; if a rebuilt CH advertises a different
	// section size this constant is the single point to adjust.
	memHotplugAlignBytes int64 = 256 * 1024 * 1024

	// safeMemFloorBytes is the memory level the governor jumps to in ONE hotplug
	// when the guest is critically low (PSI-full ≥ 10 or MemAvailable < 8%) and
	// current RAM is below this floor. This converts a multi-step deficit
	// staircase (512→768→~1.1G→~1.6G over ~15 s) into a single ~5 s poll, so a
	// Claude Code launch (~1.6 GiB cold) is absorbed before ZRAM swap saturates.
	//
	// This is a CEILING on the rescue, not a boot floor: the idle shrink path
	// remains aggressive and will reclaim back to minBytes when pressure clears,
	// preserving fine-grained control at steady state. The jump fires on BOTH
	// cold-start AND post-idle-shrink spikes — it is gated on
	// critical && current < safeMemFloorBytes, not on "first-ever grow".
	//
	// 2 GiB is a multiple of memHotplugAlignBytes (8 × 256 MiB), so no extra
	// alignment block is consumed by the jump itself.
	safeMemFloorBytes int64 = 2 * 1024 * 1024 * 1024
)

// sampleWantsGrow reports whether s indicates the sandbox needs more memory.
//
// Three independent signals are checked in order:
//
//  1. PSI some_avg10 (leading): any sustained stall above psiGrowPressure means
//     grow. Gated on MemPSISupported — an unsupported-PSI zero must NEVER be
//     read as "healthy".
//
//  2. MemAvailable ratio (lagging backstop): ratio < defaultGrowThreshold (0.20)
//     catches collapses that PSI misses when the kernel races the OOM path.
//
//  3. Swap-pressure term (flow-gated, D-RAM-07 + D-RAM-10): fires when zram is
//     active, SwapUsed/MemTotal >= defaultSwapPressureRatio (0.20), AND
//     SwapUsed has INCREASED since the previous sample. The increase guard
//     (stock→flow conversion) prevents the monotonic ratchet that the original
//     stock-only check caused: a stable high SwapUsed no longer triggers
//     repeated grows. prevSwapUsed is the caller's previous sample's SwapUsed
//     (Governor.prevSwapUsed); pass 0 on first sample (increase from 0 to any
//     non-zero SwapUsed is treated as a genuine new-pressure event).
//     Gated on SwapTotalBytes > 0 (pre-D-RAM-07 agents leave it zero).
//
// Source: OLD memory_resize.go:228-243.
func sampleWantsGrow(s resize.Sample, prevSwapUsed uint64) bool {
	if s.MemTotalBytes == 0 {
		return false
	}
	// Signal 1: PSI — leading flow indicator.
	if s.MemPSISupported && s.MemPSISomeAvg10 >= psiGrowPressure {
		return true
	}
	// Signal 2: MemAvailable ratio — lagging backstop.
	ratio := float64(s.MemAvailableBytes) / float64(s.MemTotalBytes)
	if ratio < defaultGrowThreshold {
		return true
	}
	// Signal 3: swap-pressure flow gate — zram indicator (D-RAM-07 + D-RAM-10).
	// Fires only when SwapUsed has increased (new pressure accumulating), not on
	// a stable stock. A static high SwapUsed is already part of the guest's
	// working set and does not warrant a further grow.
	if s.SwapTotalBytes > 0 && s.SwapFreeBytes <= s.SwapTotalBytes {
		swapUsed := s.SwapTotalBytes - s.SwapFreeBytes
		if swapUsed > prevSwapUsed &&
			float64(swapUsed)/float64(s.MemTotalBytes) >= defaultSwapPressureRatio {
			return true
		}
	}
	return false
}

// sampleWantsShrink reports whether s indicates spare memory the VM can give
// back. All three grow-trigger conditions must be absent before the governor
// considers shrinking — never shrink a VM that any grow signal considers starved.
//
//   - PSI stall: active memory stall blocks shrink (gated on MemPSISupported).
//   - Swap-flow gate (D-RAM-13): shrink is blocked only when SwapUsed has
//     INCREASED since the previous sample, not on a stock ratio. A guest
//     actively accumulating zram pages must not be shrunk; a guest with a stable
//     (non-increasing) SwapUsed has found its working set and the MemAvailable
//     ratio is the correct signal. This removes the pin floor that the
//     defaultSwapShrinkBlockRatio = 0.10 check imposed (required MemTotal >
//     10 × SwapUsed before shrink was allowed, pinning a 710 MiB swap guest
//     at ≥7.1 GiB indefinitely — D-RAM-13).
//     prevSwapUsed is the caller's previous sample's SwapUsed
//     (Governor.prevSwapUsed); the zero value on first governor start causes the
//     block to fire conservatively whenever any swap is present (non-zero
//     SwapUsed > 0 = prevSwapUsed). Gated on SwapTotalBytes > 0.
//   - MemAvailable ratio: only shrink when ratio > defaultShrinkThreshold (0.45).
//
// PSI check is gated on MemPSISupported.
// Source: OLD memory_resize.go:245-256.
func sampleWantsShrink(s resize.Sample, prevSwapUsed uint64) bool {
	if s.MemTotalBytes == 0 {
		return false
	}
	// Block shrink when PSI reports active stall.
	if s.MemPSISupported && s.MemPSISomeAvg10 >= psiGrowPressure {
		return false
	}
	// Block shrink when swap is actively increasing (D-RAM-13 flow gate).
	// Symmetric with sampleWantsGrow: only a recent SwapUsed increase blocks,
	// never a stock ratio. A stable high SwapUsed is part of the working set.
	if s.SwapTotalBytes > 0 && s.SwapFreeBytes <= s.SwapTotalBytes {
		swapUsed := s.SwapTotalBytes - s.SwapFreeBytes
		if swapUsed > prevSwapUsed {
			return false
		}
	}
	ratio := float64(s.MemAvailableBytes) / float64(s.MemTotalBytes)
	return ratio > defaultShrinkThreshold
}

// sampleIsCritical reports whether s warrants bypassing the post-resize
// cooldown. Two independent bypasses are checked (D-DC-23):
//
//  1. PSI-based: MemPSIFullAvg10 >= psiCriticalPressure (every task stalling,
//     imminent OOM). Gated on MemPSISupported — an unsupported zero must not
//     be treated as critical.
//  2. MemAvailable-based: ratio < criticalGrowThreshold (0.08). This backstop
//     fires when PSI is absent or when MemAvailable collapses before the PSI
//     sampler catches up.
//
// Source: OLD memory_resize.go:258-270.
func sampleIsCritical(s resize.Sample) bool {
	if s.MemTotalBytes == 0 {
		return false
	}
	// Bypass 1: PSI "full" — every task stalling.
	if s.MemPSISupported && s.MemPSIFullAvg10 >= psiCriticalPressure {
		return true
	}
	// Bypass 2: MemAvailable floor backstop.
	ratio := float64(s.MemAvailableBytes) / float64(s.MemTotalBytes)
	return ratio < criticalGrowThreshold
}

// growStep computes the grow increment. Deficit-scaled (spec §4.3): grows by
// enough to restore a healthy MemAvailable buffer (the shrink threshold) in a
// single step, so the governor does not chase a fast allocator across many
// small grows that each lag the workload. Floored at minGrowStepBytes, capped
// at half the hotplug range, clamped to remaining room below the ceiling.
//
// Source: OLD memory_resize.go:514-554.
//
// Deviation from OLD: nexus3 applies minGrowStepBytes as the initial step
// value (floor-before-cap), whereas OLD applies the floor after the cap
// (memory_resize.go:546-547). The behaviors are equivalent here because
// stepCap is itself floored at minGrowStepBytes (lines below), so stepCap is
// always >= minGrowStepBytes, making the relative order of floor and cap
// immaterial. The file header's "verified verbatim" claim covers constants and
// control-law logic; this ordering detail is a deliberate structural choice
// that produces identical runtime results.
func growStep(minBytes, maxBytes, current int64, s resize.Sample) int64 {
	delta := maxBytes - minBytes

	// Per-step ceiling: half the configurable range, but never below the floor.
	stepCap := delta / 2
	if stepCap < minGrowStepBytes {
		stepCap = minGrowStepBytes
	}
	if stepCap > delta {
		stepCap = delta
	}

	// Deficit: extra MemAvailable needed to reach the shrink threshold. Adding
	// this many bytes of total RAM restores roughly that much available memory.
	step := minGrowStepBytes
	if s.MemTotalBytes > 0 {
		targetAvail := int64(float64(s.MemTotalBytes) * defaultShrinkThreshold)
		if deficit := targetAvail - int64(s.MemAvailableBytes); deficit > step {
			step = deficit
		}
	}
	if step > stepCap {
		step = stepCap
	}
	remaining := maxBytes - current
	if step > remaining {
		step = remaining
	}
	if step < 0 {
		step = 0
	}
	return step
}

// shrinkStep computes the shrink increment. Demand-scaled (symmetric with
// growStep, spec §4.3): shrinks by only the SURPLUS MemAvailable above
// defaultShrinkThreshold, so a healthy current allocation returns to roughly
// that threshold in one step instead of losing a fixed fraction of the
// entire configured range. Floored at minShrinkStepBytes, capped at half the
// hotplug range, and capped at the room remaining above minBytes so the
// result can never go negative.
//
// D-DC-?? (2026-08-30 live thrash fix): the previous implementation was
// `(maxBytes-minBytes)/2`, ignoring current and the sample entirely. With
// min=512MiB/max=8GiB that is a fixed 3.75GiB step regardless of how much
// memory the guest actually had allocated — from a healthy current of 2GiB
// that overshoots past zero and clamps to the minBytes floor. The very next
// eager sample (memoryGrowConsecutive=1) then finds the guest starved at
// 512MiB and jumps back up (often via the safeMemFloorBytes rescue), and the
// cycle repeats indefinitely: shrink and grow chasing a range-sized step
// against a workload that never stopped needing roughly the same amount of
// memory. Scaling off the actual surplus (mirroring growStep's deficit
// scaling) makes shrink return to a size just above the threshold instead of
// always undershooting to the floor, breaking the oscillation.
//
// Source: OLD memory_resize.go:555-568 (range-scaled formula superseded by
// this demand-scaled one; OLD's "verified verbatim" header claim does not
// cover this function post-fix).
func shrinkStep(minBytes, maxBytes, current int64, s resize.Sample) int64 {
	delta := maxBytes - minBytes

	// Per-step ceiling: half the configurable range, but never below the floor.
	stepCap := delta / 2
	if stepCap < minShrinkStepBytes {
		stepCap = minShrinkStepBytes
	}
	if stepCap > delta {
		stepCap = delta
	}

	// Surplus: how far MemAvailable sits above the shrink threshold. Removing
	// this many bytes of total RAM brings MemAvailable back down to roughly
	// the threshold — the mirror image of growStep's deficit calculation.
	step := minShrinkStepBytes
	if s.MemTotalBytes > 0 {
		targetAvail := int64(float64(s.MemTotalBytes) * defaultShrinkThreshold)
		if surplus := int64(s.MemAvailableBytes) - targetAvail; surplus > step {
			step = surplus
		}
	}
	if step > stepCap {
		step = stepCap
	}
	remaining := current - minBytes
	if step > remaining {
		step = remaining
	}
	if step < 0 {
		step = 0
	}
	return step
}

// alignUp rounds n UP to the nearest multiple of align (align > 0). Used for
// grow targets: a grow must never round to LESS memory than requested.
func alignUp(n, align int64) int64 {
	if align <= 0 {
		return n
	}
	rem := n % align
	if rem == 0 {
		return n
	}
	if rem < 0 {
		// n is negative; rounding "up" (toward +inf) drops the fractional block.
		return n - rem
	}
	return n + (align - rem)
}

// alignDown rounds n DOWN to the nearest multiple of align (align > 0). Used
// for shrink targets: a shrink must never round to MORE memory than requested.
func alignDown(n, align int64) int64 {
	if align <= 0 {
		return n
	}
	rem := n % align
	if rem == 0 {
		return n
	}
	if rem < 0 {
		// n is negative; rounding "down" (toward -inf) subtracts a full block.
		return n - rem - align
	}
	return n - rem
}

// evaluate inspects the latest telemetry sample recorded in g and issues a
// ResizeMemory call when the control law warrants one.
//
// All state is owned by the Governor struct; evaluate runs on the governor
// goroutine exclusively so no locking is needed.
//
// Deviations from OLD:
//   - No workspaceID parameter (single-tenant, D-DC-12).
//   - No bootToRequest path (nexus3 has no user-requested allocation field).
//   - Host headroom fails CONSERVATIVE (not fail-open as in OLD). OLD fails
//     open because a transient read failure must not starve a multi-workspace
//     system; nexus3 fails conservative because the motivating failure was a
//     HOST OOM wall during nested builds, and a failed headroom check must
//     never amplify that failure.
//
// Source: OLD evaluateMemoryForWorkspace (memory_resize.go:310-442).
func (g *Governor) evaluate(ctx context.Context) {
	// Guard: sample must be fresh (re-checked here even though Run checks age
	// before calling; the delta may grow if evaluate is slow).
	if g.lastSampleTime.IsZero() || g.clock.Now().Sub(g.lastSampleTime) > resize.SampleMaxAge {
		return
	}

	minBytes := g.bounds.MemMinBytes
	maxBytes := g.bounds.MemMaxBytes
	if minBytes == 0 || maxBytes == 0 || minBytes >= maxBytes {
		// Governor is passive when bounds are not configured.
		return
	}

	current := g.resizer.CurrentMemoryBytes()
	if current == 0 {
		current = minBytes
	}

	// Update consecutive-sample counters.
	// These accumulate unconditionally (including during cooldown) so that
	// when the cooldown expires the count is already built up. Matches OLD
	// recordMemoryStatsSample behaviour (memory_resize.go:142-180).
	switch {
	case sampleWantsGrow(g.latest, g.prevSwapUsed):
		g.growCount++
		g.shrinkCount = 0
	case sampleWantsShrink(g.latest, g.prevSwapUsed):
		g.shrinkCount++
		g.growCount = 0
	default:
		g.growCount = 0
		g.shrinkCount = 0
	}

	critical := sampleIsCritical(g.latest) && current < maxBytes

	// Post-resize cooldown — skipped when the sample is critical.
	// The two critical bypasses (PSI-full and MemAvailable < 0.08) are
	// embedded in sampleIsCritical.
	if !critical && !g.lastResizeTime.IsZero() {
		cooldown := memoryGrowCooldown
		if g.lastResizeWasShrink {
			cooldown = memoryShrinkCooldown
		}
		if g.clock.Now().Sub(g.lastResizeTime) < cooldown {
			return
		}
	}

	var target int64
	isShrink := false

	switch {
	case sampleWantsGrow(g.latest, g.prevSwapUsed) && g.growCount >= memoryGrowConsecutive:
		if current >= maxBytes {
			slog.Warn("govern.memory.hard_max",
				"current", current,
				"max", maxBytes,
			)
			g.growCount = 0
			return
		}
		if critical && current < safeMemFloorBytes {
			// Jump-to-floor: one hotplug to safeMemFloorBytes instead of the
			// multi-step deficit staircase. Fires on cold-start AND
			// post-idle-shrink spikes — gated on current < safeMemFloorBytes,
			// NOT on "first-ever grow only". The bounds clamp below handles
			// maxBytes < safeMemFloorBytes conservatively.
			target = safeMemFloorBytes
		} else {
			target = current + growStep(minBytes, maxBytes, current, g.latest)
		}

	case sampleWantsShrink(g.latest, g.prevSwapUsed) && g.shrinkCount >= memoryShrinkConsecutive:
		if current <= minBytes {
			return
		}
		target = current - shrinkStep(minBytes, maxBytes, current, g.latest)
		isShrink = true

	default:
		return
	}

	// Align to CH's memory-hotplug block granularity BEFORE clamping.
	//
	// growStep/shrinkStep are deficit/range-scaled and produce arbitrary byte
	// targets; an unaligned desired_ram is rejected by CH with HTTP 500 (see
	// memHotplugAlignBytes). Grow rounds UP (never round to less memory than the
	// deficit demands); shrink rounds DOWN (never round to more). The
	// forward-progress guards keep a grow moving up by at least one block and a
	// shrink moving down by at least one block even after rounding, so alignment
	// can never turn a resize into a no-op or a backwards move.
	if isShrink {
		target = alignDown(target, memHotplugAlignBytes)
		if target >= current {
			target = current - memHotplugAlignBytes
		}
	} else {
		target = alignUp(target, memHotplugAlignBytes)
		if target <= current {
			target = current + memHotplugAlignBytes
		}
	}

	// Clamp to bounds (belt-and-suspenders; MemoryResizer also clamps).
	// MemMinBytes and MemMaxBytes are themselves block-aligned, so clamping a
	// block-aligned target keeps it aligned.
	if target < minBytes {
		target = minBytes
	}
	if target > maxBytes {
		target = maxBytes
	}
	if target == current {
		return
	}

	// Host-headroom admission control (grow only).
	//
	// Fail CONSERVATIVE: if headroom cannot be determined, refuse the grow.
	// This is the primary guard against host OOM during nested builds
	// (the motivating failure for this entire effort). It is a deliberate
	// deviation from OLD which fails open because a transient read error must
	// not starve a PSI-pressured workspace in a multi-tenant system. In nexus3
	// the analogous risk is causing the HOST to OOM, which is worse.
	//
	// Future consideration (not for now): when nexus3 itself runs inside an
	// outer nexus3 sandbox, host MemAvailable underestimates true headroom
	// because the outer host can itself grow (G-3 from ticket-13).
	if !isShrink {
		ok, err := g.headroom.HasHeadroom(ctx, target-current)
		if err != nil {
			slog.Warn("govern.memory.headroom_error",
				"err", err,
				"target", target,
				"action", "refusing_grow_conservative",
			)
			return // fail conservative
		}
		if !ok {
			slog.Info("govern.memory.headroom_insufficient",
				"target", target,
				"current", current,
			)
			return
		}
	}

	// Reset counters before resize (fresh series required for the next step).
	// Matches OLD memory_resize.go:440-443.
	g.growCount = 0
	g.shrinkCount = 0

	newCurrent, err := g.resizer.ResizeMemory(ctx, target)

	// Record resize time regardless of outcome so the cooldown fires and we
	// don't hammer a failing resizer on every tick.
	g.lastResizeTime = g.clock.Now()
	g.lastResizeWasShrink = isShrink

	if err != nil {
		slog.Warn("govern.memory.resize_error",
			"err", err,
			"target", target,
		)
		return
	}

	if newCurrent != current {
		if isShrink {
			slog.Info("govern.memory.shrink", "from", current, "to", newCurrent)
		} else {
			slog.Info("govern.memory.grow", "from", current, "to", newCurrent)
		}
	}
}
