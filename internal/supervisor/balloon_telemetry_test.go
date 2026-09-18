package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/resize"
)

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

func newTestBalloonResizer(totalMiB, minMiB, balloonMiB uint32) *cloudhypervisor.BalloonMemoryResizer {
	return cloudhypervisor.NewBalloonMemoryResizer(nil, domain.NewSandboxID(), totalMiB, minMiB, balloonMiB)
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
	norm := newBalloonNormSource(inner, newTestBalloonResizer(totalMiB, 2048, balloonMiB))

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
	norm := newBalloonNormSource(inner, newTestBalloonResizer(8192, 2048, 6144))
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
	norm := newBalloonNormSource(inner, newTestBalloonResizer(totalMiB, 2048, balloonMiB))

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
	norm := newBalloonNormSource(inner, newTestBalloonResizer(8192, 2048, 6144))
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

func TestBalloonNormSource_Stream_CancelUnblocksForwarder(t *testing.T) {
	sampleCh := make(chan resize.Sample, 1)
	sampleCh <- resize.Sample{MemTotalBytes: 8192 * 1024 * 1024}
	innerErrCh := make(chan error)

	inner := &blockingStreamSource{sampleCh: sampleCh, errCh: innerErrCh}
	norm := newBalloonNormSource(inner, newTestBalloonResizer(8192, 2048, 6144))

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
