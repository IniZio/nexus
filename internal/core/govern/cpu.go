package govern

import (
	"context"
	"log/slog"
	"time"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// CPU resize control law constants.
//
// Source column refers to OLD-nexus cpu_resize.go line numbers
// (packages/nexus/internal/engine/workspace/cpu_resize.go).
// TestCPUControlLawConstants is an anti-regression guard: it fails loudly if
// any constant drifts.
//
// Verified against OLD constants on 2026-08-14:
//
//	cpuEvalInterval      = 5s    ← OLD cpu_resize.go:26 cpuResizeEvalInterval
//	cpuGrowConsecutive   = 1     ← OLD cpu_resize.go:31
//	cpuShrinkConsecutive = 5     ← OLD cpu_resize.go:33
//	cpuGrowCooldown      = 60s   ← OLD cpu_resize.go:35
//	cpuShrinkCooldown    = 120s  ← OLD cpu_resize.go:36
//	cpuGrowPressure      = 15.0  ← OLD cpu_resize.go:41
//	cpuShrinkPressure    = 2.0   ← OLD cpu_resize.go:42
const (
	// cpuEvalInterval is the nominal poll cadence that the control law was written
	// against. It is used to derive cpuGrowWindow and cpuShrinkWindow so the law
	// is invariant to the adaptive poll cadence (loop.go:212-217 drops the interval
	// to 2 s under memory pressure; count-based tracking would make the CPU axis
	// 2.5× more aggressive in exactly the target scenario — pressured nested build).
	// Source: OLD cpu_resize.go:26.
	cpuEvalInterval = 5 * time.Second

	// cpuGrowConsecutive = 1: EAGER — a single high-pressure sample triggers grow.
	// Growth is eager because a build burst or compile spike needs headroom
	// without delay. Unlike memory, there is no OOM cliff — but throughput
	// collapses instantly on CPU starvation.
	// Source: OLD cpu_resize.go:31.
	cpuGrowConsecutive = 1

	// cpuShrinkConsecutive = 5: conservative anti-flap. The shrink window is
	// (cpuShrinkConsecutive-1) × cpuEvalInterval = 20 s of sustained idle.
	// Source: OLD cpu_resize.go:33.
	cpuShrinkConsecutive = 5

	// cpuGrowCooldown / cpuShrinkCooldown gate the inter-resize interval.
	// Source: OLD cpu_resize.go:35-36.
	cpuGrowCooldown   = 60 * time.Second
	cpuShrinkCooldown = 120 * time.Second

	// cpuGrowPressure: grow vCPUs when CPU some_avg10 is at or above this value.
	// Source: OLD cpu_resize.go:41.
	cpuGrowPressure = 15.0

	// cpuShrinkPressure: shrink when some_avg10 is strictly below this value.
	// Strict inequality (< not <=) enforces hysteresis with cpuGrowPressure.
	// Source: OLD cpu_resize.go:42.
	cpuShrinkPressure = 2.0
)

// Derived timing windows. Both use cpuEvalInterval so the constants are live and
// the formulas document the original count-based intent.
const (
	// cpuGrowWindow is the minimum sustained-pressure duration before a grow fires.
	// cpuGrowConsecutive=1 (eager) → (1-1)×5s = 0s: the first pressured sample
	// fires immediately. The formula generalises if cpuGrowConsecutive ever changes.
	cpuGrowWindow = time.Duration(cpuGrowConsecutive-1) * cpuEvalInterval // = 0 s

	// cpuShrinkWindow is the minimum sustained-idle duration before a shrink fires.
	// OLD increments shrinkCount to 1 on the first idle sample and fires at >= 5,
	// so it fires on the 5th idle sample = (5-1)×5s = 20s after the first.
	// Using wall time rather than sample count makes the law invariant to the
	// adaptive poll cadence. Without this fix: 5×2s = 10s under memory pressure,
	// 2× more shrink-aggressive than the ported 20s law.
	cpuShrinkWindow = time.Duration(cpuShrinkConsecutive-1) * cpuEvalInterval // = 20 s
)

// cpuAxis implements AxisEvaluator for the CPU resize dimension.
//
// It holds a pointer to the parent Governor for shared state (latest telemetry
// sample, clock, bounds) and owns its own CPU-specific counters. All fields
// are accessed from the governor goroutine only; no locking is needed.
type cpuAxis struct {
	g       *Governor
	resizer resize.CPUResizer

	// per-axis state — exclusively owned by this axis (governor goroutine only)
	//
	// growSince / shrinkSince track the wall-clock start of the current
	// continuous pressure or idle run. Zero means no run is active.
	// Time-based tracking (not count-based) makes the windows invariant to the
	// adaptive poll cadence (loop.go:212-217): under memory pressure the loop
	// drops to 2 s; count-based 5×2s = 10s would be 2× more aggressive than
	// the ported law ((cpuShrinkConsecutive-1) × cpuEvalInterval = 20 s).
	growSince           time.Time
	shrinkSince         time.Time
	lastResizeTime      time.Time
	lastResizeWasShrink bool
	currentVCPUs        int32 // axis's accounting of the desired/online count
}

// NewCPUAxis constructs a CPU axis, seeds currentVCPUs from the resizer, and
// registers it on g. Must be called before g.Run.
func NewCPUAxis(g *Governor, r resize.CPUResizer) *cpuAxis {
	a := &cpuAxis{
		g:            g,
		resizer:      r,
		currentVCPUs: r.CurrentVCPUs(),
	}
	g.RegisterAxis(a)
	return a
}

// Evaluate inspects the latest telemetry sample from the parent Governor and
// issues a ResizeCPU call when the CPU control law warrants one.
//
// Deviations from OLD:
//   - No workspaceID parameter (single-tenant, D-DC-12).
//   - CPU has no urgent/critical cooldown bypass. A vCPU shortage degrades
//     throughput rather than failing a build (survivable across a cooldown
//     window). Explicitly documented in OLD cpu_resize.go:242-249.
//   - VCPUOnline drift detection: when the guest reports a different online
//     count than the axis believes, the axis reconciles to the guest's ground
//     truth before computing the target.
//
// Hard PSI gate: if the guest does not report CPU PSI (CPUPSISupported=false),
// Evaluate takes NO action. An absent PSI reads as zero (perfectly idle) which
// would otherwise drive a spurious shrink on a starving VM.
//
// Source: OLD evaluateCPUForWorkspace (cpu_resize.go:155-268).
func (a *cpuAxis) Evaluate(ctx context.Context) {
	s := a.g.latest

	// Hard gate: no PSI → no action.
	// Source: OLD cpu_resize.go:104-106 (disabled check) and recordCPUStatsSample.
	if !s.CPUPSISupported {
		return
	}

	minVCPUs := a.g.bounds.VCPUMin
	maxVCPUs := a.g.bounds.VCPUMax
	if minVCPUs == 0 || maxVCPUs == 0 || minVCPUs >= maxVCPUs {
		// Governor is passive when CPU bounds are not configured.
		return
	}

	// Drift detection: reconcile axis accounting to guest ground truth.
	// VCPUOnline is measured by the guest agent (sysfs); VCPUCount is the boot
	// ceiling. A partial CH failure (fewer CPUs onlined than requested) must not
	// leave the governor's accounting silently wrong.
	// Source: OLD InitAdoptedCPUState (cpu_resize.go:325-358).
	if s.VCPUOnline != 0 && s.VCPUOnline != a.currentVCPUs {
		slog.Warn("govern.cpu.vcpu_drift",
			"expected", a.currentVCPUs,
			"online", s.VCPUOnline,
		)
		a.currentVCPUs = s.VCPUOnline
	}

	current := a.currentVCPUs
	if current == 0 {
		current = minVCPUs
	}

	now := a.g.clock.Now()

	// Update time-based run windows. These are updated BEFORE the cooldown check
	// so the windows accumulate during cooldown — matching OLD recordCPUStatsSample
	// behaviour (counters built up while in cooldown so the next step fires
	// immediately when the cooldown expires).
	// Source: OLD cpu_resize.go:104-126.
	switch {
	case cpuSampleWantsGrow(s):
		if a.growSince.IsZero() {
			a.growSince = now
		}
		a.shrinkSince = time.Time{}
	case cpuSampleWantsShrink(s):
		if a.shrinkSince.IsZero() {
			a.shrinkSince = now
		}
		a.growSince = time.Time{}
	default:
		a.growSince = time.Time{}
		a.shrinkSince = time.Time{}
	}

	// Post-resize cooldown — CPU has NO urgent bypass (unlike memory).
	// Source: OLD cpu_resize.go:217-249.
	if !a.lastResizeTime.IsZero() {
		cooldown := cpuGrowCooldown
		if a.lastResizeWasShrink {
			cooldown = cpuShrinkCooldown
		}
		if now.Sub(a.lastResizeTime) < cooldown {
			return
		}
	}

	var target int32
	isShrink := false

	switch {
	case cpuSampleWantsGrow(s) && !a.growSince.IsZero() && now.Sub(a.growSince) >= cpuGrowWindow:
		// cpuGrowWindow = 0s: fires immediately on the first pressured sample (eager).
		if current >= maxVCPUs {
			slog.Warn("govern.cpu.hard_max",
				"current", current,
				"max", maxVCPUs,
			)
			a.growSince = time.Time{}
			return
		}
		target = current + 1

	case cpuSampleWantsShrink(s) && !a.shrinkSince.IsZero() && now.Sub(a.shrinkSince) >= cpuShrinkWindow:
		// cpuShrinkWindow = 20s: requires sustained idle for (cpuShrinkConsecutive-1) × cpuEvalInterval.
		if current <= minVCPUs {
			return
		}
		target = current - 1
		isShrink = true

	default:
		return
	}

	// Clamp to bounds (belt-and-suspenders; CPUResizer also clamps).
	if target < minVCPUs {
		target = minVCPUs
	}
	if target > maxVCPUs {
		target = maxVCPUs
	}
	if target == current {
		return
	}

	// Reset run windows before resize (fresh series required for next step).
	// Matches OLD cpu_resize.go:253-254.
	a.growSince = time.Time{}
	a.shrinkSince = time.Time{}

	newCurrent, err := a.resizer.ResizeCPU(ctx, target)

	// Record resize time regardless of outcome so the cooldown fires and we
	// don't hammer a failing resizer on every tick.
	a.lastResizeTime = a.g.clock.Now()
	a.lastResizeWasShrink = isShrink

	if err != nil {
		slog.Warn("govern.cpu.resize_error",
			"err", err,
			"target", target,
		)
		return
	}

	a.currentVCPUs = newCurrent

	if newCurrent != current {
		if isShrink {
			slog.Info("govern.cpu.shrink", "from", current, "to", newCurrent)
		} else {
			slog.Info("govern.cpu.grow", "from", current, "to", newCurrent)
		}
	}
}

// cpuSampleWantsGrow reports whether s indicates the sandbox needs more vCPUs.
// Gated on CPUPSISupported — an absent PSI zero must never be read as pressure.
// Source: OLD cpu_resize.go:129-130.
func cpuSampleWantsGrow(s resize.Sample) bool {
	return s.CPUPSISupported && s.CPUPSISomeAvg10 >= cpuGrowPressure
}

// cpuSampleWantsShrink reports whether s indicates spare vCPUs the VM can give back.
// Strict less-than (not <=) enforces hysteresis with cpuGrowPressure.
// Gated on CPUPSISupported — an absent PSI zero must not drive a spurious shrink.
// Source: OLD cpu_resize.go:135-136.
func cpuSampleWantsShrink(s resize.Sample) bool {
	return s.CPUPSISupported && s.CPUPSISomeAvg10 < cpuShrinkPressure
}
