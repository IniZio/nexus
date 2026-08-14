// AR-DISK unit tests for DiskAxis.
//
// All tests inject a fake clock and a fake DiskResizer — no real VM, no real
// timers. Helpers (fakeClock, injectSample, newTestGovernorMinMax, etc.) are
// defined in govern_test.go in the same package.
package govern

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// ── Fake DiskResizer ──────────────────────────────────────────────────────────

type fakeDiskResizer struct {
	calls []diskGrowCall
	err   error // if non-nil, GrowDisk returns this error
}

type diskGrowCall struct {
	diskIndex int
	target    int64
}

func (f *fakeDiskResizer) GrowDisk(_ context.Context, diskIndex int, targetBytes int64) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, diskGrowCall{diskIndex: diskIndex, target: targetBytes})
	return nil
}

// ── Sample helpers ────────────────────────────────────────────────────────────

// diskSampleAtRatio builds a Sample with DiskSupported=true and the given
// used/total ratio.
func diskSampleAtRatio(total uint64, ratio float64) resize.Sample {
	used := uint64(float64(total) * ratio)
	return resize.Sample{
		Timestamp:      time.Now(),
		DiskUsedBytes:  used,
		DiskTotalBytes: total,
		DiskSupported:  true,
		// Memory fields left zero — disk tests don't need them.
	}
}

// diskSampleUnsupported returns a Sample with DiskSupported=false.
func diskSampleUnsupported() resize.Sample {
	return resize.Sample{
		Timestamp:     time.Now(),
		DiskSupported: false,
	}
}

// ── DiskAxis test helpers ─────────────────────────────────────────────────────

// newTestDiskAxis creates a Governor with dummy memory bounds and attaches a
// DiskAxis on top, returning the axis, its fake resizer, and the fake clock.
func newTestDiskAxis(t *testing.T, diskMaxBytes int64, dr *fakeDiskResizer) (*DiskAxis, *fakeClock) {
	t.Helper()
	// Memory bounds must be non-zero so Governor.Run does not short-circuit,
	// but we never call Run in disk tests — we call Evaluate directly.
	resizer := newFakeResizer(2 * 1024 * 1024 * 1024) // 2 GiB dummy
	g, clk := newTestGovernorMinMax(t, 2*1024*1024*1024, 8*1024*1024*1024, resizer, nil)
	if diskMaxBytes > 0 {
		g.bounds.DiskMaxBytes = diskMaxBytes
	}
	axis := NewDiskAxis(g, dr, 0)
	return axis, clk
}

