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

// ── Multi-disk and per-disk-sample tests ──────────────────────────────────────

// diskSampleWithStats builds a Sample with both DiskStats populated AND
// legacy fields set. Used to verify that Evaluate prefers DiskStats over
// legacy when a matching entry exists.
func diskSampleWithStats(stats []resize.DiskSample) resize.Sample {
	s := resize.Sample{Timestamp: time.Now()}
	// Populate legacy fields from the first entry if present, so old-path
	// callers still see something; multi-disk tests create their own axes.
	if len(stats) > 0 {
		s.DiskUsedBytes = stats[0].UsedBytes
		s.DiskTotalBytes = stats[0].TotalBytes
		s.DiskSupported = stats[0].Supported
	}
	s.DiskStats = stats
	return s
}

// TestDiskAxis_PerDiskSample_UsesMatchingEntry verifies that when DiskStats
// contains an entry for the axis's diskIndex, Evaluate uses that entry's
// Used/Total/Supported rather than the legacy fields.
func TestDiskAxis_PerDiskSample_UsesMatchingEntry(t *testing.T) {
	dr := &fakeDiskResizer{}
	resizer := newFakeResizer(2 << 30)
	g, clk := newTestGovernorMinMax(t, 2<<30, 8<<30, resizer, nil)
	g.bounds.DiskMaxBytes = 100 << 30
	axis := NewDiskAxis(g, dr, 2) // manage disk index 2
	pastBootDelay(clk)

	// DiskStats: index 2 is over threshold (90%), index 0 is under (10%).
	// Legacy fields mirror index 0 (under threshold) — must be ignored.
	sample := diskSampleWithStats([]resize.DiskSample{
		{Index: 0, UsedBytes: 1 << 30, TotalBytes: 10 << 30, Supported: true},
		{Index: 2, UsedBytes: 9 << 30, TotalBytes: 10 << 30, Supported: true},
	})
	sample.DiskUsedBytes = 1 << 30  // legacy: under threshold
	sample.DiskTotalBytes = 10 << 30
	sample.DiskSupported = true
	injectSample(g, clk, sample)

	axis.Evaluate(context.Background())

	if len(dr.calls) != 1 {
		t.Fatalf("expected 1 GrowDisk call (disk 2 over threshold), got %d", len(dr.calls))
	}
	if dr.calls[0].diskIndex != 2 {
		t.Errorf("GrowDisk diskIndex = %d, want 2", dr.calls[0].diskIndex)
	}
}

// TestDiskAxis_MultiDisk_IndependentAxes verifies that two DiskAxis instances
// (indices 1 and 2) each grow based on their own disk's ratio and do not
// interfere with each other.
func TestDiskAxis_MultiDisk_IndependentAxes(t *testing.T) {
	dr1 := &fakeDiskResizer{}
	dr2 := &fakeDiskResizer{}

	resizer := newFakeResizer(2 << 30)
	g, clk := newTestGovernorMinMax(t, 2<<30, 8<<30, resizer, nil)
	g.bounds.DiskMaxBytes = 100 << 30

	// axis1 manages disk index 1 (under threshold), axis2 manages disk index 2 (over threshold).
	axis1 := NewDiskAxis(g, dr1, 1)
	axis2 := NewDiskAxis(g, dr2, 2)
	pastBootDelay(clk)

	sample := diskSampleWithStats([]resize.DiskSample{
		{Index: 1, UsedBytes: 2 << 30, TotalBytes: 10 << 30, Supported: true}, // 20% — under
		{Index: 2, UsedBytes: 9 << 30, TotalBytes: 10 << 30, Supported: true}, // 90% — over
	})
	injectSample(g, clk, sample)

	axis1.Evaluate(context.Background())
	axis2.Evaluate(context.Background())

	// axis1 (index 1, 20%) must NOT grow.
	if len(dr1.calls) != 0 {
		t.Errorf("axis1 (disk 1, 20%%): expected no GrowDisk calls, got %d", len(dr1.calls))
	}
	// axis2 (index 2, 90%) must grow.
	if len(dr2.calls) != 1 {
		t.Fatalf("axis2 (disk 2, 90%%): expected 1 GrowDisk call, got %d", len(dr2.calls))
	}
	if dr2.calls[0].diskIndex != 2 {
		t.Errorf("axis2 GrowDisk diskIndex = %d, want 2", dr2.calls[0].diskIndex)
	}
}

