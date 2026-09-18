package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/resize"
)

type testLogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *testLogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *testLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *testLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *testLogHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *testLogHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Message == msg {
			n++
		}
	}
	return n
}

func captureLog(t *testing.T) *testLogHandler {
	t.Helper()
	h := &testLogHandler{}
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	slog.SetDefault(slog.New(h))
	return h
}

type fakePollSource struct{ sample resize.Sample }

func (f *fakePollSource) Poll(_ context.Context) (resize.Sample, error) { return f.sample, nil }

type fakeStreamSource struct {
	fakePollSource
	samples []resize.Sample
}

func (f *fakeStreamSource) Stream(_ context.Context) (<-chan resize.Sample, <-chan error, error) {
	ch := make(chan resize.Sample, len(f.samples))
	for _, s := range f.samples {
		ch <- s
	}
	close(ch)
	errCh := make(chan error, 1)
	close(errCh)
	return ch, errCh, nil
}

type fakeObserveResp struct {
	status     driver.DriftStatus
	newBalloon uint32 // MiB
}

type fakeMemModeReporter struct {
	mu         sync.Mutex
	balloonMiB uint32
	responses  []fakeObserveResp
}

func newFakeReporter(balloonMiB uint32, responses ...fakeObserveResp) *fakeMemModeReporter {
	return &fakeMemModeReporter{balloonMiB: balloonMiB, responses: responses}
}

func (f *fakeMemModeReporter) MemoryMode(_ domain.SandboxID) driver.MemoryMode {
	return driver.MemoryModeBalloon
}

func (f *fakeMemModeReporter) BalloonBytes(_ domain.SandboxID) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(f.balloonMiB) * 1024 * 1024
}

func (f *fakeMemModeReporter) ObserveSample(_ domain.SandboxID, _, _ uint64) (driver.DriftStatus, uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.responses) == 0 {
		return driver.DriftOK, f.balloonMiB
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	f.balloonMiB = resp.newBalloon
	return resp.status, resp.newBalloon
}

func newNorm(inner resize.TelemetrySource, reporter *fakeMemModeReporter) *balloonNormSource {
	return newBalloonNormSource(inner, reporter, domain.NewSandboxID())
}

func TestBalloonNormSource_Poll(t *testing.T) {
	const (
		totalMiB   = 8192
		balloonMiB = 6144
		wantTotal  = (totalMiB - balloonMiB) * 1024 * 1024
	)
	inner := &fakePollSource{sample: resize.Sample{
		MemTotalBytes:     uint64(totalMiB) * 1024 * 1024,
		MemAvailableBytes: 512 * 1024 * 1024,
	}}
	norm := newNorm(inner, newFakeReporter(balloonMiB))

	s, err := norm.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if s.MemTotalBytes != wantTotal {
		t.Errorf("MemTotalBytes = %d, want %d", s.MemTotalBytes, wantTotal)
	}
	if s.MemAvailableBytes != 512*1024*1024 {
		t.Errorf("MemAvailableBytes changed unexpectedly: %d", s.MemAvailableBytes)
	}
}

func TestBalloonNormSource_Poll_NoPollSourceStream(t *testing.T) {
	inner := &fakePollSource{}
	norm := newNorm(inner, newFakeReporter(6144))
	_, _, err := norm.Stream(context.Background())
	if !resize.IsStreamUnsupported(err) {
		t.Errorf("expected ErrStreamUnsupported, got %v", err)
	}
}

func TestBalloonNormSource_Stream(t *testing.T) {
	const (
		totalMiB   = 8192
		balloonMiB = 6144
		wantTotal  = (totalMiB - balloonMiB) * 1024 * 1024
	)
	inner := &fakeStreamSource{
		samples: []resize.Sample{
			{MemTotalBytes: uint64(totalMiB) * 1024 * 1024, MemAvailableBytes: 200 * 1024 * 1024},
			{MemTotalBytes: uint64(totalMiB) * 1024 * 1024, MemAvailableBytes: 100 * 1024 * 1024},
		},
	}
	norm := newNorm(inner, newFakeReporter(balloonMiB))

	sampleCh, errCh, err := norm.Stream(context.Background())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []resize.Sample
	for s := range sampleCh {
		got = append(got, s)
	}
	if streamErr := <-errCh; streamErr != nil && !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("stream error: %v", streamErr)
	}
	if len(got) != 2 {
		t.Fatalf("got %d samples, want 2", len(got))
	}
	for i, s := range got {
		if s.MemTotalBytes != wantTotal {
			t.Errorf("sample[%d] MemTotalBytes = %d, want %d", i, s.MemTotalBytes, wantTotal)
		}
	}
}

func TestBalloonNormSource_Clamp(t *testing.T) {
	inner := &fakePollSource{sample: resize.Sample{MemTotalBytes: 100}}
	norm := newNorm(inner, newFakeReporter(6144))
	s, _ := norm.Poll(context.Background())
	if s.MemTotalBytes != 0 {
		t.Errorf("underflow not clamped to 0: %d", s.MemTotalBytes)
	}
}

type blockingStreamSource struct {
	fakePollSource
	sampleCh chan resize.Sample
	errCh    chan error
}

func (f *blockingStreamSource) Stream(_ context.Context) (<-chan resize.Sample, <-chan error, error) {
	return f.sampleCh, f.errCh, nil
}

