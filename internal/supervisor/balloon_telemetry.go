package supervisor

import (
	"context"
	"fmt"

	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/resize"
)

type balloonNormSource struct {
	inner   resize.TelemetrySource
	resizer *cloudhypervisor.BalloonMemoryResizer
}

func newBalloonNormSource(inner resize.TelemetrySource, r *cloudhypervisor.BalloonMemoryResizer) *balloonNormSource {
	return &balloonNormSource{inner: inner, resizer: r}
}

func (b *balloonNormSource) Poll(ctx context.Context) (resize.Sample, error) {
	s, err := b.inner.Poll(ctx)
	if err != nil {
		return s, err
	}
	b.norm(&s)
	return s, nil
}

func (b *balloonNormSource) norm(s *resize.Sample) {
	balloon := uint64(b.resizer.BalloonBytes()) //nolint:gosec
	if s.MemTotalBytes > balloon {
		s.MemTotalBytes -= balloon
	} else {
		s.MemTotalBytes = 0
	}
}

func (b *balloonNormSource) Stream(ctx context.Context) (<-chan resize.Sample, <-chan error, error) {
	streamer, ok := b.inner.(resize.TelemetryStream)
	if !ok {
		return nil, nil, fmt.Errorf("%w: inner does not implement TelemetryStream", resize.ErrStreamUnsupported)
	}
	sampleCh, errCh, err := streamer.Stream(ctx)
	if err != nil {
		return nil, nil, err
	}
	normCh := make(chan resize.Sample)
	go func() {
		defer close(normCh)
		for s := range sampleCh {
			b.norm(&s)
			normCh <- s
		}
	}()
	return normCh, errCh, nil
}
