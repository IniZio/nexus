package govern

import (
	"context"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// ── Anti-regression constant guard ────────────────────────────────────────────

// TestCPUControlLawConstants pins the control law constants against the
// verbatim values from OLD cpu_resize.go. Any drift from these values is a
// regression: a constant change must be justified by a spec change, not
// silence.
//
// Source: OLD cpu_resize.go:26-42.
func TestCPUControlLawConstants(t *testing.T) {
	if cpuEvalInterval != 5*time.Second {
		t.Errorf("cpuEvalInterval = %v, want 5s (OLD cpu_resize.go:26)", cpuEvalInterval)
	}
	if cpuGrowConsecutive != 1 {
		t.Errorf("cpuGrowConsecutive = %d, want 1 (eager, OLD cpu_resize.go:31)", cpuGrowConsecutive)
	}
	if cpuShrinkConsecutive != 5 {
		t.Errorf("cpuShrinkConsecutive = %d, want 5 (OLD cpu_resize.go:33)", cpuShrinkConsecutive)
	}
	if cpuGrowCooldown != 60*time.Second {
		t.Errorf("cpuGrowCooldown = %v, want 60s (OLD cpu_resize.go:35)", cpuGrowCooldown)
	}
	if cpuShrinkCooldown != 120*time.Second {
		t.Errorf("cpuShrinkCooldown = %v, want 120s (OLD cpu_resize.go:36)", cpuShrinkCooldown)
	}
	if cpuGrowPressure != 15.0 {
		t.Errorf("cpuGrowPressure = %v, want 15.0 (OLD cpu_resize.go:41)", cpuGrowPressure)
	}
	if cpuShrinkPressure != 2.0 {
		t.Errorf("cpuShrinkPressure = %v, want 2.0 (OLD cpu_resize.go:42)", cpuShrinkPressure)
	}
	// Derived windows: verify they use cpuEvalInterval so the formula is live.
	if cpuGrowWindow != 0 {
		t.Errorf("cpuGrowWindow = %v, want 0s (eager: (cpuGrowConsecutive-1)×cpuEvalInterval)", cpuGrowWindow)
	}
	if cpuShrinkWindow != 20*time.Second {
		t.Errorf("cpuShrinkWindow = %v, want 20s ((cpuShrinkConsecutive-1)×cpuEvalInterval, matches OLD fire-at-5th-sample)", cpuShrinkWindow)
	}
}

// ── fakeCPUResizer ─────────────────────────────────────────────────────────────

// fakeCPUResizer records ResizeCPU calls.
type fakeCPUResizer struct {
	current  int32
	calls    []int32 // ordered list of targets passed to ResizeCPU
	resizeErr error  // if non-nil, ResizeCPU returns this error
}

func newFakeCPUResizer(bootVCPUs int32) *fakeCPUResizer {
	return &fakeCPUResizer{current: bootVCPUs}
}

func (r *fakeCPUResizer) ResizeCPU(_ context.Context, targetVCPUs int32) (int32, error) {
	if r.resizeErr != nil {
		return r.current, r.resizeErr
	}
	r.current = targetVCPUs
	r.calls = append(r.calls, targetVCPUs)
	return targetVCPUs, nil
}

func (r *fakeCPUResizer) CurrentVCPUs() int32 { return r.current }

// callCount returns how many ResizeCPU calls were made.
func (r *fakeCPUResizer) callCount() int { return len(r.calls) }

// ── Test helpers ───────────────────────────────────────────────────────────────

// cpuBounds returns a Bounds with memory unconfigured but CPU configured
// for [min, max].
func cpuBounds(min, max int32) resize.Bounds {
	return resize.Bounds{
		// Memory bounds must be zero or equal so the memory axis stays passive;
		// the CPU axis reads VCPUMin/VCPUMax directly.
		VCPUMin: min,
		VCPUMax: max,
	}
}

// newCPUGovernorAndAxis creates a Governor with a fake clock and a CPU axis,
// with memory bounds unconfigured (so the memory axis is passive).
func newCPUGovernorAndAxis(clk *fakeClock, bootVCPUs int32, bounds resize.Bounds) (*Governor, *cpuAxis, *fakeCPUResizer) {
	fr := newFakeCPUResizer(bootVCPUs)
	// Governor needs a MemoryResizer even when the memory axis is passive.
	memResizer := newFakeResizer(1024 * 1024 * 1024)
	g := New(Config{
		Resizer:   memResizer,
		Telemetry: &fakeTelemetry{},
		Headroom:  &fakeHeadroom{ok: true},
		Bounds:    bounds,
		Clock:     clk,
	})
	a := NewCPUAxis(g, fr)
	return g, a, fr
}

// injectCPUSample sets the governor's latest sample and marks the sample time.
func injectCPUSample(g *Governor, clk *fakeClock, s resize.Sample) {
	s.Timestamp = clk.Now()
	g.latest = s
	g.lastSampleTime = clk.Now()
}

// pressuredSample returns a sample with CPU PSI above the grow threshold.
func pressuredSample() resize.Sample {
	return resize.Sample{
		CPUPSISupported: true,
		CPUPSISomeAvg10: 20.0, // above cpuGrowPressure (15.0)
	}
}

// idleSample returns a sample with CPU PSI below the shrink threshold.
func idleSample() resize.Sample {
	return resize.Sample{
		CPUPSISupported: true,
		CPUPSISomeAvg10: 0.5, // below cpuShrinkPressure (2.0)
	}
}

// deadBandSample returns a sample in the dead band (no grow, no shrink).
func deadBandSample() resize.Sample {
	return resize.Sample{
		CPUPSISupported: true,
		CPUPSISomeAvg10: 8.0, // between 2.0 and 15.0
	}
}

// noPSISample returns a sample with CPUPSISupported=false.
func noPSISample() resize.Sample {
	return resize.Sample{
		CPUPSISupported: false,
		CPUPSISomeAvg10: 20.0, // would look like pressure if PSI were trusted
	}
}

// ── Pure-function signal tests ─────────────────────────────────────────────────

// TestCPUSampleSignals mirrors OLD TestCPUSampleSignals (cpu_resize_test.go).
// Verifies the grow/shrink predicate functions against the boundary cases that
// matter most, especially the PSI gate.
func TestCPUSampleSignals(t *testing.T) {
	cases := []struct {
		name       string
		s          resize.Sample
		wantGrow   bool
		wantShrink bool
	}{
		{
			name:       "PSI absent => neither grow nor shrink (zero reads as idle without PSI)",
			s:          noPSISample(),
			wantGrow:   false,
			wantShrink: false,
		},
		{
			name:       "PSI absent, zero pressure => neither",
			s:          resize.Sample{CPUPSISupported: false, CPUPSISomeAvg10: 0},
			wantGrow:   false,
			wantShrink: false,
		},
		{
			name:       "PSI present, some_avg10 >= 15 => grow",
			s:          resize.Sample{CPUPSISupported: true, CPUPSISomeAvg10: 20},
			wantGrow:   true,
			wantShrink: false,
		},
		{
			name:       "PSI present, some_avg10 < 2 => shrink",
			s:          resize.Sample{CPUPSISupported: true, CPUPSISomeAvg10: 0},
			wantGrow:   false,
			wantShrink: true,
		},
		{
			name:       "PSI present, dead band (8) => neither",
			s:          resize.Sample{CPUPSISupported: true, CPUPSISomeAvg10: 8},
			wantGrow:   false,
			wantShrink: false,
		},
		{
			name:       "boundary: exactly 15.0 => grow (>= threshold)",
			s:          resize.Sample{CPUPSISupported: true, CPUPSISomeAvg10: 15.0},
			wantGrow:   true,
			wantShrink: false,
		},
		{
			name:       "boundary: exactly 2.0 => NOT shrink (shrink is strict < 2)",
			s:          resize.Sample{CPUPSISupported: true, CPUPSISomeAvg10: 2.0},
			wantGrow:   false,
			wantShrink: false,
		},
		{
			name:       "just below shrink threshold 1.99 => shrink",
			s:          resize.Sample{CPUPSISupported: true, CPUPSISomeAvg10: 1.99},
			wantGrow:   false,
			wantShrink: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cpuSampleWantsGrow(tc.s); got != tc.wantGrow {
				t.Errorf("cpuSampleWantsGrow = %v, want %v", got, tc.wantGrow)
			}
			if got := cpuSampleWantsShrink(tc.s); got != tc.wantShrink {
				t.Errorf("cpuSampleWantsShrink = %v, want %v", got, tc.wantShrink)
			}
		})
	}
}

