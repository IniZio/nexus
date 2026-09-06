package govern

// Tests for the zram/swap-pressure grow signal (D-RAM-07 + D-RAM-10 flow gate).
//
// Mutation proof A — grow trigger (D-RAM-07):
// Removing the `return true` inside the swap-pressure block of sampleWantsGrow
// causes TestZramPressureGrowTrigger to fail. Substitution count: 1.
//
// Mutation proof A2 — flow gate in grow trigger path (D-RAM-10):
// Replacing `swapUsed > prevSwapUsed` with `false` in sampleWantsGrow
// causes TestZramPressureGrowTrigger to fail — swap increased but gate
// blocks it. Substitution count: 1.
//
// Mutation proof B — flow gate (D-RAM-10):
// Removing the `swapUsed > prevSwapUsed` guard (replacing with `true`) causes
// TestSwapFlowGateStaticSwapNoGrow to fail — the ratchet regression test.
// Substitution count: 1.
//
// Mutation proof C — shrink flow gate (D-RAM-13):
// Replacing `swapUsed > prevSwapUsed` with `true` in the swap block of
// sampleWantsShrink causes TestStaticSwapShrinkPinFloor to fail — static high
// swap would incorrectly block shrink. Substitution count: 1.

import (
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// liveZramSample constructs the exact sample measured 2026-08-30 inside sandbox
// nexus3/nexus3-create-orphan-leak-reap:
//
//	MemTotal:     1543912 kB  (1 543 847 936 bytes)
//	MemAvailable:  616516 kB  → ratio 0.399 — healthy, above 0.20 grow threshold
//	SwapTotal:    2097148 kB  (zram0)
//	SwapFree:     1386212 kB  → SwapUsed = 710936 kB → 46% of MemTotal
//	PSI some_avg10: 0.00      — zram is idle but heavily loaded; no active stall
//
// The governor must want to grow on this sample despite the healthy MemAvailable
// ratio, because zram has absorbed 46% of MemTotal as compressed pages.
func liveZramSample() resize.Sample {
	const (
		memTotalKB    = 1543912
		memAvailKB    = 616516
		swapTotalKB   = 2097148
		swapFreeKB    = 1386212 // swapUsed = 710936 kB → ratio 0.460
	)
	return resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     memTotalKB * 1024,
		MemAvailableBytes: memAvailKB * 1024,
		SwapTotalBytes:    swapTotalKB * 1024,
		SwapFreeBytes:     swapFreeKB * 1024,
		MemPSISupported:   true,
		MemPSISomeAvg10:   0.0, // PSI avg10 zero — flow metric misses stock pressure
		MemPSIFullAvg10:   0.0,
	}
}

// TestZramPressureGrowTrigger asserts that the exact live sample fires a grow
// when swap has genuinely increased since the previous sample — the actual
// production trigger path (D-RAM-07 + D-RAM-10 flow gate).
//
// prevSwapUsed is set to swapUsed/2 so swap has climbed from ~355 MiB to
// ~710 MiB across two samples. This is the real production scenario that
// D-RAM-07 was designed to catch.
//
// Mutation targets:
//   - Comment out `return true` inside the swap-pressure block → must fail (D-RAM-07).
//   - Replace `swapUsed > prevSwapUsed` with `false` → must fail (D-RAM-10 gate).
//
// Substitution count: 1 per mutation.
func TestZramPressureGrowTrigger(t *testing.T) {
	s := liveZramSample()

	availRatio := float64(s.MemAvailableBytes) / float64(s.MemTotalBytes)
	if availRatio < defaultGrowThreshold {
		t.Fatalf("test setup error: avail_ratio=%.3f is below defaultGrowThreshold=%.2f — "+
			"the MemAvailable term would fire before the swap-pressure term, "+
			"making this test unable to isolate signal 3 (D-RAM-07/D-RAM-10)",
			availRatio, defaultGrowThreshold)
	}
	if s.MemPSISomeAvg10 >= psiGrowPressure {
		t.Fatalf("test setup error: PSI avg10=%.2f should be below psiGrowPressure=%.2f to isolate the swap-pressure term", s.MemPSISomeAvg10, psiGrowPressure)
	}

	swapUsed := s.SwapTotalBytes - s.SwapFreeBytes
	swapRatio := float64(swapUsed) / float64(s.MemTotalBytes)

	// prevSwapUsed = swapUsed/2: models a real increase from ~355 MiB to ~710 MiB
	// across two governor samples. The flow gate (swapUsed > prevSwapUsed) must
	// pass, and the swap-pressure term must fire because swapRatio >= defaultSwapPressureRatio.
	prevSwapUsed := swapUsed / 2
	if !sampleWantsGrow(s, prevSwapUsed) {
		t.Fatalf("sampleWantsGrow=false on zram-loaded sample (prevSwapUsed=%d, swapUsed=%d): "+
			"avail_ratio=%.3f (above grow threshold %.2f), "+
			"swap_used_ratio=%.3f (above swap threshold %.2f), "+
			"psi_avg10=%.2f — expected grow to fire on swap-pressure signal (D-RAM-07/D-RAM-10)",
			prevSwapUsed, swapUsed,
			availRatio, defaultGrowThreshold,
			swapRatio, defaultSwapPressureRatio,
			s.MemPSISomeAvg10)
	}
}

