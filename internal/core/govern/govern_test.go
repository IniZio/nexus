package govern

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

// fakeClock is an injected Clock whose wall time is controlled by the test.
// After() registers a pending timer; Advance() fires any timers whose deadline
// has passed. Not safe for concurrent use — tests drive it from a single
// goroutine.
type fakeClock struct {
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{deadline: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock forward by d and fires any pending timers.
func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
	remaining := c.timers[:0]
	for _, t := range c.timers {
		if !c.now.Before(t.deadline) {
			t.ch <- c.now
		} else {
			remaining = append(remaining, t)
		}
	}
	c.timers = remaining
}

// fakeResizer records ResizeMemory calls and returns the target as the new
// current value (simulates a successful resize).
type fakeResizer struct {
	current   int64
	calls     []int64 // ordered list of targets passed to ResizeMemory
	resizeErr error   // if non-nil, ResizeMemory returns this error
}

func newFakeResizer(bootBytes int64) *fakeResizer {
	return &fakeResizer{current: bootBytes}
}

func (r *fakeResizer) ResizeMemory(_ context.Context, targetBytes int64) (int64, error) {
	if r.resizeErr != nil {
		return r.current, r.resizeErr
	}
	r.current = targetBytes
	r.calls = append(r.calls, targetBytes)
	return targetBytes, nil
}

func (r *fakeResizer) CurrentMemoryBytes() int64 { return r.current }

// fakeTelemetry serves a fixed sample (or error) from Poll.
type fakeTelemetry struct {
	sample resize.Sample
	err    error
}

func (f *fakeTelemetry) Poll(_ context.Context) (resize.Sample, error) {
	return f.sample, f.err
}

// fakeHeadroom allows tests to control whether the headroom check passes.
type fakeHeadroom struct {
	ok  bool
	err error
}

func (h *fakeHeadroom) HasHeadroom(_ context.Context, _ int64) (bool, error) {
	return h.ok, h.err
}

// ── Sample helpers ─────────────────────────────────────────────────────────────

const gib = 1024 * 1024 * 1024

// healthySample: 30% available — between shrink (45%) and grow (20%) thresholds.
// PSI zeroed; governor should take no action.
func healthySample(total uint64) resize.Sample {
	return resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     total,
		MemAvailableBytes: uint64(float64(total) * 0.30),
		MemPSISupported:   true,
	}
}

// growSample: 10% available — below the grow threshold (20%).
func growSample(total uint64) resize.Sample {
	return resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     total,
		MemAvailableBytes: uint64(float64(total) * 0.10),
		MemPSISupported:   true,
	}
}

// shrinkSample: 60% available — above the shrink threshold (45%).
func shrinkSample(total uint64) resize.Sample {
	return resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     total,
		MemAvailableBytes: uint64(float64(total) * 0.60),
		MemPSISupported:   true,
	}
}

// psiGrowSample: plenty of MemAvailable but high PSI pressure.
// Available is 40% (above grow threshold) but some_avg10 is 15 (> psiGrowPressure=10).
func psiGrowSample(total uint64) resize.Sample {
	return resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     total,
		MemAvailableBytes: uint64(float64(total) * 0.40),
		MemPSISupported:   true,
		MemPSISomeAvg10:   15.0,
	}
}

// criticalPSISample: PSI full avg is above psiCriticalPressure (10).
// Triggers the cooldown bypass (bypass 1).
func criticalPSISample(total uint64) resize.Sample {
	return resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     total,
		MemAvailableBytes: uint64(float64(total) * 0.10),
		MemPSISupported:   true,
		MemPSIFullAvg10:   15.0, // > psiCriticalPressure
	}
}

// criticalAvailSample: MemAvailable below criticalGrowThreshold (0.08).
// Triggers the cooldown bypass (bypass 2).
func criticalAvailSample(total uint64) resize.Sample {
	return resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     total,
		MemAvailableBytes: uint64(float64(total) * 0.05), // 5% < 8%
		MemPSISupported:   true,
	}
}

// ── Helpers to build test governors ───────────────────────────────────────────

// newTestGovernorMinMax constructs a governor with explicit min/max bounds.
// resizer and headroom may be nil; nil resizer panics (callers must supply one).
func newTestGovernorMinMax(t *testing.T, minBytes, maxBytes int64, resizer *fakeResizer, headroom HostHeadroomReader) (*Governor, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	if headroom == nil {
		headroom = &fakeHeadroom{ok: true}
	}
	g := New(Config{
		Resizer:   resizer,
		Telemetry: &fakeTelemetry{},
		Headroom:  headroom,
		Bounds: resize.Bounds{
			MemMinBytes: minBytes,
			MemMaxBytes: maxBytes,
		},
		Clock: clk,
	})
	return g, clk
}