func TestBalloonNormSource_DriftSuspect_Poll(t *testing.T) {
	h := captureLog(t)
	const mib = 1024 * 1024
	inner := &fakePollSource{sample: resize.Sample{
		MemTotalBytes:     8192 * mib,
		MemAvailableBytes: 7000 * mib,
	}}
	reporter := newFakeReporter(6144, fakeObserveResp{driver.DriftSuspect, 6144})
	norm := newNorm(inner, reporter)

	s, err := norm.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	wantTotal := uint64(8192-6144) * mib
	if s.MemTotalBytes != wantTotal {
		t.Errorf("MemTotalBytes = %d, want %d", s.MemTotalBytes, wantTotal)
	}
	if h.count("govern.balloon.drift_suspect") != 1 {
		t.Errorf("drift_suspect log count = %d, want 1", h.count("govern.balloon.drift_suspect"))
	}
	if h.count("govern.balloon.drift_corrected") != 0 {
		t.Errorf("drift_corrected should not fire on first sample")
	}
}

func TestBalloonNormSource_DriftCorrected_Poll(t *testing.T) {
	h := captureLog(t)
	const mib = 1024 * 1024
	inner := &fakePollSource{sample: resize.Sample{
		MemTotalBytes:     8192 * mib,
		MemAvailableBytes: 7000 * mib,
	}}
	reporter := newFakeReporter(6144,
		fakeObserveResp{driver.DriftSuspect, 6144},
		fakeObserveResp{driver.DriftOK, 6144},
		fakeObserveResp{driver.DriftCorrected, 1192},
	)
	norm := newNorm(inner, reporter)

	norm.Poll(context.Background()) //nolint:errcheck
	norm.Poll(context.Background()) //nolint:errcheck
	s, err := norm.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	wantTotal := uint64(8192-1192) * mib
	if s.MemTotalBytes != wantTotal {
		t.Errorf("MemTotalBytes = %d, want %d", s.MemTotalBytes, wantTotal)
	}
	if h.count("govern.balloon.drift_corrected") != 1 {
		t.Errorf("drift_corrected log count = %d, want 1", h.count("govern.balloon.drift_corrected"))
	}
}

func TestBalloonNormSource_DriftCorrected_Stream(t *testing.T) {
	h := captureLog(t)
	const mib = 1024 * 1024
	reporter := newFakeReporter(6144,
		fakeObserveResp{driver.DriftSuspect, 6144},
		fakeObserveResp{driver.DriftOK, 6144},
		fakeObserveResp{driver.DriftCorrected, 1192},
	)
	inner := &fakeStreamSource{
		samples: []resize.Sample{
			{MemTotalBytes: 8192 * mib, MemAvailableBytes: 7000 * mib},
			{MemTotalBytes: 8192 * mib, MemAvailableBytes: 7000 * mib},
			{MemTotalBytes: 8192 * mib, MemAvailableBytes: 7000 * mib},
		},
	}
	norm := newNorm(inner, reporter)
	sampleCh, errCh, err := norm.Stream(context.Background())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []resize.Sample
	for s := range sampleCh {
		got = append(got, s)
	}
	if e := <-errCh; e != nil && !errors.Is(e, context.Canceled) {
		t.Fatalf("stream error: %v", e)
	}
	if len(got) != 3 {
		t.Fatalf("got %d samples, want 3", len(got))
	}
	wantCorrected := uint64(8192-1192) * mib
	if got[2].MemTotalBytes != wantCorrected {
		t.Errorf("sample[2] MemTotalBytes = %d, want %d", got[2].MemTotalBytes, wantCorrected)
	}
	if h.count("govern.balloon.drift_corrected") != 1 {
		t.Errorf("drift_corrected log count = %d, want 1", h.count("govern.balloon.drift_corrected"))
	}
}

func TestBalloonNormSource_FirstSampleLog_Once(t *testing.T) {
	h := captureLog(t)
	inner := &fakePollSource{sample: resize.Sample{
		MemTotalBytes:     8192 * 1024 * 1024,
		MemAvailableBytes: 512 * 1024 * 1024,
	}}
	norm := newNorm(inner, newFakeReporter(6144))
	for range 3 {
		if _, err := norm.Poll(context.Background()); err != nil {
			t.Fatalf("Poll: %v", err)
		}
	}
	if n := h.count("govern.balloon.first_sample"); n != 1 {
		t.Errorf("first_sample log count = %d, want 1", n)
	}
}

func TestBalloonNormSource_Stream_CancelUnblocksForwarder(t *testing.T) {
	sampleCh := make(chan resize.Sample, 1)
	sampleCh <- resize.Sample{MemTotalBytes: 8192 * 1024 * 1024}
	innerErrCh := make(chan error)

	inner := &blockingStreamSource{sampleCh: sampleCh, errCh: innerErrCh}
	norm := newNorm(inner, newFakeReporter(6144))

	ctx, cancel := context.WithCancel(context.Background())

	normCh, _, err := norm.Stream(ctx)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-normCh:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("forwarder goroutine did not exit within 2s after ctx cancel")
		}
	}
}

func TestBalloonNormSource_FlatMode_NoOp(t *testing.T) {
	const mib = 1024 * 1024
	inner := &fakePollSource{sample: resize.Sample{
		MemTotalBytes:     4096 * mib,
		MemAvailableBytes: 1024 * mib,
	}}
	reporter := newFakeReporter(0)
	norm := newNorm(inner, reporter)

	s, err := norm.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if s.MemTotalBytes != 4096*mib {
		t.Errorf("MemTotalBytes = %d, want %d (flat mode: zero balloon is a no-op)", s.MemTotalBytes, uint64(4096*mib))
	}
}