// TestZramPressureGrowOnGovernorRestart asserts grow fires on first sample after
// a governor restart, when prevSwapUsed=0 and current SwapUsed is already high.
//
// D-RAM-14 NOTE: this encodes a KNOWN ACCEPTED BIAS. Governor.prevSwapUsed is
// zero at startup, so the first sample grants one unconditional grow even if
// zram was already loaded before the governor started. This is intentional
// (conservative, safe to grow). If the bias is ever fixed by seeding
// prevSwapUsed from the first observed sample, THIS TEST IS THE ONE TO UPDATE —
// not TestZramPressureGrowTrigger.
func TestZramPressureGrowOnGovernorRestart(t *testing.T) {
	s := liveZramSample()

	availRatio := float64(s.MemAvailableBytes) / float64(s.MemTotalBytes)
	if availRatio < defaultGrowThreshold {
		t.Fatalf("test setup error: avail_ratio=%.3f is below defaultGrowThreshold=%.2f — "+
			"MemAvailable term would fire first, can't isolate swap-pressure signal",
			availRatio, defaultGrowThreshold)
	}
	if s.MemPSISomeAvg10 >= psiGrowPressure {
		t.Fatalf("test setup error: PSI avg10=%.2f should be below psiGrowPressure=%.2f",
			s.MemPSISomeAvg10, psiGrowPressure)
	}

	swapUsed := s.SwapTotalBytes - s.SwapFreeBytes
	swapRatio := float64(swapUsed) / float64(s.MemTotalBytes)

	// prevSwapUsed=0: governor just restarted; treated as increase (D-RAM-14 restart bias).
	if !sampleWantsGrow(s, 0) {
		t.Fatalf("sampleWantsGrow=false on governor-restart sample (prevSwapUsed=0): "+
			"swap_used_ratio=%.3f — restart bias must grant a grow (D-RAM-14)",
			swapRatio)
	}
}

// TestZramPressureAbsentNoGrow is the regression guard (AC3): the SAME
// MemAvailable ratio (0.399) with ZERO swap usage must NOT trigger a grow.
// The swap-pressure term must not become an unconditional grow.
func TestZramPressureAbsentNoGrow(t *testing.T) {
	memTotalBytes := uint64(1543912 * 1024)
	s := resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     memTotalBytes,
		MemAvailableBytes: uint64(float64(memTotalBytes) * 0.399), // same ratio as live sample
		MemPSISupported:   true,
		MemPSISomeAvg10:   0.0,
		SwapTotalBytes:    0, // no swap — field absent (old agent or no zram)
		SwapFreeBytes:     0,
	}

	if sampleWantsGrow(s, 0) {
		t.Fatalf("sampleWantsGrow=true with avail_ratio=0.399 and zero swap — "+
			"swap-pressure term must not fire when SwapTotalBytes=0 (D-RAM-10 guard)")
	}
}

// TestZramPressureBelowThresholdNoGrow confirms that low zram usage (below
// defaultSwapPressureRatio) does not trigger a grow at a healthy MemAvailable
// ratio. Guards against the threshold being accidentally set to zero.
func TestZramPressureBelowThresholdNoGrow(t *testing.T) {
	memTotalBytes := uint64(1543912 * 1024)
	// SwapUsed = 5% of MemTotal — well below the 20% threshold.
	swapUsedBytes := uint64(float64(memTotalBytes) * 0.05)
	swapTotalBytes := uint64(2097148 * 1024)
	swapFreeBytes := swapTotalBytes - swapUsedBytes

	s := resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     memTotalBytes,
		MemAvailableBytes: uint64(float64(memTotalBytes) * 0.399),
		MemPSISupported:   true,
		MemPSISomeAvg10:   0.0,
		SwapTotalBytes:    swapTotalBytes,
		SwapFreeBytes:     swapFreeBytes,
	}

	// prevSwapUsed=0: even with an apparent increase (0→5%), ratio is below
	// threshold so no grow should fire.
	if sampleWantsGrow(s, 0) {
		swapRatio := float64(swapUsedBytes) / float64(memTotalBytes)
		t.Fatalf("sampleWantsGrow=true with swap_used_ratio=%.3f (below threshold %.2f) "+
			"and healthy avail_ratio=0.399 — threshold guard failed",
			swapRatio, defaultSwapPressureRatio)
	}
}