// newTestGovernor is a convenience wrapper where min = bootBytes, max = bootBytes*4.
// Suitable for grow tests where current starts at the minimum.
func newTestGovernor(t *testing.T, bootBytes int64, resizer *fakeResizer, headroom HostHeadroomReader) (*Governor, *fakeClock) {
	t.Helper()
	if resizer == nil {
		resizer = newFakeResizer(bootBytes)
	}
	return newTestGovernorMinMax(t, bootBytes, bootBytes*4, resizer, headroom)
}

// injectSample plants a fresh sample directly into g and updates lastSampleTime.
func injectSample(g *Governor, clk *fakeClock, s resize.Sample) {
	s.Timestamp = clk.Now()
	g.latest = s
	g.lastSampleTime = clk.Now()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestMemoryControlLawConstants is the anti-regression guard.
// It fails loudly if any constant drifts back to a prior-incorrect value.
// Specifically: grow threshold MUST be 0.20 (not 0.15) and grow consecutive
// MUST be 1 (not 2). The corrected values are documented in D-DC-23 and
// verified against OLD memory_resize.go:47-81.
func TestMemoryControlLawConstants(t *testing.T) {
	t.Parallel()
	if defaultGrowThreshold != 0.20 {
		t.Errorf("defaultGrowThreshold = %v, want 0.20 (not 0.15 — see D-DC-23)", defaultGrowThreshold)
	}
	if memoryGrowConsecutive != 1 {
		t.Errorf("memoryGrowConsecutive = %d, want 1 (not 2 — eager; see D-DC-23)", memoryGrowConsecutive)
	}
	if criticalGrowThreshold != 0.08 {
		t.Errorf("criticalGrowThreshold = %v, want 0.08", criticalGrowThreshold)
	}
	if memoryShrinkConsecutive != 5 {
		t.Errorf("memoryShrinkConsecutive = %d, want 5", memoryShrinkConsecutive)
	}
	if memoryGrowCooldown != 60*time.Second {
		t.Errorf("memoryGrowCooldown = %v, want 60s", memoryGrowCooldown)
	}
	if memoryShrinkCooldown != 120*time.Second {
		t.Errorf("memoryShrinkCooldown = %v, want 120s", memoryShrinkCooldown)
	}
	if defaultShrinkThreshold != 0.45 {
		t.Errorf("defaultShrinkThreshold = %v, want 0.45", defaultShrinkThreshold)
	}
	if psiGrowPressure != 10.0 {
		t.Errorf("psiGrowPressure = %v, want 10.0", psiGrowPressure)
	}
	if psiCriticalPressure != 10.0 {
		t.Errorf("psiCriticalPressure = %v, want 10.0", psiCriticalPressure)
	}
	if minGrowStepBytes != 256*1024*1024 {
		t.Errorf("minGrowStepBytes = %d, want 256 MiB", minGrowStepBytes)
	}
	if minShrinkStepBytes != 512*1024*1024 {
		t.Errorf("minShrinkStepBytes = %d, want 512 MiB", minShrinkStepBytes)
	}
}

// TestSampleSignals verifies sampleWantsGrow / sampleWantsShrink / sampleIsCritical
// for the canonical signal combinations.
func TestSampleSignals(t *testing.T) {
	t.Parallel()
	total := uint64(4 * gib)

	t.Run("grow: MemAvailable below threshold", func(t *testing.T) {
		t.Parallel()
		if !sampleWantsGrow(growSample(total)) {
			t.Error("10% available should wantsGrow")
		}
	})
	t.Run("grow: PSI pressure even with high MemAvailable", func(t *testing.T) {
		t.Parallel()
		if !sampleWantsGrow(psiGrowSample(total)) {
			t.Error("high PSI some_avg10 should wantsGrow regardless of MemAvailable")
		}
	})
	t.Run("no grow: PSI absent, zero treated correctly", func(t *testing.T) {
		t.Parallel()
		// PSISupported=false: a zero PSISomeAvg10 must NOT be treated as healthy.
		// The ratio check still applies.
		noPSI := resize.Sample{
			Timestamp:         time.Now(),
			MemTotalBytes:     total,
			MemAvailableBytes: uint64(float64(total) * 0.10), // 10% — below grow threshold
			MemPSISupported:   false,
			MemPSISomeAvg10:   0, // zero must not be treated as "healthy"
		}
		if !sampleWantsGrow(noPSI) {
			t.Error("PSI absent, low MemAvailable: should still wantsGrow via ratio")
		}
	})
	t.Run("no grow: PSI absent, high MemAvailable, zero PSI", func(t *testing.T) {
		t.Parallel()
		noPSI := resize.Sample{
			Timestamp:         time.Now(),
			MemTotalBytes:     total,
			MemAvailableBytes: uint64(float64(total) * 0.50), // above shrink threshold
			MemPSISupported:   false,
			MemPSISomeAvg10:   0,
		}
		if sampleWantsGrow(noPSI) {
			t.Error("high MemAvailable + PSI absent: should NOT wantsGrow")
		}
	})
	t.Run("shrink: high MemAvailable, no PSI pressure", func(t *testing.T) {
		t.Parallel()
		if !sampleWantsShrink(shrinkSample(total)) {
			t.Error("60% available should wantsShrink")
		}
	})
	t.Run("no shrink: PSI pressure blocks shrink", func(t *testing.T) {
		t.Parallel()
		if sampleWantsShrink(psiGrowSample(total)) {
			t.Error("high PSI should block shrink even with high MemAvailable")
		}
	})
	t.Run("critical: PSI full above threshold (bypass 1)", func(t *testing.T) {
		t.Parallel()
		if !sampleIsCritical(criticalPSISample(total)) {
			t.Error("PSI full_avg10=15 should be critical (bypass 1)")
		}
	})
	t.Run("critical: MemAvailable below 8% (bypass 2)", func(t *testing.T) {
		t.Parallel()
		if !sampleIsCritical(criticalAvailSample(total)) {
			t.Error("5% MemAvailable should be critical (bypass 2)")
		}
	})
	t.Run("not critical: PSI absent, high MemAvail", func(t *testing.T) {
		t.Parallel()
		if sampleIsCritical(healthySample(total)) {
			t.Error("healthy sample should not be critical")
		}
	})
	t.Run("PSI full ignored when PSI unsupported (bypass 1 gate)", func(t *testing.T) {
		t.Parallel()
		// PSI full=15 but PSISupported=false — must not trigger bypass 1.
		// Only bypass 2 (MemAvailable ratio) should apply.
		s := resize.Sample{
			Timestamp:         time.Now(),
			MemTotalBytes:     total,
			MemAvailableBytes: uint64(float64(total) * 0.30), // 30%, above critical 8%
			MemPSISupported:   false,
			MemPSIFullAvg10:   15.0, // irrelevant when PSI unsupported
		}
		if sampleIsCritical(s) {
			t.Error("PSI full should be ignored when MemPSISupported=false")
		}
	})
}

// TestGrowEager verifies that a single grow-wanting sample triggers ResizeMemory
// (memoryGrowConsecutive = 1).
func TestGrowEager(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newFakeResizer(boot)
	g, clk := newTestGovernor(t, boot, resizer, nil)

	injectSample(g, clk, growSample(uint64(boot)))
	g.evaluate(context.Background())

	if len(resizer.calls) != 1 {
		t.Fatalf("expected 1 ResizeMemory call, got %d", len(resizer.calls))
	}
	if resizer.calls[0] <= boot {
		t.Errorf("grow target %d should be > boot %d", resizer.calls[0], boot)
	}
}

// TestGrowOnlyCoolsDown verifies that a second grow is blocked by the 60s
// cooldown after the first.
func TestGrowCooldown(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newFakeResizer(boot)
	g, clk := newTestGovernor(t, boot, resizer, nil)

	// First grow.
	injectSample(g, clk, growSample(uint64(g.bounds.MemMaxBytes)))
	g.evaluate(context.Background())
	if len(resizer.calls) != 1 {
		t.Fatalf("expected first grow, got %d calls", len(resizer.calls))
	}

	// Second sample immediately — within cooldown.
	injectSample(g, clk, growSample(uint64(g.bounds.MemMaxBytes)))
	g.evaluate(context.Background())
	if len(resizer.calls) != 1 {
		t.Errorf("second grow within cooldown should be suppressed; calls=%d", len(resizer.calls))
	}

	// Advance past the 60s grow cooldown.
	clk.Advance(memoryGrowCooldown + time.Second)
	injectSample(g, clk, growSample(uint64(g.bounds.MemMaxBytes)))
	g.evaluate(context.Background())
	if len(resizer.calls) != 2 {
		t.Errorf("grow after cooldown expired should fire; calls=%d", len(resizer.calls))
	}
}

// TestShrinkRequiresFive verifies that exactly 5 consecutive shrink-wanting
// samples are needed before a shrink fires (memoryShrinkConsecutive = 5).
func TestShrinkRequiresFive(t *testing.T) {
	t.Parallel()
	// current must be above minBytes so the shrink floor check does not suppress
	// the action. Use bootBytes as current but set minBytes to half that.
	const boot = 4 * gib
	resizer := newFakeResizer(boot) // current = boot = 4 GiB
	g, clk := newTestGovernorMinMax(t, boot/2, boot*4, resizer, nil)

	for i := 1; i <= 4; i++ {
		injectSample(g, clk, shrinkSample(uint64(boot)))
		g.evaluate(context.Background())
		if len(resizer.calls) != 0 {
			t.Fatalf("shrink sample %d/%d should not have triggered; calls=%v",
				i, memoryShrinkConsecutive, resizer.calls)
		}
	}

	// Fifth sample triggers the shrink.
	injectSample(g, clk, shrinkSample(uint64(boot)))
	g.evaluate(context.Background())
	if len(resizer.calls) != 1 {
		t.Fatalf("fifth shrink sample should have triggered; calls=%v", resizer.calls)
	}
	if resizer.calls[0] >= boot {
		t.Errorf("shrink target %d should be < boot %d", resizer.calls[0], boot)
	}
}

// TestShrinkCounterResets verifies that a non-shrink sample resets the
// consecutive counter so the shrink requires a fresh run of 5.
func TestShrinkCounterResets(t *testing.T) {
	t.Parallel()
	const boot = 4 * gib
	resizer := newFakeResizer(boot)
	g, clk := newTestGovernorMinMax(t, boot/2, boot*4, resizer, nil)

	// Three shrink samples.
	for i := 0; i < 3; i++ {
		injectSample(g, clk, shrinkSample(uint64(boot)))
		g.evaluate(context.Background())
	}

	// One healthy sample resets the counter.
	injectSample(g, clk, healthySample(uint64(boot)))
	g.evaluate(context.Background())

	// Four more shrink samples — not enough (need 5 fresh).
	for i := 0; i < 4; i++ {
		injectSample(g, clk, shrinkSample(uint64(boot)))
		g.evaluate(context.Background())
	}
	if len(resizer.calls) != 0 {
		t.Errorf("shrink counter should have been reset; calls=%v", resizer.calls)
	}
}

// TestCriticalPSIBypassesCooldown verifies that PSI full_avg10 above
// psiCriticalPressure skips the inter-grow cooldown (bypass 1).
func TestCriticalPSIBypassesCooldown(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newFakeResizer(boot)
	g, clk := newTestGovernor(t, boot, resizer, nil)

	// First grow.
	injectSample(g, clk, growSample(uint64(boot)))
	g.evaluate(context.Background())
	if len(resizer.calls) != 1 {
		t.Fatalf("expected first grow")
	}

	// Second sample within cooldown but CRITICAL (PSI full bypass).
	injectSample(g, clk, criticalPSISample(uint64(boot)))
	g.evaluate(context.Background())
	if len(resizer.calls) != 2 {
		t.Errorf("critical PSI full should bypass cooldown; calls=%d", len(resizer.calls))
	}
}

// TestCriticalAvailBypassesCooldown verifies that MemAvailable < 0.08
// skips the inter-grow cooldown (bypass 2).
func TestCriticalAvailBypassesCooldown(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newFakeResizer(boot)
	g, clk := newTestGovernor(t, boot, resizer, nil)

	// First grow.
	injectSample(g, clk, growSample(uint64(boot)))
	g.evaluate(context.Background())
	if len(resizer.calls) != 1 {
		t.Fatalf("expected first grow")
	}

	// Second sample within cooldown but CRITICAL (MemAvailable bypass).
	injectSample(g, clk, criticalAvailSample(uint64(boot)))
	g.evaluate(context.Background())
	if len(resizer.calls) != 2 {
		t.Errorf("critical MemAvailable should bypass cooldown; calls=%d", len(resizer.calls))
	}
}

// TestHostHeadroomRefusesGrow verifies that when the headroom check returns
// ok=false, the grow is refused even when the memory control law says grow.
func TestHostHeadroomRefusesGrow(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newFakeResizer(boot)
	g, clk := newTestGovernor(t, boot, resizer, &fakeHeadroom{ok: false})

	injectSample(g, clk, growSample(uint64(boot)))
	g.evaluate(context.Background())

	if len(resizer.calls) != 0 {
		t.Errorf("grow should be refused when headroom is insufficient; calls=%v", resizer.calls)
	}
}

// TestHostHeadroomErrorIsConservative verifies that when the headroom check
// returns an error, the grow is refused (fail conservative, not fail open).
// This is the key safety behaviour for HOST OOM prevention.
func TestHostHeadroomErrorIsConservative(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newFakeResizer(boot)
	g, clk := newTestGovernor(t, boot, resizer, &fakeHeadroom{err: errors.New("procfs error")})

	injectSample(g, clk, growSample(uint64(boot)))
	g.evaluate(context.Background())

	if len(resizer.calls) != 0 {
		t.Errorf("grow should be refused when headroom check errors (conservative); calls=%v", resizer.calls)
	}
}

// TestHeadroomNotCheckedOnShrink verifies that shrink does not invoke the
// headroom check (only grows need admission control).
func TestHeadroomNotCheckedOnShrink(t *testing.T) {
	t.Parallel()
	const boot = 4 * gib
	resizer := newFakeResizer(boot)
	// Headroom returns error — if shrink checked it, the shrink would be refused.
	g, clk := newTestGovernorMinMax(t, boot/2, boot*4, resizer, &fakeHeadroom{err: errors.New("should not be called")})

	for i := 0; i < memoryShrinkConsecutive; i++ {
		injectSample(g, clk, shrinkSample(uint64(boot)))
		g.evaluate(context.Background())
	}
	if len(resizer.calls) != 1 {
		t.Errorf("shrink should not be gated by headroom check; calls=%v", resizer.calls)
	}
}

// TestStaleSampleBlocked verifies that a sample whose Timestamp is older than
// SampleMaxAge is rejected and does not trigger a resize.
func TestStaleSampleBlocked(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newFakeResizer(boot)
	g, clk := newTestGovernor(t, boot, resizer, nil)

	// Plant a sample at clock.Now() but advance the clock past SampleMaxAge.
	injectSample(g, clk, growSample(uint64(boot)))
	clk.Advance(resize.SampleMaxAge + time.Second)

	// lastSampleTime is still old, so evaluate should bail out.
	g.evaluate(context.Background())

	if len(resizer.calls) != 0 {
		t.Errorf("stale sample should be blocked; calls=%v", resizer.calls)
	}
}

// nopollTelemetry fails the test if Poll is ever called. Used by
// TestBoundsPassive to prove that unconfigured bounds produce zero polls — the
// coverage gap that let B2 through (old test called evaluate() directly so
// passive-mode polling was never exercised).
type nopollTelemetry struct{ t *testing.T }

func (n *nopollTelemetry) Poll(_ context.Context) (resize.Sample, error) {
	n.t.Error("Poll called in passive mode (unconfigured bounds): B2 regression")
	return resize.Sample{}, nil
}

// TestBoundsPassive verifies that the governor does nothing when bounds are
// zero or invalid (passive mode, no auto-resize configured).
func TestBoundsPassive(t *testing.T) {
	t.Parallel()
	resizer := newFakeResizer(2 * gib)
	// nopollTelemetry fails if Poll is ever called — proves Run exits without
	// polling when bounds are unconfigured (B2: the old test called evaluate()
	// directly and never exercised Run's poll loop, letting B2 slip through).
	g := New(Config{
		Resizer:   resizer,
		Telemetry: &nopollTelemetry{t: t},
		Headroom:  &fakeHeadroom{ok: true},
		Bounds:    resize.Bounds{}, // unconfigured → Run must return immediately
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		g.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Pass: Run exited without polling.
	case <-time.After(time.Second):
		t.Error("Run blocked despite unconfigured bounds; expected immediate return")
	}
	if len(resizer.calls) != 0 {
		t.Errorf("zero bounds should suppress all resizes; calls=%v", resizer.calls)
	}
}

// TestGrowStep verifies the deficit-scaled grow step calculation.
func TestGrowStep(t *testing.T) {
	t.Parallel()
	total := uint64(4 * gib)
	// MemAvailable = 10%: deficit to reach shrinkThreshold (45%) = 35% of total.
	// step = max(deficit, minGrowStepBytes) capped at delta/2.
	s := growSample(total)
	delta := int64(total) * 3 // max - min = 4GiB * 4 - 4GiB = 12GiB? let's use simple bounds
	_ = delta

	step := growStep(int64(total), int64(total)*4, int64(total), s)
	if step < minGrowStepBytes {
		t.Errorf("grow step %d should be >= minGrowStepBytes %d", step, minGrowStepBytes)
	}
	if step > (int64(total)*4-int64(total))/2 {
		t.Errorf("grow step %d exceeds half-range cap", step)
	}
}

// TestShrinkStep verifies that shrinkStep always returns at least minShrinkStepBytes.
func TestShrinkStep(t *testing.T) {
	t.Parallel()
	const minBytes = 1 * gib
	const maxBytes = 4 * gib
	step := shrinkStep(minBytes, maxBytes)
	if step < minShrinkStepBytes {
		t.Errorf("shrink step %d should be >= minShrinkStepBytes %d", step, minShrinkStepBytes)
	}
}

// TestHostMemAvailFloor verifies the dynamic floor formula.
func TestHostMemAvailFloor(t *testing.T) {
	t.Parallel()
	// 5% of 40 GiB = 2 GiB > 1 GiB hard floor.
	if got := hostMemAvailFloor(40 * gib); got != int64(40*gib)*5/100 {
		t.Errorf("40 GiB host: floor = %d, want %d", got, int64(40*gib)*5/100)
	}
	// 5% of 8 GiB = 409 MiB < 1 GiB hard floor → floor = 1 GiB.
	if got := hostMemAvailFloor(8 * gib); got != hostMemAvailFloorMin {
		t.Errorf("8 GiB host: floor = %d, want %d (hard min)", got, hostMemAvailFloorMin)
	}
}

// TestReadMeminfo verifies the /proc/meminfo parser with synthetic content.
func TestReadMeminfo(t *testing.T) {
	t.Parallel()
	// Write a synthetic meminfo file.
	tmp := t.TempDir()
	path := tmp + "/meminfo"
	content := "MemTotal:       16384000 kB\nMemFree:        4096000 kB\nMemAvailable:   8192000 kB\nBuffers:        1024000 kB\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	avail, total, err := readMeminfo(path)
	if err != nil {
		t.Fatalf("readMeminfo: %v", err)
	}
	const wantTotal = 16384000 * 1024
	const wantAvail = 8192000 * 1024
	if total != wantTotal {
		t.Errorf("MemTotal = %d, want %d", total, wantTotal)
	}
	if avail != wantAvail {
		t.Errorf("MemAvailable = %d, want %d", avail, wantAvail)
	}
}

// TestGrowTargetAligned is the regression guard for the CH hotplug-alignment
// bug: growStep is deficit-scaled and produces arbitrary byte targets, which CH
// rejects with HTTP 500 unless they are a multiple of memHotplugAlignBytes.
//
// Bounds min=512 MiB, max=4 GiB (both block-aligned), current=512 MiB. With a
// growSample over a 1 GiB total the deficit-scaled raw target is 912680550 B
// (~870.4 MiB) — NOT a 256 MiB multiple. Before the fix evaluate issued that
// value verbatim and CH 500'd; after the fix it must be rounded UP to the next
// block multiple (1073741824 = 1 GiB) and stay > current.
//
// Fails before the fix (912680550 % 256 MiB != 0); passes after.
func TestGrowTargetAligned(t *testing.T) {
	t.Parallel()
	const minBytes int64 = 512 * 1024 * 1024
	const maxBytes int64 = 4 * 1024 * 1024 * 1024
	resizer := newFakeResizer(minBytes)
	g, clk := newTestGovernorMinMax(t, minBytes, maxBytes, resizer, nil)

	injectSample(g, clk, growSample(uint64(gib))) // 1 GiB total
	g.evaluate(context.Background())

	if len(resizer.calls) != 1 {
		t.Fatalf("expected 1 ResizeMemory call, got %d (%v)", len(resizer.calls), resizer.calls)
	}
	got := resizer.calls[0]
	if got%memHotplugAlignBytes != 0 {
		t.Errorf("grow target %d is not a multiple of memHotplugAlignBytes %d (CH would reject with HTTP 500)",
			got, memHotplugAlignBytes)
	}
	if got <= minBytes {
		t.Errorf("grow target %d must make forward progress above current %d", got, minBytes)
	}
	// Worked example pinned so the rounding direction cannot silently regress:
	// raw deficit target 912680550 must round UP to 1 GiB, never stay unaligned.
	const wantAligned int64 = 1073741824 // 1 GiB, the next 256 MiB multiple
	if got != wantAligned {
		t.Errorf("grow target = %d, want %d (raw 912680550 rounded up to the next 256 MiB block)", got, wantAligned)
	}
}

// TestShrinkTargetAligned is the shrink-side regression guard. With an unaligned
// current (5.1 GiB) the shrink target current-shrinkStep is unaligned; evaluate
// must round it DOWN to a block multiple that stays < current and >= min.
//
// Fails before the fix (unaligned target 1447034880 % 256 MiB != 0); passes after.
func TestShrinkTargetAligned(t *testing.T) {
	t.Parallel()
	const minBytes int64 = 512 * 1024 * 1024
	const maxBytes int64 = 8 * 1024 * 1024 * 1024
	// current = 5 GiB + 100 MiB: deliberately NOT a 256 MiB multiple.
	current := int64(5*gib) + 100*1024*1024
	resizer := newFakeResizer(current)
	g, clk := newTestGovernorMinMax(t, minBytes, maxBytes, resizer, nil)

	for i := 0; i < memoryShrinkConsecutive; i++ {
		injectSample(g, clk, shrinkSample(uint64(maxBytes)))
		g.evaluate(context.Background())
	}

	if len(resizer.calls) != 1 {
		t.Fatalf("expected 1 ResizeMemory call after %d shrink samples, got %d (%v)",
			memoryShrinkConsecutive, len(resizer.calls), resizer.calls)
	}
	got := resizer.calls[0]
	if got%memHotplugAlignBytes != 0 {
		t.Errorf("shrink target %d is not a multiple of memHotplugAlignBytes %d (CH would reject with HTTP 500)",
			got, memHotplugAlignBytes)
	}
	if got >= current {
		t.Errorf("shrink target %d must be strictly below current %d", got, current)
	}
	if got < minBytes {
		t.Errorf("shrink target %d must not underflow the floor %d", got, minBytes)
	}
	// Worked example: raw target 1447034880 rounds DOWN to 1342177280 (1.25 GiB).
	const wantAligned int64 = 1342177280
	if got != wantAligned {
		t.Errorf("shrink target = %d, want %d (raw 1447034880 rounded down to a 256 MiB block)", got, wantAligned)
	}
}

// ── Jump-to-safe-floor tests (D-DC-?? cold-start OOM fix) ────────────────────

// criticalGrowSample returns a sample that is BOTH wantsGrow AND critical via
// MemAvailable < criticalGrowThreshold (0.08). Available = 5% of total.
func criticalGrowSample(total uint64) resize.Sample {
	return resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     total,
		MemAvailableBytes: uint64(float64(total) * 0.05), // 5% < 8% → critical
		MemPSISupported:   true,
	}
}

// TestCriticalColdStart_JumpsToSafeFloor: from a 512 MiB boot, a single
// critical sample must produce a target of safeMemFloorBytes (2 GiB) in ONE
// evaluate() call — not the 256 MiB deficit step.
func TestCriticalColdStart_JumpsToSafeFloor(t *testing.T) {
	t.Parallel()
	const boot int64 = 512 * 1024 * 1024

	cases := []struct {
		name    string
		maxBytes int64
		want    int64
	}{
		{"max_4GiB", 4 * gib, safeMemFloorBytes},
		{"max_8GiB", 8 * gib, safeMemFloorBytes},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resizer := newFakeResizer(boot)
			g, clk := newTestGovernorMinMax(t, boot, tc.maxBytes, resizer, nil)

			injectSample(g, clk, criticalGrowSample(uint64(boot)))
			g.evaluate(context.Background())

			if len(resizer.calls) != 1 {
				t.Fatalf("expected 1 ResizeMemory call, got %d (%v)", len(resizer.calls), resizer.calls)
			}
			got := resizer.calls[0]
			if got != tc.want {
				t.Errorf("jump-to-floor target = %d (%d MiB), want %d (%d MiB)",
					got, got>>20, tc.want, tc.want>>20)
			}
			if got%memHotplugAlignBytes != 0 {
				t.Errorf("target %d is not a multiple of memHotplugAlignBytes %d", got, memHotplugAlignBytes)
			}
		})
	}
}