// pastBootDelay advances clk past diskBootDelay so Evaluate no longer skips.
func pastBootDelay(clk *fakeClock) {
	clk.Advance(diskBootDelay + time.Second)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestDiskAxis_ControlLawConstants verifies the control-law constants match the
// values from OLD disk_resize.go:26-45 and the ticket spec.
func TestDiskAxis_ControlLawConstants(t *testing.T) {
	const wantGrowThreshold = 0.80
	const wantGrowStep = 16 * int64(1<<30)
	const wantDefaultMax = 100 * int64(1<<30)
	const wantCooldown = 30 * time.Second
	const wantBootDelay = 15 * time.Second

	if diskGrowThreshold != wantGrowThreshold {
		t.Errorf("diskGrowThreshold = %v, want %v", diskGrowThreshold, wantGrowThreshold)
	}
	if diskGrowStep != wantGrowStep {
		t.Errorf("diskGrowStep = %d, want %d", diskGrowStep, wantGrowStep)
	}
	if diskDefaultMax != wantDefaultMax {
		t.Errorf("diskDefaultMax = %d, want %d", diskDefaultMax, wantDefaultMax)
	}
	if diskGrowCooldown != wantCooldown {
		t.Errorf("diskGrowCooldown = %v, want %v", diskGrowCooldown, wantCooldown)
	}
	if diskBootDelay != wantBootDelay {
		t.Errorf("diskBootDelay = %v, want %v", diskBootDelay, wantBootDelay)
	}
}

// TestDiskAxis_NoActionWhenDiskSupportedFalse proves Trap 1:
// a sample with DiskSupported=false must never trigger a grow, even when
// DiskTotalBytes would yield a "0% used" ratio that reads as healthy.
func TestDiskAxis_NoActionWhenDiskSupportedFalse(t *testing.T) {
	dr := &fakeDiskResizer{}
	axis, clk := newTestDiskAxis(t, 0, dr)
	pastBootDelay(clk)

	// Inject a sample where DiskSupported is false.
	injectSample(axis.g, clk, diskSampleUnsupported())
	axis.Evaluate(context.Background())

	if len(dr.calls) != 0 {
		t.Errorf("GrowDisk called %d time(s) with DiskSupported=false, want 0", len(dr.calls))
	}
}

// TestDiskAxis_NoActionWhenDiskSupportedFalseHighUsage is the critical variant:
// a sample that would look like 100% full if we naïvely read DiskUsedBytes/0
// must also be suppressed when DiskSupported=false.
func TestDiskAxis_NoActionWhenDiskSupportedFalseHighUsage(t *testing.T) {
	dr := &fakeDiskResizer{}
	axis, clk := newTestDiskAxis(t, 0, dr)
	pastBootDelay(clk)

	s := resize.Sample{
		Timestamp:      clk.Now(),
		DiskUsedBytes:  50 * uint64(diskGiB),
		DiskTotalBytes: 50 * uint64(diskGiB),
		DiskSupported:  false,
	}
	injectSample(axis.g, clk, s)
	axis.Evaluate(context.Background())

	if len(dr.calls) != 0 {
		t.Errorf("GrowDisk called when DiskSupported=false, want 0")
	}
}

// TestDiskAxis_GrowOnly_BelowThreshold proves grow-only: a disk at 50% used
// (well below 0.80) must not trigger any grow call.
func TestDiskAxis_GrowOnly_BelowThreshold(t *testing.T) {
	dr := &fakeDiskResizer{}
	axis, clk := newTestDiskAxis(t, 0, dr)
	pastBootDelay(clk)

	injectSample(axis.g, clk, diskSampleAtRatio(50*uint64(diskGiB), 0.50))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 0 {
		t.Errorf("GrowDisk called %d time(s) for 50%% usage, want 0", len(dr.calls))
	}
}

// TestDiskAxis_GrowOnly_AtThreshold proves the boundary: exactly at 0.80 does
// NOT trigger a grow (threshold is strictly >).
func TestDiskAxis_GrowOnly_AtThreshold(t *testing.T) {
	dr := &fakeDiskResizer{}
	axis, clk := newTestDiskAxis(t, 0, dr)
	pastBootDelay(clk)

	injectSample(axis.g, clk, diskSampleAtRatio(50*uint64(diskGiB), 0.80))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 0 {
		t.Errorf("GrowDisk called at exactly 0.80 threshold, want 0 (strictly greater)")
	}
}

// TestDiskAxis_HappyPath_Grow proves a normal grow event:
// disk at 85% used → GrowDisk called with current + 16 GiB.
func TestDiskAxis_HappyPath_Grow(t *testing.T) {
	dr := &fakeDiskResizer{}
	axis, clk := newTestDiskAxis(t, 0, dr)
	pastBootDelay(clk)

	totalBytes := uint64(30 * diskGiB)
	injectSample(axis.g, clk, diskSampleAtRatio(totalBytes, 0.85))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 1 {
		t.Fatalf("GrowDisk called %d time(s), want 1", len(dr.calls))
	}
	wantTarget := int64(totalBytes) + diskGrowStep
	if dr.calls[0].target != wantTarget {
		t.Errorf("GrowDisk target = %d, want %d", dr.calls[0].target, wantTarget)
	}
	if dr.calls[0].diskIndex != 0 {
		t.Errorf("GrowDisk diskIndex = %d, want 0", dr.calls[0].diskIndex)
	}
}

// TestDiskAxis_DiskIndex proves that the disk axis passes the correct diskIndex
// to GrowDisk (not hardcoded to 0 when constructed with index 1).
func TestDiskAxis_DiskIndex(t *testing.T) {
	dr := &fakeDiskResizer{}
	resizer := newFakeResizer(2 * 1024 * 1024 * 1024)
	g, clk := newTestGovernorMinMax(t, 2*1024*1024*1024, 8*1024*1024*1024, resizer, nil)
	axis := NewDiskAxis(g, dr, 1) // diskIndex = 1
	pastBootDelay(clk)

	injectSample(g, clk, diskSampleAtRatio(30*uint64(diskGiB), 0.85))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 1 {
		t.Fatalf("GrowDisk called %d time(s), want 1", len(dr.calls))
	}
	if dr.calls[0].diskIndex != 1 {
		t.Errorf("GrowDisk diskIndex = %d, want 1", dr.calls[0].diskIndex)
	}
}

// TestDiskAxis_CeilingClamps_DefaultMax proves that when current + step would
// exceed diskDefaultMax (100 GiB), the target is clamped to the ceiling.
func TestDiskAxis_CeilingClamps_DefaultMax(t *testing.T) {
	dr := &fakeDiskResizer{}
	axis, clk := newTestDiskAxis(t, 0, dr) // Bounds.DiskMaxBytes = 0 → use diskDefaultMax
	pastBootDelay(clk)

	// Disk at 92 GiB, 90% full → would grow to 108 GiB > 100 GiB ceiling.
	totalBytes := uint64(92 * diskGiB)
	injectSample(axis.g, clk, diskSampleAtRatio(totalBytes, 0.90))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 1 {
		t.Fatalf("GrowDisk called %d time(s), want 1", len(dr.calls))
	}
	if dr.calls[0].target != diskDefaultMax {
		t.Errorf("GrowDisk target = %d, want clamped to %d", dr.calls[0].target, diskDefaultMax)
	}
}

// TestDiskAxis_CeilingClamps_BoundsMax proves that Bounds.DiskMaxBytes is
// respected when it is lower than diskDefaultMax.
func TestDiskAxis_CeilingClamps_BoundsMax(t *testing.T) {
	dr := &fakeDiskResizer{}
	boundsCeiling := int64(50 * diskGiB)
	axis, clk := newTestDiskAxis(t, boundsCeiling, dr)
	pastBootDelay(clk)

	// Disk at 40 GiB, 85% full → would grow to 56 GiB > 50 GiB ceiling.
	totalBytes := uint64(40 * diskGiB)
	injectSample(axis.g, clk, diskSampleAtRatio(totalBytes, 0.85))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 1 {
		t.Fatalf("GrowDisk called %d time(s), want 1", len(dr.calls))
	}
	if dr.calls[0].target != boundsCeiling {
		t.Errorf("GrowDisk target = %d, want clamped to %d", dr.calls[0].target, boundsCeiling)
	}
}

// TestDiskAxis_AtHardMax_NoGrow proves that when the disk is already at the
// ceiling, no GrowDisk call is made (equal-size is a no-op not an error).
func TestDiskAxis_AtHardMax_NoGrow(t *testing.T) {
	dr := &fakeDiskResizer{}
	axis, clk := newTestDiskAxis(t, 0, dr)
	pastBootDelay(clk)

	// Disk already at 100 GiB ceiling, 90% full.
	totalBytes := uint64(diskDefaultMax)
	injectSample(axis.g, clk, diskSampleAtRatio(totalBytes, 0.90))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 0 {
		t.Errorf("GrowDisk called %d time(s) when already at ceiling, want 0", len(dr.calls))
	}
}

// TestDiskAxis_Cooldown proves that a second grow within diskGrowCooldown is
// suppressed, but a grow after the cooldown expires succeeds.
func TestDiskAxis_Cooldown(t *testing.T) {
	dr := &fakeDiskResizer{}
	axis, clk := newTestDiskAxis(t, 0, dr)
	pastBootDelay(clk)

	total := uint64(30 * diskGiB)
	sample := diskSampleAtRatio(total, 0.85)

	// First grow — should succeed.
	injectSample(axis.g, clk, sample)
	axis.Evaluate(context.Background())
	if len(dr.calls) != 1 {
		t.Fatalf("first Evaluate: GrowDisk called %d time(s), want 1", len(dr.calls))
	}

	// Advance just under the cooldown boundary — should be suppressed.
	clk.Advance(diskGrowCooldown - time.Second)
	injectSample(axis.g, clk, sample)
	axis.Evaluate(context.Background())
	if len(dr.calls) != 1 {
		t.Errorf("within cooldown: GrowDisk called %d time(s), want still 1", len(dr.calls))
	}

	// Advance past the cooldown — should fire again.
	clk.Advance(2 * time.Second)
	injectSample(axis.g, clk, sample)
	axis.Evaluate(context.Background())
	if len(dr.calls) != 2 {
		t.Errorf("after cooldown: GrowDisk called %d time(s), want 2", len(dr.calls))
	}
}

// TestDiskAxis_BootDelay proves that Evaluate takes no action before the boot
// delay has elapsed, then grows normally once it has.
func TestDiskAxis_BootDelay(t *testing.T) {
	dr := &fakeDiskResizer{}
	resizer := newFakeResizer(2 * 1024 * 1024 * 1024)
	g, clk := newTestGovernorMinMax(t, 2*1024*1024*1024, 8*1024*1024*1024, resizer, nil)
	axis := NewDiskAxis(g, dr, 0)
	// Do NOT call pastBootDelay; advance only partway through the boot delay.
	clk.Advance(diskBootDelay - time.Second)

	total := uint64(30 * diskGiB)
	injectSample(g, clk, diskSampleAtRatio(total, 0.90))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 0 {
		t.Errorf("within boot delay: GrowDisk called %d time(s), want 0", len(dr.calls))
	}

	// Advance past the boot delay — grow must now fire.
	clk.Advance(2 * time.Second)
	injectSample(g, clk, diskSampleAtRatio(total, 0.90))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 1 {
		t.Errorf("after boot delay: GrowDisk called %d time(s), want 1", len(dr.calls))
	}
}

// TestDiskAxis_GrowError_NoCooldownCorruption proves that a GrowDisk error
// does not set lastGrow, so the axis retries on the next evaluation cycle
// (after the cooldown does not interfere).
func TestDiskAxis_GrowError_NoCooldownCorruption(t *testing.T) {
	dr := &fakeDiskResizer{err: errors.New("host full")}
	axis, clk := newTestDiskAxis(t, 0, dr)
	pastBootDelay(clk)

	total := uint64(30 * diskGiB)
	injectSample(axis.g, clk, diskSampleAtRatio(total, 0.85))
	axis.Evaluate(context.Background())

	if len(dr.calls) != 0 {
		t.Fatalf("on error fakeDiskResizer.calls should be empty, got %d", len(dr.calls))
	}
	if !axis.lastGrow.IsZero() {
		t.Errorf("lastGrow set after failed GrowDisk, want zero")
	}

	// Second attempt immediately — error still returned; no cooldown penalty.
	axis.Evaluate(context.Background())
	if !axis.lastGrow.IsZero() {
		t.Errorf("lastGrow set after second failed GrowDisk, want zero")
	}
}