// TestDiskAxis_MissingIndexInDiskStats_NoGrow verifies the hardened lookup:
// when DiskStats is non-empty but contains no entry for the axis's diskIndex,
// Evaluate must NOT grow (not fall through to legacy fields). This is the
// host/guest version-skew safety: a named-volume axis registered by a newer
// host is silently idle against an old guest that doesn't report it.
//
// MUTATION PROOF (missing entry = no grow): removing the `if !found { return }`
// guard in disk.go causes this test to fail because the legacy DiskUsedBytes
// (set to 9 GiB / 10 GiB = 90%) would trigger a spurious grow for the
// docker-disk axis (index 0) even though DiskStats has no entry for index 0.
func TestDiskAxis_MissingIndexInDiskStats_NoGrow(t *testing.T) {
	dr := &fakeDiskResizer{}
	resizer := newFakeResizer(2 << 30)
	g, clk := newTestGovernorMinMax(t, 2<<30, 8<<30, resizer, nil)
	g.bounds.DiskMaxBytes = 100 << 30
	// axis manages index 0 (docker named-volume disk).
	NewDiskAxis(g, dr, 0)
	pastBootDelay(clk)

	// DiskStats has an entry for index 1 (workspace) but NOT for index 0
	// (docker disk). Legacy fields are set over-threshold (90%) to catch any
	// spurious fallback.
	sample := resize.Sample{
		Timestamp:      time.Now(),
		DiskUsedBytes:  9 << 30,  // legacy: over threshold — must NOT be used
		DiskTotalBytes: 10 << 30,
		DiskSupported:  true,
		DiskStats: []resize.DiskSample{
			{Index: 1, UsedBytes: 9 << 30, TotalBytes: 10 << 30, Supported: true},
			// index 0 is absent: old guest doesn't report docker disk
		},
	}
	injectSample(g, clk, sample)

	// The axis for index 0 must see no matching DiskStats entry and return
	// without calling GrowDisk.
	g.axes[0].Evaluate(context.Background())

	if len(dr.calls) != 0 {
		t.Errorf("expected no GrowDisk call when index absent from DiskStats (got %d calls — spurious legacy fallback?)", len(dr.calls))
	}
}

// TestDiskAxis_TwoDiskStats_EachAxisDrivenByOwnIndex verifies the two-entry
// DiskStats scenario for a docker disk (index 0) + workspace (index 1):
// the docker axis grows when docker fills and the workspace axis is idle (and
// vice versa). This is the primary correctness proof for named-volume auto-resize.
func TestDiskAxis_TwoDiskStats_EachAxisDrivenByOwnIndex(t *testing.T) {
	drDocker := &fakeDiskResizer{}    // axis for index 0 (docker disk)
	drWorkspace := &fakeDiskResizer{} // axis for index 1 (workspace)

	resizer := newFakeResizer(2 << 30)
	g, clk := newTestGovernorMinMax(t, 2<<30, 8<<30, resizer, nil)
	g.bounds.DiskMaxBytes = 100 << 30

	NewDiskAxis(g, drDocker, 0)    // docker disk: index 0 → /dev/vdb
	NewDiskAxis(g, drWorkspace, 1) // workspace: index 1 → /dev/vdc
	pastBootDelay(clk)

	// Docker disk at 90% (over threshold), workspace at 20% (under).
	sample := diskSampleWithStats([]resize.DiskSample{
		{Index: 0, UsedBytes: 18 << 30, TotalBytes: 20 << 30, Supported: true}, // 90%
		{Index: 1, UsedBytes: 2 << 30, TotalBytes: 10 << 30, Supported: true},  // 20%
	})
	injectSample(g, clk, sample)

	g.axes[0].Evaluate(context.Background()) // docker axis
	g.axes[1].Evaluate(context.Background()) // workspace axis

	// Docker axis must grow.
	if len(drDocker.calls) != 1 {
		t.Fatalf("docker axis: expected 1 GrowDisk call, got %d", len(drDocker.calls))
	}
	if drDocker.calls[0].diskIndex != 0 {
		t.Errorf("docker axis: GrowDisk diskIndex = %d, want 0", drDocker.calls[0].diskIndex)
	}
	// Workspace axis must NOT grow (20% — under threshold).
	if len(drWorkspace.calls) != 0 {
		t.Errorf("workspace axis: expected no GrowDisk call at 20%%, got %d", len(drWorkspace.calls))
	}
}

// TestDiskAxis_LegacyFallback_WhenDiskStatsEmpty verifies that Evaluate falls
// back to legacy DiskUsedBytes/DiskTotalBytes/DiskSupported when DiskStats is
// empty (old guest agents that have not yet populated DiskStats).
func TestDiskAxis_LegacyFallback_WhenDiskStatsEmpty(t *testing.T) {
	dr := &fakeDiskResizer{}
	resizer := newFakeResizer(2 << 30)
	g, clk := newTestGovernorMinMax(t, 2<<30, 8<<30, resizer, nil)
	g.bounds.DiskMaxBytes = 100 << 30
	axis := NewDiskAxis(g, dr, 0) // workspace disk at index 0
	pastBootDelay(clk)

	// No DiskStats — only legacy fields (over threshold).
	sample := resize.Sample{
		Timestamp:      time.Now(),
		DiskUsedBytes:  9 << 30,
		DiskTotalBytes: 10 << 30,
		DiskSupported:  true,
		// DiskStats: nil (omitted)
	}
	injectSample(g, clk, sample)

	axis.Evaluate(context.Background())

	if len(dr.calls) != 1 {
		t.Fatalf("legacy fallback: expected 1 GrowDisk call, got %d", len(dr.calls))
	}
	if dr.calls[0].diskIndex != 0 {
		t.Errorf("legacy fallback: GrowDisk diskIndex = %d, want 0", dr.calls[0].diskIndex)
	}
}
