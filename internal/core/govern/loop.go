package govern

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/resize"
)

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type AxisEvaluator interface {
	Evaluate(ctx context.Context)
}

type axisEvalFunc func(ctx context.Context)

func (f axisEvalFunc) Evaluate(ctx context.Context) { f(ctx) }

type Governor struct {
	resizer   resize.MemoryResizer
	telemetry resize.TelemetrySource
	headroom  HostHeadroomReader
	bounds    resize.Bounds
	clock     Clock

	growCount           int
	shrinkCount         int
	lastResizeTime      time.Time
	lastResizeWasShrink bool
	grewOnce            bool
	latest              resize.Sample
	lastSampleTime      time.Time
	prevSwapUsed        uint64
	prevSwapInPages     uint64
	agentOutdated       bool
	pollErrLogged       bool
	axes                []AxisEvaluator
}

type Config struct {
	Resizer   resize.MemoryResizer
	Telemetry resize.TelemetrySource
	Headroom  HostHeadroomReader
	Bounds    resize.Bounds
	Clock     Clock
}

func New(cfg Config) *Governor {
	if cfg.Resizer == nil {
		panic("govern.New: Resizer must not be nil")
	}
	if cfg.Telemetry == nil {
		panic("govern.New: Telemetry must not be nil")
	}
	clk := Clock(realClock{})
	if cfg.Clock != nil {
		clk = cfg.Clock
	}
	hr := HostHeadroomReader(NewProcfsHeadroom())
	if cfg.Headroom != nil {
		hr = cfg.Headroom
	}
	g := &Governor{
		resizer:   cfg.Resizer,
		telemetry: cfg.Telemetry,
		headroom:  hr,
		bounds:    cfg.Bounds,
		clock:     clk,
	}
	g.axes = []AxisEvaluator{axisEvalFunc(g.evaluate)}
	return g
}

func (g *Governor) RegisterAxis(a AxisEvaluator) {
	g.axes = append(g.axes, a)
}

// streamWatchdog: silence budget before declaring the push stream dead and reconnecting (3× nominal eval interval).
const streamWatchdog = 3 * memoryEvalInterval

const (
	streamBackoffMin = 1 * time.Second
	streamBackoffMax = 30 * time.Second
)

// Run starts the adaptive sampling loop; blocks until ctx cancelled.
// Boot delay removed: first evaluation fires on the first validated sample.
// When telemetry implements TelemetryStream the push path is used; a watchdog
// reconnects after streamWatchdog silence; ErrStreamUnsupported falls back
// permanently to Poll; pollFallback polls once immediately then at the adaptive
// interval during each reconnect backoff.
func (g *Governor) Run(ctx context.Context) {
	if g.bounds.MemMinBytes == 0 || g.bounds.MemMaxBytes == 0 ||
		g.bounds.MemMinBytes >= g.bounds.MemMaxBytes {
		slog.Info("govern.loop.skipped", "reason", "bounds_not_configured")
		return
	}
	slog.Info("govern.loop.started",
		"min_bytes", g.bounds.MemMinBytes,
		"max_bytes", g.bounds.MemMaxBytes,
	)
	if streamer, ok := g.telemetry.(resize.TelemetryStream); ok {
		g.runStream(ctx, streamer)
	} else {
		g.runPoll(ctx)
	}
}

func (g *Governor) runStream(ctx context.Context, streamer resize.TelemetryStream) {
	backoff := streamBackoffMin
	for {
		if ctx.Err() != nil {
			return
		}
		streamCtx, streamCancel := context.WithCancel(ctx)
		sampleCh, errCh, err := streamer.Stream(streamCtx)
		if err != nil {
			streamCancel()
			if resize.IsStreamUnsupported(err) {
				slog.Info("govern.stream.unsupported", "reason", "old guest agent; falling back to poll permanently")
				g.runPoll(ctx)
				return
			}
			slog.Warn("govern.stream.open_error", "err", err, "reconnect_in", backoff)
			g.pollFallback(ctx, backoff)
			backoff = min(backoff*2, streamBackoffMax)
			continue
		}
		backoff = streamBackoffMin
		dead := g.driveStream(ctx, sampleCh, errCh)
		streamCancel()
		if dead && ctx.Err() == nil {
			g.pollFallback(ctx, backoff)
			backoff = min(backoff*2, streamBackoffMax)
		}
	}
}

func (g *Governor) driveStream(ctx context.Context, sampleCh <-chan resize.Sample, errCh <-chan error) (dead bool) {
	watchdog := g.clock.After(streamWatchdog)
	for {
		select {
		case s, ok := <-sampleCh:
			if !ok {
				return true
			}
			g.ingestSample(ctx, s)
			watchdog = g.clock.After(streamWatchdog)
		case err, ok := <-errCh:
			if ok && err != nil {
				slog.Warn("govern.stream.error", "err", err)
			}
			return true
		case <-watchdog:
			slog.Warn("govern.stream.watchdog_fired", "silence", streamWatchdog, "action", "reconnecting")
			return true
		case <-ctx.Done():
			return false
		}
	}
}

func (g *Governor) pollFallback(ctx context.Context, dur time.Duration) {
	g.pollOnce(ctx)
	if ctx.Err() != nil {
		return
	}
	timeout := g.clock.After(dur)
	for {
		interval := memoryEvalInterval
		if sampleWantsGrow(g.latest, g.prevSwapUsed) {
			interval = memoryPressurePollInterval
		}
		select {
		case <-timeout:
			return
		case <-ctx.Done():
			return
		case <-g.clock.After(interval):
			g.pollOnce(ctx)
			if ctx.Err() != nil {
				return
			}
		}
	}
}