// TestSwapFlowGateStaticSwapNoGrow is the ratchet regression test (D-RAM-10 AC3).
// A guest with a STATIC high SwapUsed (no increase since previous sample) must
// NOT keep growing. This is the core criterion: the flow gate breaks the ratchet.
//
// Mutation target (sampleWantsGrow): replace `swapUsed > prevSwapUsed` with
// `true` — this test must fail (grow fires on static stock). Substitution count: 1.
func TestSwapFlowGateStaticSwapNoGrow(t *testing.T) {
	s := liveZramSample() // SwapUsed = 710936 kB * 1024 ≈ 710 MiB, ratio ≈ 46%

	// prevSwapUsed equals current SwapUsed: no increase between samples.
	// This models the steady state after an initial grow: zram is still heavily
	// loaded but not accumulating new pages.
	swapUsed := s.SwapTotalBytes - s.SwapFreeBytes
	if sampleWantsGrow(s, swapUsed) {
		t.Fatalf("sampleWantsGrow=true with static SwapUsed=%d bytes (ratio=%.3f) — "+
			"flow gate must suppress grow when SwapUsed has not increased (D-RAM-10 ratchet fix)",
			swapUsed, float64(swapUsed)/float64(s.MemTotalBytes))
	}
}

// TestZramPressureShrinkBlocked confirms that sampleWantsShrink returns false
// when SwapUsed has INCREASED since the previous sample (D-RAM-13 flow gate),
// even if MemAvailable appears high. A guest actively accumulating zram pages
// must not be shrunk regardless of the absolute swap ratio.
func TestZramPressureShrinkBlocked(t *testing.T) {
	memTotalBytes := uint64(1543912 * 1024)
	// MemAvailable looks very high (60%) — would normally want to shrink.
	s := resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     memTotalBytes,
		MemAvailableBytes: uint64(float64(memTotalBytes) * 0.60),
		MemPSISupported:   true,
		MemPSISomeAvg10:   0.0,
		SwapTotalBytes:    2097148 * 1024,
		SwapFreeBytes:     (2097148 - 710936) * 1024, // swapUsed = 710936 kB → 46%
	}
	swapUsed := s.SwapTotalBytes - s.SwapFreeBytes

	// prevSwapUsed < swapUsed: swap is actively increasing. Flow gate blocks shrink.
	if sampleWantsShrink(s, swapUsed-1) {
		t.Fatal("sampleWantsShrink=true while SwapUsed is increasing — " +
			"shrink must be blocked when SwapUsed has increased since previous sample (D-RAM-13)")
	}
}

// TestZramPressureShrinkStaticSwapAllowed confirms that sampleWantsShrink
// returns true when SwapUsed is STATIC (not increasing) even at 15% of MemTotal
// — a ratio that the old defaultSwapShrinkBlockRatio=0.10 check would have
// blocked. Under D-RAM-13 the flow gate only blocks on an increase; a stable
// high SwapUsed is part of the working set and does not pin the allocation.
//
// This test also verifies grow does not fire (static, below grow threshold),
// confirming the flow gates are symmetric.
func TestZramPressureShrinkStaticSwapAllowed(t *testing.T) {
	memTotalBytes := uint64(1543912 * 1024)
	// SwapUsed = 15% of MemTotal — above old shrink-block threshold (0.10),
	// below grow trigger threshold (0.20). Under D-RAM-13 it must not block shrink.
	swapUsedBytes := uint64(float64(memTotalBytes) * 0.15)
	swapTotalBytes := uint64(2097148 * 1024)

	s := resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     memTotalBytes,
		MemAvailableBytes: uint64(float64(memTotalBytes) * 0.60), // high — above shrink threshold
		MemPSISupported:   true,
		MemPSISomeAvg10:   0.0,
		SwapTotalBytes:    swapTotalBytes,
		SwapFreeBytes:     swapTotalBytes - swapUsedBytes,
	}

	swapUsed := s.SwapTotalBytes - s.SwapFreeBytes
	swapRatio := float64(swapUsed) / float64(memTotalBytes)

	// Verify test geometry: must be above old block ratio and below grow ratio.
	if swapRatio >= defaultSwapPressureRatio {
		t.Fatalf("test setup error: swapRatio=%.3f is above defaultSwapPressureRatio=%.2f",
			swapRatio, defaultSwapPressureRatio)
	}

	// Grow must not fire (static: prevSwapUsed == swapUsed).
	if sampleWantsGrow(s, swapUsed) {
		t.Fatalf("sampleWantsGrow=true with static SwapUsed at ratio=%.3f — "+
			"grow must not fire when SwapUsed is static and below grow threshold",
			swapRatio)
	}

	// Shrink must be allowed: swap is static, MemAvailable=60%>45% (D-RAM-13).
	if !sampleWantsShrink(s, swapUsed) {
		t.Fatalf("sampleWantsShrink=false with static SwapUsed at ratio=%.3f and "+
			"MemAvail=60%% — static high swap must not pin shrink (D-RAM-13 pin floor removed)",
			swapRatio)
	}
}