// TestCriticalPostShrink_JumpsToSafeFloor: lastResizeTime is NON-zero (the
// governor already shrank back to boot RAM), but a fresh critical sample must
// still jump to safeMemFloorBytes — proves the branch is NOT gated on
// "first-ever grow only".
func TestCriticalPostShrink_JumpsToSafeFloor(t *testing.T) {
	t.Parallel()
	const boot int64 = 512 * 1024 * 1024

	resizer := newFakeResizer(boot)
	g, clk := newTestGovernorMinMax(t, boot, 4*gib, resizer, nil)

	// Simulate a prior shrink: set lastResizeTime to "just now" (within cooldown)
	// and lastResizeWasShrink=true. Without the critical bypass the cooldown
	// (memoryShrinkCooldown = 120s) would block the next grow.
	g.lastResizeTime = clk.Now()
	g.lastResizeWasShrink = true

	injectSample(g, clk, criticalGrowSample(uint64(boot)))
	g.evaluate(context.Background())

	if len(resizer.calls) != 1 {
		t.Fatalf("post-shrink critical: expected 1 ResizeMemory call, got %d (%v)", len(resizer.calls), resizer.calls)
	}
	got := resizer.calls[0]
	if got != safeMemFloorBytes {
		t.Errorf("post-shrink critical jump = %d (%d MiB), want safeMemFloorBytes %d (%d MiB)",
			got, got>>20, safeMemFloorBytes, safeMemFloorBytes>>20)
	}
}