// ── Evaluate behaviour tests ───────────────────────────────────────────────────

// TestCPUNoPSINoAction proves that Evaluate takes no action when
// CPUPSISupported is false, even when the pressure value looks high.
// This is the critical trap: an absent PSI reads as zero (idle), so acting on
// it would shrink a starving VM. The gate must fire first.
//
// Maps to: AR-GOV-AC5 in the ticket.
func TestCPUNoPSINoAction(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 4)
	g, a, fr := newCPUGovernorAndAxis(clk, 2, bounds)
	ctx := context.Background()

	// Inject a sample with CPUPSISupported=false and high-looking pressure.
	injectCPUSample(g, clk, noPSISample())
	a.Evaluate(ctx)
	if fr.callCount() != 0 {
		t.Fatalf("ResizeCPU called %d times; want 0 (PSI not supported)", fr.callCount())
	}

	// Also inject an idle-looking PSI-absent sample and verify no shrink,
	// even after advancing well past cpuShrinkWindow. The PSI gate fires before
	// any shrinkSince tracking is done, so time elapsed is irrelevant.
	clk.Advance(cpuShrinkWindow * 2)
	injectCPUSample(g, clk, resize.Sample{CPUPSISupported: false, CPUPSISomeAvg10: 0})
	for i := 0; i < 3; i++ {
		clk.Advance(cpuEvalInterval)
		injectCPUSample(g, clk, resize.Sample{CPUPSISupported: false, CPUPSISomeAvg10: 0})
		a.Evaluate(ctx)
	}
	if fr.callCount() != 0 {
		t.Fatalf("ResizeCPU called %d times on idle no-PSI sample (even past shrinkWindow); want 0", fr.callCount())
	}
}