func (g *Governor) runPoll(ctx context.Context) {
	for {
		g.pollOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		interval := memoryEvalInterval
		if sampleWantsGrow(g.latest, g.prevSwapUsed) {
			interval = memoryPressurePollInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-g.clock.After(interval):
		}
	}
}

func (g *Governor) pollOnce(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	sample, pollErr := g.telemetry.Poll(pollCtx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	if pollErr != nil {
		if !g.pollErrLogged {
			g.pollErrLogged = true
			slog.Warn("govern.poll_error", "err", pollErr)
		}
		return
	}
	g.pollErrLogged = false
	g.ingestSample(ctx, sample)
}

// ingestSample validates age and, if fresh, stores the sample and runs all axes (shared by stream and poll paths).
func (g *Governor) ingestSample(ctx context.Context, sample resize.Sample) {
	age := g.clock.Now().Sub(sample.Timestamp)
	if age < 0 {
		age = -age
	}
	if age > resize.SampleMaxAge {
		if !g.agentOutdated {
			g.agentOutdated = true
			slog.Warn("govern.sample_stale", "age", age, "max_age", resize.SampleMaxAge)
		}
		return
	}
	g.agentOutdated = false
	g.acceptSample(ctx, sample)
}

func (g *Governor) acceptSample(ctx context.Context, sample resize.Sample) {
	if g.latest.SwapTotalBytes > 0 && g.latest.SwapFreeBytes <= g.latest.SwapTotalBytes {
		g.prevSwapUsed = g.latest.SwapTotalBytes - g.latest.SwapFreeBytes
	} else {
		g.prevSwapUsed = 0
	}
	g.prevSwapInPages = g.latest.SwapInPages
	g.latest = sample
	g.lastSampleTime = g.clock.Now()
	for _, a := range g.axes {
		a.Evaluate(ctx)
	}
}

type vsockTelemetry struct {
	dialer driver.GuestDialer
	id     domain.SandboxID
}

func NewVsockTelemetry(dialer driver.GuestDialer, id domain.SandboxID) resize.TelemetrySource {
	return &vsockTelemetry{dialer: dialer, id: id}
}

func (v *vsockTelemetry) Poll(ctx context.Context) (resize.Sample, error) {
	conn, err := v.dialer.DialGuest(ctx, v.id, resize.TelemetryVsockPort)
	if err != nil {
		return resize.Sample{}, fmt.Errorf("govern: vsock dial port %d: %w", resize.TelemetryVsockPort, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return resize.Sample{}, fmt.Errorf("govern: set vsock deadline: %w", err)
		}
	}
	if err := resize.EncodeSampleRequest(conn); err != nil {
		return resize.Sample{}, fmt.Errorf("govern: send sample request: %w", err)
	}
	resp, err := resize.DecodeSampleResponse(conn)
	if err != nil {
		return resize.Sample{}, fmt.Errorf("govern: decode sample response: %w", err)
	}
	return resp.Sample, nil
}

// Stream implements TelemetryStream. Decodes the first frame synchronously; returns ErrStreamUnsupported when
// the guest closes without a frame (io.EOF) or sends "unknown kind", so Run falls back to Poll permanently.
// First-frame detection is bounded by a 10 s deadline so a hanging connection does not block the governor.
func (v *vsockTelemetry) Stream(ctx context.Context) (<-chan resize.Sample, <-chan error, error) {
	conn, err := v.dialer.DialGuest(ctx, v.id, resize.TelemetryVsockPort)
	if err != nil {
		return nil, nil, fmt.Errorf("govern: vsock dial port %d for stream: %w", resize.TelemetryVsockPort, err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("govern: stream setup deadline: %w", err)
	}
	if err := resize.EncodeStreamRequest(conn); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("govern: send stream request: %w", err)
	}

	dec := resize.NewStreamDecoder(conn)
	type firstResult struct {
		s   resize.Sample
		err error
	}
	firstCh := make(chan firstResult, 1)
	go func() {
		s, err := dec.Next()
		firstCh <- firstResult{s, err}
	}()

	var first firstResult
	select {
	case first = <-firstCh:
	case <-ctx.Done():
		conn.Close()
		return nil, nil, ctx.Err()
	}

	if first.err != nil {
		conn.Close()
		if isStreamUnsupportedErr(first.err) {
			return nil, nil, fmt.Errorf("govern: stream setup: %w", resize.ErrStreamUnsupported)
		}
		return nil, nil, fmt.Errorf("govern: stream first frame: %w", first.err)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("govern: clear stream deadline: %w", err)
	}

	sampleCh := make(chan resize.Sample, 16)
	errCh := make(chan error, 1)
	sampleCh <- first.s

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	go func() {
		defer close(sampleCh)
		defer close(errCh)
		for {
			s, err := dec.Next()
			if err != nil {
				if err != io.EOF && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, net.ErrClosed) {
					select {
					case errCh <- err:
					default:
					}
				}
				return
			}
			select {
			case sampleCh <- s:
			case <-ctx.Done():
				return
			}
		}
	}()

	return sampleCh, errCh, nil
}

func isStreamUnsupportedErr(err error) bool {
	if err == io.EOF || errors.Is(err, io.ErrClosedPipe) || resize.IsStreamUnsupported(err) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