// TestStaticSwapShrinkPinFloor is the primary AC3 test for D-RAM-13.
// It proves the pin floor is gone: a guest with static SwapUsed of ~710 MiB
// (the measured live value from D-RAM-04) CAN shrink at MemTotal = 2 GiB,
// which is far below the old 10× pin floor (7.1 GiB) that the
// defaultSwapShrinkBlockRatio=0.10 check imposed.
//
// Mutation target (sampleWantsShrink): replace `swapUsed > prevSwapUsed` with
// `true` — this test must fail (shrink is incorrectly blocked). Substitution count: 1.
func TestStaticSwapShrinkPinFloor(t *testing.T) {
	// MemTotal = 2 GiB. Old pin floor: 10 × 710 MiB ≈ 7.1 GiB.
	// Under the old ratio check this guest could never shrink.
	memTotalBytes := uint64(2 * 1024 * 1024 * 1024) // 2 GiB

	// SwapUsed = 710936 kB (live measured, D-RAM-04). Ratio ≈ 33.9% of MemTotal.
	// Old check: 33.9% >= 10% → block; new check: static (prevSwapUsed==swapUsed) → allow.
	const swapUsedKB = 710936
	swapTotalBytes := uint64(2097148 * 1024)
	swapUsedBytes := uint64(swapUsedKB * 1024)
	swapFreeBytes := swapTotalBytes - swapUsedBytes

	// MemAvailable = 60% of MemTotal (1.2 GiB) — well above defaultShrinkThreshold (0.45).
	s := resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     memTotalBytes,
		MemAvailableBytes: uint64(float64(memTotalBytes) * 0.60),
		MemPSISupported:   true,
		MemPSISomeAvg10:   0.0,
		SwapTotalBytes:    swapTotalBytes,
		SwapFreeBytes:     swapFreeBytes,
	}

	swapUsed := s.SwapTotalBytes - s.SwapFreeBytes
	swapRatio := float64(swapUsed) / float64(memTotalBytes)
	oldPinFloorGiB := float64(swapUsedBytes) / (0.10 * float64(1024*1024*1024))

	// Sanity: MemTotal must be well below the old pin floor.
	if float64(memTotalBytes) >= float64(swapUsedBytes)/0.10 {
		t.Fatalf("test setup error: memTotal=%.1f GiB is above old pin floor=%.1f GiB",
			float64(memTotalBytes)/(1024*1024*1024), oldPinFloorGiB)
	}

	// Grow must not fire (static: prevSwapUsed == swapUsed, ratio=33.9% but static).
	if sampleWantsGrow(s, swapUsed) {
		t.Fatalf("sampleWantsGrow=true with static SwapUsed=%.3f of MemTotal — "+
			"grow must not fire when SwapUsed is not increasing", swapRatio)
	}

	// Shrink MUST be allowed: static swap, MemAvail=60%>45%, MemTotal below old pin floor.
	// Under old code (defaultSwapShrinkBlockRatio=0.10): 33.9%>=10% → returns false.
	// Under D-RAM-13: swapUsed==prevSwapUsed → not increasing → no block → returns true.
	if !sampleWantsShrink(s, swapUsed) {
		t.Fatalf("sampleWantsShrink=false with static SwapUsed ratio=%.3f and MemTotal=2GiB "+
			"(old pin floor was %.1f GiB) — static swap must not prevent shrink (D-RAM-13)",
			swapRatio, oldPinFloorGiB)
	}
}