// TestCPUEagerGrow proves that a single pressured sample immediately triggers
// a grow (cpuGrowConsecutive = 1). This is the eager-grow property.
func TestCPUEagerGrow(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 4)
	g, a, fr := newCPUGovernorAndAxis(clk, 2, bounds)
	ctx := context.Background()

	// One high-pressure sample → immediate grow.
	injectCPUSample(g, clk, pressuredSample())
	a.Evaluate(ctx)

	if fr.callCount() != 1 {
		t.Fatalf("ResizeCPU called %d times; want 1 (eager grow on single sample)", fr.callCount())
	}
	if fr.calls[0] != 3 {
		t.Errorf("ResizeCPU target = %d, want 3 (2+1)", fr.calls[0])
	}
}

// TestCPUShrinkRequiresSustainedIdle proves that shrink requires cpuShrinkWindow
// (= (cpuShrinkConsecutive-1) × cpuEvalInterval = 20 s) of continuous idle before
// firing. This replaces the old count-based test ("5 consecutive samples") with
// a time-based one that survives the adaptive poll cadence (loop.go:212-217
// drops to 2 s under memory pressure; count-based 5×2s = 10s ≠ 20s).
//
// Acceptance: advancing to cpuShrinkWindow-1s must NOT fire; advancing to
// cpuShrinkWindow must fire.
func TestCPUShrinkRequiresSustainedIdle(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 4)
	g, a, fr := newCPUGovernorAndAxis(clk, 3, bounds)
	ctx := context.Background()

	// First idle sample: sets shrinkSince = t=0. Must not fire (0s elapsed).
	injectCPUSample(g, clk, idleSample())
	a.Evaluate(ctx)
	if fr.callCount() != 0 {
		t.Fatalf("t=0: ResizeCPU called %d times; want 0 (0s < cpuShrinkWindow)", fr.callCount())
	}

	// Advance to just below cpuShrinkWindow: still must not fire.
	clk.Advance(cpuShrinkWindow - time.Second) // t = 24s
	injectCPUSample(g, clk, idleSample())
	a.Evaluate(ctx)
	if fr.callCount() != 0 {
		t.Fatalf("t=%v: ResizeCPU called %d times; want 0 (< cpuShrinkWindow)",
			cpuShrinkWindow-time.Second, fr.callCount())
	}

	// Advance past cpuShrinkWindow: shrink must now fire.
	clk.Advance(time.Second) // t = 25s = cpuShrinkWindow
	injectCPUSample(g, clk, idleSample())
	a.Evaluate(ctx)
	if fr.callCount() != 1 {
		t.Fatalf("t=%v: ResizeCPU called %d times; want 1 (>= cpuShrinkWindow)", cpuShrinkWindow, fr.callCount())
	}
	if fr.calls[0] != 2 {
		t.Errorf("ResizeCPU target = %d, want 2 (3-1)", fr.calls[0])
	}
}