// TestCriticalJump_ClampedByLowMax: when maxBytes < safeMemFloorBytes the
// jump must clamp to maxBytes, never exceed it.
func TestCriticalJump_ClampedByLowMax(t *testing.T) {
	t.Parallel()
	const boot int64 = 512 * 1024 * 1024
	const maxBytes int64 = 1 * gib // 1 GiB < 2 GiB safeMemFloorBytes

	resizer := newFakeResizer(boot)
	g, clk := newTestGovernorMinMax(t, boot, maxBytes, resizer, nil)

	injectSample(g, clk, criticalGrowSample(uint64(boot)))
	g.evaluate(context.Background())

	if len(resizer.calls) != 1 {
		t.Fatalf("expected 1 ResizeMemory call, got %d (%v)", len(resizer.calls), resizer.calls)
	}
	got := resizer.calls[0]
	if got > maxBytes {
		t.Errorf("clamped jump target %d exceeds maxBytes %d", got, maxBytes)
	}
	if got != maxBytes {
		t.Errorf("jump target = %d, want %d (clamped to maxBytes)", got, maxBytes)
	}
	if got%memHotplugAlignBytes != 0 {
		t.Errorf("target %d is not a multiple of memHotplugAlignBytes %d", got, memHotplugAlignBytes)
	}
}

