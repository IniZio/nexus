package govern

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/resize"
)

// pagingSample models the HAN-941 F7/F13/F20 guest: MemAvailable comfortably
// above the shrink threshold, swap present and STABLE (stock gate passes; the
// used ratio stays under defaultSwapPressureRatio so no grow fires either),
// but the cumulative pswpin counter advancing between samples.
func pagingSample(total, pswpin uint64) resize.Sample {
	return resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     total,
		MemAvailableBytes: uint64(float64(total) * 0.60),
		MemPSISupported:   true,
		SwapTotalBytes:    1 * gib,
		SwapFreeBytes:     gib / 2,
		SwapInPages:       pswpin,
	}
}

func TestSampleWantsShrink_SwapInDeltaBlocks(t *testing.T) {
	t.Parallel()
	const total = 8 * gib
	swapUsed := uint64(gib / 2)

	cases := []struct {
		name      string
		s         resize.Sample
		prevPswp  uint64
		wantShrnk bool
	}{
		{name: "paging_stable_swap", s: pagingSample(total, 5000), prevPswp: 4000, wantShrnk: false},
		{name: "paging_one_page", s: pagingSample(total, 4001), prevPswp: 4000, wantShrnk: false},
		{name: "no_paging_stable_swap", s: pagingSample(total, 4000), prevPswp: 4000, wantShrnk: true},
		{name: "old_agent_zero_field", s: pagingSample(total, 0), prevPswp: 0, wantShrnk: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sampleWantsShrink(tc.s, swapUsed, tc.prevPswp); got != tc.wantShrnk {
				t.Fatalf("sampleWantsShrink(pswpin=%d, prev=%d) = %v, want %v",
					tc.s.SwapInPages, tc.prevPswp, got, tc.wantShrnk)
			}
		})
	}
}

func TestSampleWantsShrink_SwapUsedRisingStillBlocks(t *testing.T) {
	t.Parallel()
	s := pagingSample(8*gib, 0)
	swapUsed := s.SwapTotalBytes - s.SwapFreeBytes
	if sampleWantsShrink(s, swapUsed-1, 0) {
		t.Fatal("sampleWantsShrink=true while SwapUsed is rising")
	}
	if !sampleWantsShrink(s, swapUsed, 0) {
		t.Fatal("sampleWantsShrink=false with stable SwapUsed and zero pswpin delta")
	}
}

func TestGovernor_PagingGuestNeverShrinks(t *testing.T) {
	t.Parallel()
	const boot = 4 * gib
	resizer := newFakeResizer(boot)
	g, clk := newTestGovernorMinMax(t, boot/2, boot*4, resizer, nil)
	ctx := context.Background()

	pswpin := uint64(10_000)
	for i := 0; i < 3*memoryShrinkConsecutive; i++ {
		clk.Advance(memoryEvalInterval)
		pswpin += 250
		g.acceptSample(ctx, pagingSample(uint64(boot), pswpin))
		if len(resizer.calls) != 0 {
			t.Fatalf("sample %d: paging guest was shrunk; calls=%v", i+1, resizer.calls)
		}
	}
	if g.prevSwapInPages != pswpin-250 {
		t.Fatalf("prevSwapInPages = %d, want %d (previous sample's counter)", g.prevSwapInPages, pswpin-250)
	}

	for i := 0; i < memoryShrinkConsecutive; i++ {
		clk.Advance(memoryEvalInterval)
		g.acceptSample(ctx, pagingSample(uint64(boot), pswpin))
	}
	if len(resizer.calls) != 1 || resizer.calls[0] >= boot {
		t.Fatalf("expected one shrink after pswpin went flat; calls=%v", resizer.calls)
	}
}

// Wire compatibility: an older agent's payload omits swap_in_pages entirely and
// must decode to 0 without error.
func TestSample_SwapInPagesOmittedDecodesZero(t *testing.T) {
	t.Parallel()
	var s resize.Sample
	if err := json.Unmarshal([]byte(`{"mem_total_bytes":1,"mem_available_bytes":1}`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.SwapInPages != 0 {
		t.Fatalf("SwapInPages = %d, want 0", s.SwapInPages)
	}
}

func TestGovernor_SwapInBlockLogsInfo(t *testing.T) {
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	const boot = 4 * gib
	g, clk := newTestGovernorMinMax(t, boot/2, boot*4, newFakeResizer(boot), nil)
	ctx := context.Background()

	clk.Advance(memoryEvalInterval)
	g.acceptSample(ctx, pagingSample(uint64(boot), 1000))
	buf.Reset()
	clk.Advance(memoryEvalInterval)
	g.acceptSample(ctx, pagingSample(uint64(boot), 1000))
	if strings.Contains(buf.String(), "shrink_blocked") {
		t.Fatalf("shrink_blocked logged with flat swap-in counter:\n%s", buf.String())
	}

	clk.Advance(memoryEvalInterval)
	g.acceptSample(ctx, pagingSample(uint64(boot), 1416))
	out := buf.String()
	for _, want := range []string{"govern.memory.shrink_blocked", "reason=swap_in", "delta=416", "swap_in_pages=1416"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q:\n%s", want, out)
		}
	}
}