// TestCPUShrinkWindowResetByDeadBand proves that a dead-band sample resets
// shrinkSince so the 20 s window restarts from zero. Without the reset, an
// interrupted idle run would carry stale timing into the next run.
func TestCPUShrinkWindowResetByDeadBand(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 4)
	g, a, fr := newCPUGovernorAndAxis(clk, 3, bounds)
	ctx := context.Background()

	// Idle run for 15s (< cpuShrinkWindow=20s): no fire.
	injectCPUSample(g, clk, idleSample()) // t=0: shrinkSince=0
	a.Evaluate(ctx)
	clk.Advance(15 * time.Second) // t=15
	injectCPUSample(g, clk, idleSample())
	a.Evaluate(ctx)
	if fr.callCount() != 0 {
		t.Fatalf("15s idle: want 0 calls, got %d", fr.callCount())
	}

	// Dead-band resets shrinkSince.
	clk.Advance(cpuEvalInterval) // t=20
	injectCPUSample(g, clk, deadBandSample())
	a.Evaluate(ctx)

	// Fresh idle run for 15s from the reset point: still < cpuShrinkWindow → no fire.
	clk.Advance(cpuEvalInterval) // t=25: shrinkSince=25
	injectCPUSample(g, clk, idleSample())
	a.Evaluate(ctx)
	clk.Advance(15 * time.Second) // t=40 → elapsed=15s < 20s
	injectCPUSample(g, clk, idleSample())
	a.Evaluate(ctx)
	if fr.callCount() != 0 {
		t.Fatalf("15s idle after window reset: want 0 calls, got %d (window must have restarted)", fr.callCount())
	}
}

// TestCPUGrowCooldown proves that a second grow is suppressed within
// cpuGrowCooldown after the first.
func TestCPUGrowCooldown(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 8)
	g, a, fr := newCPUGovernorAndAxis(clk, 2, bounds)
	ctx := context.Background()

	// First grow.
	injectCPUSample(g, clk, pressuredSample())
	a.Evaluate(ctx)
	if fr.callCount() != 1 {
		t.Fatalf("first Evaluate: ResizeCPU called %d times; want 1", fr.callCount())
	}

	// Immediately evaluate again — still in cooldown.
	injectCPUSample(g, clk, pressuredSample())
	a.Evaluate(ctx)
	if fr.callCount() != 1 {
		t.Fatalf("second Evaluate (in cooldown): ResizeCPU called %d times; want 1", fr.callCount())
	}

	// Advance clock past grow cooldown.
	clk.Advance(cpuGrowCooldown + time.Second)
	injectCPUSample(g, clk, pressuredSample())
	a.Evaluate(ctx)
	if fr.callCount() != 2 {
		t.Fatalf("after grow cooldown: ResizeCPU called %d times; want 2", fr.callCount())
	}
}