// TestCriticalJump_RespectsHeadroom: when host headroom is insufficient the
// jump-to-floor must be refused even under critical pressure.
func TestCriticalJump_RespectsHeadroom(t *testing.T) {
	t.Parallel()
	const boot int64 = 512 * 1024 * 1024

	resizer := newFakeResizer(boot)
	g, clk := newTestGovernorMinMax(t, boot, 4*gib, resizer, &fakeHeadroom{ok: false})

	injectSample(g, clk, criticalGrowSample(uint64(boot)))
	g.evaluate(context.Background())

	if len(resizer.calls) != 0 {
		t.Errorf("critical jump must be refused when host headroom is insufficient; calls=%v", resizer.calls)
	}
}

// TestNonCriticalGrow_UnchangedDeficitStep: a non-critical grow (MemAvailable
// between 8% and 20%) must still use the deficit-scaled staircase, not the
// jump-to-floor. Regression guard for the critical branch.
func TestNonCriticalGrow_UnchangedDeficitStep(t *testing.T) {
	t.Parallel()
	const boot int64 = 512 * 1024 * 1024

	resizer := newFakeResizer(boot)
	g, clk := newTestGovernorMinMax(t, boot, 4*gib, resizer, nil)

	// growSample at 512 MiB total: MemAvailable=10% (above criticalGrowThreshold
	// 8%) and PSI full=0 → NOT critical. Must use deficit step, not floor jump.
	injectSample(g, clk, growSample(uint64(boot)))
	g.evaluate(context.Background())

	if len(resizer.calls) != 1 {
		t.Fatalf("non-critical grow: expected 1 resize call, got %d", len(resizer.calls))
	}
	got := resizer.calls[0]
	if got == safeMemFloorBytes {
		t.Errorf("non-critical grow produced safeMemFloorBytes %d; want deficit-scaled step (e.g. 768 MiB)", got)
	}
	// Deficit-scaled result from 512 MiB boot with growSample(512MiB):
	// deficit=179.2MiB < minGrowStepBytes=256MiB → step=256MiB →
	// alignUp(512MiB+256MiB, 256MiB) = 768MiB = 805306368.
	const wantStep int64 = 805306368 // 768 MiB
	if got != wantStep {
		t.Errorf("non-critical grow target = %d (%d MiB), want %d (%d MiB) (deficit-scaled step)",
			got, got>>20, wantStep, wantStep>>20)
	}
}

// TestAlignUpDown covers the alignment helpers directly, including the negative
// inputs that a shrink target below zero can produce before clamping.
func TestAlignUpDown(t *testing.T) {
	t.Parallel()
	const a = 256 * 1024 * 1024
	cases := []struct {
		n              int64
		wantUp, wantDn int64
	}{
		{0, 0, 0},
		{a, a, a},
		{a + 1, 2 * a, a},
		{2*a - 1, 2 * a, a},
		{912680550, 1073741824, 805306368}, // ~870.4 MiB
		{-1, 0, -a},                        // negative: up→0, down→one block below
		{-a, -a, -a},
	}
	for _, tc := range cases {
		if got := alignUp(tc.n, a); got != tc.wantUp {
			t.Errorf("alignUp(%d) = %d, want %d", tc.n, got, tc.wantUp)
		}
		if got := alignDown(tc.n, a); got != tc.wantDn {
			t.Errorf("alignDown(%d) = %d, want %d", tc.n, got, tc.wantDn)
		}
	}
}
