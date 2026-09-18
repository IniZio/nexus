package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/resize"
)

type balloonNormSource struct {
	inner           resize.TelemetrySource
	reporter        driver.MemoryModeReporter
	id              domain.SandboxID
	firstSampleOnce sync.Once
}

func newBalloonNormSource(inner resize.TelemetrySource, reporter driver.MemoryModeReporter, id domain.SandboxID) *balloonNormSource {
	return &balloonNormSource{inner: inner, reporter: reporter, id: id}
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
	const mib = 1024 * 1024
	oldBalloonMiB := uint32(b.reporter.BalloonBytes(b.id) / mib) //nolint:gosec
	status, newBalloonMiB := b.reporter.ObserveSample(b.id, s.MemTotalBytes, s.MemAvailableBytes)

	guestTotalMiB := s.MemTotalBytes / mib
	guestAvailMiB := s.MemAvailableBytes / mib
	var guestUsedMiB uint64
	if s.MemTotalBytes > s.MemAvailableBytes {
		guestUsedMiB = (s.MemTotalBytes - s.MemAvailableBytes) / mib
	}
	effectiveTotalMiB := guestTotalMiB - uint64(newBalloonMiB)

	b.firstSampleOnce.Do(func() {
		slog.Info("govern.balloon.first_sample",
			"guest_total_mib", guestTotalMiB,
			"guest_avail_mib", guestAvailMiB,
			"guest_used_mib", guestUsedMiB,
			"balloon_mib", newBalloonMiB,
			"effective_total_mib", effectiveTotalMiB,
		)
	})
	switch status {
	case driver.DriftSuspect:
		slog.Debug("govern.balloon.drift_suspect",
			"guest_total_mib", guestTotalMiB,
			"guest_avail_mib", guestAvailMiB,
			"balloon_mib", oldBalloonMiB,
		)
	case driver.DriftCorrected:
		slog.Warn("govern.balloon.drift_corrected",
			"old_balloon_mib", oldBalloonMiB,
			"new_balloon_mib", newBalloonMiB,
			"guest_total_mib", guestTotalMiB,
			"guest_avail_mib", guestAvailMiB,
		)
	}

	balloon := uint64(b.reporter.BalloonBytes(b.id)) //nolint:gosec
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
	outErrCh := make(chan error, 1)
	go func() {
		defer close(normCh)
		for {
			select {
			case <-ctx.Done():
				return
			case s, ok := <-sampleCh:
				if !ok {
					return
				}
				b.norm(&s)
				select {
				case normCh <- s:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	go func() {
		defer close(outErrCh)
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-errCh:
				if !ok {
					return
				}
				if e != nil {
					select {
					case outErrCh <- e:
					default:
					}
					return
				}
			}
		}
	}()
	return normCh, outErrCh, nil
}