// TestCPUShrinkCooldown proves that a second shrink is suppressed within
// cpuShrinkCooldown after the first.
//
// The window-accumulation property is also verified: idle samples during the
// cooldown keep shrinkSince set, so when the cooldown expires the window is
// already satisfied and the next idle sample fires immediately.
func TestCPUShrinkCooldown(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 8)
	g, a, fr := newCPUGovernorAndAxis(clk, 4, bounds)
	ctx := context.Background()

	// Drive first shrink: inject idle at t=0 (sets shrinkSince), then advance
	// past cpuShrinkWindow so the next evaluation fires.
	injectCPUSample(g, clk, idleSample()) // t=0: shrinkSince=0
	a.Evaluate(ctx)
	clk.Advance(cpuShrinkWindow) // t=25s
	injectCPUSample(g, clk, idleSample())
	a.Evaluate(ctx) // fires: elapsed=25s >= cpuShrinkWindow
	if fr.callCount() != 1 {
		t.Fatalf("first shrink: ResizeCPU called %d times; want 1", fr.callCount())
	}

	// Inject an idle sample during cooldown: sets shrinkSince for the next cycle.
	clk.Advance(cpuEvalInterval) // t=30s (still in 120s cooldown)
	injectCPUSample(g, clk, idleSample())
	a.Evaluate(ctx) // in cooldown → suppressed, but shrinkSince is now set
	if fr.callCount() != 1 {
		t.Fatalf("in shrink cooldown: ResizeCPU called %d times; want 1", fr.callCount())
	}

	// Advance past shrink cooldown: shrinkSince was set during cooldown so the
	// window is already satisfied — the next idle sample fires immediately.
	clk.Advance(cpuShrinkCooldown + time.Second) // t >> 120s, elapsed from shrinkSince >> 25s
	injectCPUSample(g, clk, idleSample())
	a.Evaluate(ctx)
	if fr.callCount() != 2 {
		t.Fatalf("after shrink cooldown: ResizeCPU called %d times; want 2", fr.callCount())
	}
}

// TestCPUNoCriticalBypass proves that the CPU axis has no urgent/critical
// cooldown bypass. Unlike the memory axis, a vCPU shortage is survivable.
// The grow cooldown must be honoured even under maximum pressure.
//
// Source: OLD cpu_resize.go:242-249.
func TestCPUNoCriticalBypass(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 8)
	g, a, fr := newCPUGovernorAndAxis(clk, 2, bounds)
	ctx := context.Background()

	// First grow fires.
	injectCPUSample(g, clk, pressuredSample())
	a.Evaluate(ctx)
	if fr.callCount() != 1 {
		t.Fatalf("initial grow: want 1 call, got %d", fr.callCount())
	}

	// Subsequent evaluations with maximum pressure must NOT bypass cooldown.
	for i := 0; i < 10; i++ {
		injectCPUSample(g, clk, pressuredSample())
		a.Evaluate(ctx)
	}
	if fr.callCount() != 1 {
		t.Fatalf("no critical bypass: want 1 call, got %d (cooldown must not be bypassed)", fr.callCount())
	}
}

// TestCPUAtMaxVCPUs proves that Evaluate does not call ResizeCPU when already
// at VCPUMax, and resets the grow counter.
func TestCPUAtMaxVCPUs(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 2) // at max
	g, a, fr := newCPUGovernorAndAxis(clk, 2, bounds)
	ctx := context.Background()

	injectCPUSample(g, clk, pressuredSample())
	a.Evaluate(ctx)
	if fr.callCount() != 0 {
		t.Fatalf("at VCPUMax: ResizeCPU called %d times; want 0", fr.callCount())
	}
}

