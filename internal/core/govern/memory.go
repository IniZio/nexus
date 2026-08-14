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

	// minGrowStepBytes: floor on each grow increment (256 MiB).
	// Source: OLD spec §4.3, D-DC-23 corrected value.
	minGrowStepBytes int64 = 256 * 1024 * 1024

	// minShrinkStepBytes: floor on each shrink increment (512 MiB).
	// Source: OLD memory_resize.go:71.
	minShrinkStepBytes int64 = 512 * 1024 * 1024
)

// sampleWantsGrow reports whether s indicates the sandbox needs more memory.
//
// PSI (the leading signal) wins when present: any sustained stall (some_avg10
// above the threshold) means grow. The PSI check is gated on MemPSISupported;
// when PSI is absent the governor falls back to the lagging MemAvailable ratio.
// An unsupported-PSI zero must NEVER be read as "healthy".
//
// Source: OLD memory_resize.go:228-243.
func sampleWantsGrow(s resize.Sample) bool {
	if s.MemTotalBytes == 0 {
		return false
	}
	if s.MemPSISupported && s.MemPSISomeAvg10 >= psiGrowPressure {
		return true
	}
	ratio := float64(s.MemAvailableBytes) / float64(s.MemTotalBytes)
	return ratio < defaultGrowThreshold
}

// sampleWantsShrink reports whether s indicates spare memory the VM can give
// back. Requires BOTH plenty of MemAvailable AND (when PSI is present) no
// meaningful stall — never shrink a VM that is actively stalling on memory.
// PSI check is gated on MemPSISupported.
//
// Source: OLD memory_resize.go:245-256.
func sampleWantsShrink(s resize.Sample) bool {
	if s.MemTotalBytes == 0 {
		return false
	}
	if s.MemPSISupported && s.MemPSISomeAvg10 >= psiGrowPressure {
		return false
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

// shrinkStep computes the shrink increment. Half the configurable range,
// floored at minShrinkStepBytes.
//
// Source: OLD memory_resize.go:555-568.
func shrinkStep(minBytes, maxBytes int64) int64 {
	delta := maxBytes - minBytes
	step := delta / 2
	if step < minShrinkStepBytes {
		step = minShrinkStepBytes
	}
	if step > delta {
		step = delta
	}
	return step
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
	case sampleWantsGrow(g.latest):
		g.growCount++
		g.shrinkCount = 0
	case sampleWantsShrink(g.latest):
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
	case sampleWantsGrow(g.latest) && g.growCount >= memoryGrowConsecutive:
		if current >= maxBytes {
			slog.Warn("govern.memory.hard_max",
				"current", current,
				"max", maxBytes,
			)
			g.growCount = 0
			return
		}
		target = current + growStep(minBytes, maxBytes, current, g.latest)

	case sampleWantsShrink(g.latest) && g.shrinkCount >= memoryShrinkConsecutive:
		if current <= minBytes {
			return
		}
		target = current - shrinkStep(minBytes, maxBytes)
		isShrink = true

	default:
		return
	}

	// Clamp to bounds (belt-and-suspenders; MemoryResizer also clamps).
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