// TestCPUAtMinVCPUs proves that Evaluate does not call ResizeCPU when already
// at VCPUMin.
func TestCPUAtMinVCPUs(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 4)
	g, a, fr := newCPUGovernorAndAxis(clk, 1, bounds) // already at min
	ctx := context.Background()

	for i := 0; i < cpuShrinkConsecutive; i++ {
		injectCPUSample(g, clk, idleSample())
		a.Evaluate(ctx)
	}
	if fr.callCount() != 0 {
		t.Fatalf("at VCPUMin: ResizeCPU called %d times; want 0", fr.callCount())
	}
}

// TestCPUNoBoundsPassive proves that Evaluate takes no action when VCPUMin ==
// VCPUMax (bounds not configured for CPU).
func TestCPUNoBoundsPassive(t *testing.T) {
	clk := newFakeClock()
	// VCPUMin == VCPUMax → passive
	bounds := resize.Bounds{VCPUMin: 2, VCPUMax: 2}
	g, a, fr := newCPUGovernorAndAxis(clk, 2, bounds)
	ctx := context.Background()

	injectCPUSample(g, clk, pressuredSample())
	a.Evaluate(ctx)
	if fr.callCount() != 0 {
		t.Fatalf("unconfigured CPU bounds: ResizeCPU called %d times; want 0", fr.callCount())
	}
}

// TestCPUVCPUOnlineDriftReconcile proves that when VCPUOnline from the guest
// differs from the axis's currentVCPUs, the axis reconciles to the guest's
// ground truth and uses it as the base for the next resize.
func TestCPUVCPUOnlineDriftReconcile(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 8)
	g, a, fr := newCPUGovernorAndAxis(clk, 4, bounds)
	ctx := context.Background()

	// Guest reports only 2 CPUs online (partial hotplug failure).
	driftSample := pressuredSample()
	driftSample.VCPUOnline = 2
	injectCPUSample(g, clk, driftSample)
	a.Evaluate(ctx)

	// Axis should have reconciled to 2 and grown by 1 → target 3.
	if fr.callCount() != 1 {
		t.Fatalf("ResizeCPU called %d times; want 1", fr.callCount())
	}
	if fr.calls[0] != 3 {
		t.Errorf("ResizeCPU target = %d, want 3 (reconciled from drift: 2+1)", fr.calls[0])
	}
}

// TestCPUResizeErrorCooldownFires proves that even when ResizeCPU returns an
// error, the cooldown timer is set so the axis doesn't hammer a failing resizer.
func TestCPUResizeErrorCooldownFires(t *testing.T) {
	clk := newFakeClock()
	bounds := cpuBounds(1, 4)
	g, a, fr := newCPUGovernorAndAxis(clk, 2, bounds)
	fr.resizeErr = errFakeResizeFailure
	ctx := context.Background()

	// First Evaluate — resize will fail but cooldown should be set.
	injectCPUSample(g, clk, pressuredSample())
	a.Evaluate(ctx)

	// Second Evaluate immediately — cooldown must suppress the second call.
	injectCPUSample(g, clk, pressuredSample())
	a.Evaluate(ctx)

	if len(fr.calls) != 0 {
		t.Fatalf("ResizeCPU calls = %d; want 0 (errors don't produce successful calls)", len(fr.calls))
	}
	// The axis must have attempted exactly 1 call (and been blocked on second).
	// We can't inspect internal attempt count directly, so verify via axis state:
	// lastResizeTime must be set.
	if a.lastResizeTime.IsZero() {
		t.Error("lastResizeTime is zero; cooldown should have been set after error")
	}
}

// errFakeResizeFailure is a sentinel error for TestCPUResizeErrorCooldownFires.
var errFakeResizeFailure = errCPUResizeFail("cpu resize failed")

type errCPUResizeFail string

func (e errCPUResizeFail) Error() string { return string(e) }
