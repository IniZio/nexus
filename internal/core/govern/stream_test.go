package govern

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/resize"
)

// safeFakeClock is a goroutine-safe fake clock for stream tests, where the
// governor goroutine calls After() concurrently with the test calling Advance().
type safeFakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

func newSafeFakeClock() *safeFakeClock {
	return &safeFakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *safeFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *safeFakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{deadline: c.now.Add(d), ch: ch})
	return ch
}

func (c *safeFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

// fakeStreamTelemetry implements TelemetrySource (Poll) and TelemetryStream.
type fakeStreamTelemetry struct {
	pollSample resize.Sample
	pollErr    error

	mu     sync.Mutex
	opens  int
	openFn func(n int) (<-chan resize.Sample, <-chan error, error)
	ctxs   []context.Context
}

func (f *fakeStreamTelemetry) Poll(_ context.Context) (resize.Sample, error) {
	return f.pollSample, f.pollErr
}

func (f *fakeStreamTelemetry) Stream(ctx context.Context) (<-chan resize.Sample, <-chan error, error) {
	f.mu.Lock()
	f.opens++
	n := f.opens
	f.ctxs = append(f.ctxs, ctx)
	f.mu.Unlock()
	return f.openFn(n)
}

func (f *fakeStreamTelemetry) Opens() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func (f *fakeStreamTelemetry) StreamCtx(n int) context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= 1 && n <= len(f.ctxs) {
		return f.ctxs[n-1]
	}
	return nil
}

type notifyResizer struct {
	*fakeResizer
	once   sync.Once
	signal chan struct{}
}

func newNotifyResizer(bootBytes int64) *notifyResizer {
	return &notifyResizer{fakeResizer: newFakeResizer(bootBytes), signal: make(chan struct{})}
}

func (r *notifyResizer) ResizeMemory(ctx context.Context, targetBytes int64) (int64, error) {
	ret, err := r.fakeResizer.ResizeMemory(ctx, targetBytes)
	r.once.Do(func() { close(r.signal) })
	return ret, err
}

func (r *notifyResizer) CurrentMemoryBytes() int64 { return r.fakeResizer.CurrentMemoryBytes() }

func TestStreamTriggeredSampleEvaluatesImmediately(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newNotifyResizer(boot)
	clk := newSafeFakeClock()

	sampleCh := make(chan resize.Sample, 1)
	errCh := make(chan error, 1)
	stream := &fakeStreamTelemetry{
		openFn: func(_ int) (<-chan resize.Sample, <-chan error, error) {
			return sampleCh, errCh, nil
		},
	}

	g := New(Config{
		Resizer:   resizer,
		Telemetry: stream,
		Headroom:  &fakeHeadroom{ok: true},
		Bounds:    resize.Bounds{MemMinBytes: boot, MemMaxBytes: boot * 4},
		Clock:     clk,
	})

	s := growSample(uint64(boot))
	s.Timestamp = clk.Now()
	s.Trigger = resize.TriggerPSIMemory
	sampleCh <- s

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	select {
	case <-resizer.signal:
	case <-time.After(2 * time.Second):
		t.Fatal("evaluate did not fire — triggered sample should react without any clock advance")
	}
	if len(resizer.calls) < 1 {
		t.Errorf("expected ≥1 resize call after triggered sample; got %d", len(resizer.calls))
	}
}

func TestStreamUnsupportedFallsToPoll(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newNotifyResizer(boot)
	clk := newSafeFakeClock()

	stream := &fakeStreamTelemetry{
		openFn: func(_ int) (<-chan resize.Sample, <-chan error, error) {
			return nil, nil, fmt.Errorf("govern: stream setup: %w", resize.ErrStreamUnsupported)
		},
	}
	ps := growSample(uint64(boot))
	ps.Timestamp = clk.Now()
	stream.pollSample = ps

	g := New(Config{
		Resizer:   resizer,
		Telemetry: stream,
		Headroom:  &fakeHeadroom{ok: true},
		Bounds:    resize.Bounds{MemMinBytes: boot, MemMaxBytes: boot * 4},
		Clock:     clk,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	select {
	case <-resizer.signal:
	case <-time.After(2 * time.Second):
		t.Fatal("poll fallback did not fire resize after ErrStreamUnsupported")
	}
	if stream.Opens() != 1 {
		t.Errorf("Stream should be called once; got %d", stream.Opens())
	}
}

func TestStreamWatchdogFiresAndReconnects(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newFakeResizer(boot)
	clk := newSafeFakeClock()

	reconnected := make(chan struct{}, 1)
	stream := &fakeStreamTelemetry{
		openFn: func(n int) (<-chan resize.Sample, <-chan error, error) {
			sampleCh := make(chan resize.Sample)
			errCh := make(chan error, 1)
			if n >= 2 {
				select {
				case reconnected <- struct{}{}:
				default:
				}
			}
			return sampleCh, errCh, nil
		},
	}

	g := New(Config{
		Resizer:   resizer,
		Telemetry: stream,
		Headroom:  &fakeHeadroom{ok: true},
		Bounds:    resize.Bounds{MemMinBytes: boot, MemMaxBytes: boot * 4},
		Clock:     clk,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	time.Sleep(30 * time.Millisecond)
	clk.Advance(streamWatchdog + time.Second)

	time.Sleep(30 * time.Millisecond)
	clk.Advance(streamBackoffMin + time.Second)

	select {
	case <-reconnected:
	case <-time.After(2 * time.Second):
		t.Fatalf("stream did not reconnect after watchdog fired; opens=%d", stream.Opens())
	}

	firstCtx := stream.StreamCtx(1)
	if firstCtx == nil {
		t.Fatal("no context captured for first stream open")
	}
	select {
	case <-firstCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first stream ctx was not cancelled after watchdog reconnect")
	}
}

func TestStreamErrorReconnectsAfterBackoff(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newFakeResizer(boot)
	clk := newSafeFakeClock()

	reconnected := make(chan struct{}, 1)
	stream := &fakeStreamTelemetry{
		openFn: func(n int) (<-chan resize.Sample, <-chan error, error) {
			sampleCh := make(chan resize.Sample)
			errCh := make(chan error, 1)
			if n == 1 {
				errCh <- errors.New("transport broken")
				close(sampleCh)
			} else {
				select {
				case reconnected <- struct{}{}:
				default:
				}
			}
			return sampleCh, errCh, nil
		},
	}

	g := New(Config{
		Resizer:   resizer,
		Telemetry: stream,
		Headroom:  &fakeHeadroom{ok: true},
		Bounds:    resize.Bounds{MemMinBytes: boot, MemMaxBytes: boot * 4},
		Clock:     clk,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	time.Sleep(30 * time.Millisecond)
	clk.Advance(streamBackoffMin + time.Second)

	select {
	case <-reconnected:
	case <-time.After(2 * time.Second):
		t.Fatalf("stream did not reconnect after error+backoff; opens=%d", stream.Opens())
	}
	if stream.Opens() < 2 {
		t.Errorf("expected ≥2 Stream() opens; got %d", stream.Opens())
	}
}

func TestPollFallbackPollsImmediately(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newNotifyResizer(boot)
	clk := newSafeFakeClock()

	streamDead := make(chan struct{})
	stream := &fakeStreamTelemetry{
		openFn: func(n int) (<-chan resize.Sample, <-chan error, error) {
			sampleCh := make(chan resize.Sample)
			errCh := make(chan error, 1)
			if n == 1 {
				close(sampleCh)
				select {
				case streamDead <- struct{}{}:
				default:
				}
			}
			return sampleCh, errCh, nil
		},
	}
	ps := growSample(uint64(boot))
	ps.Timestamp = clk.Now()
	stream.pollSample = ps

	g := New(Config{
		Resizer:   resizer,
		Telemetry: stream,
		Headroom:  &fakeHeadroom{ok: true},
		Bounds:    resize.Bounds{MemMinBytes: boot, MemMaxBytes: boot * 4},
		Clock:     clk,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	select {
	case <-streamDead:
	case <-time.After(time.Second):
		t.Fatal("first stream did not close")
	}

	select {
	case <-resizer.signal:
	case <-time.After(2 * time.Second):
		t.Fatal("pollFallback did not poll immediately after stream dead; no resize without clock advance")
	}
}

// fakeDialer implements driver.GuestDialer using a caller-supplied function.
type fakeDialer struct {
	dialFn func(ctx context.Context) (net.Conn, error)
}

func (f *fakeDialer) DialGuest(ctx context.Context, _ domain.SandboxID, _ uint32) (net.Conn, error) {
	return f.dialFn(ctx)
}

func TestVsockStreamFirstFrameEOFIsTransient(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	go func() {
		defer server.Close()
		buf := make([]byte, 256)
		server.Read(buf) //nolint:errcheck
	}()

	dialer := &fakeDialer{dialFn: func(_ context.Context) (net.Conn, error) { return client, nil }}
	v := &vsockTelemetry{dialer: dialer, id: domain.SandboxID{}}

	_, _, err := v.Stream(context.Background())
	if err == nil {
		t.Fatal("Stream() returned nil error; want transient error")
	}
	if resize.IsStreamUnsupported(err) {
		t.Errorf("Stream() returned ErrStreamUnsupported; want transient (not permanent downgrade)")
	}
	if !errors.Is(err, errStreamSetupTransient) {
		t.Errorf("Stream() error = %v; want errStreamSetupTransient", err)
	}
}

func TestStreamTransientRetriesAndSucceeds(t *testing.T) {
	t.Parallel()
	const boot = 2 * gib
	resizer := newNotifyResizer(boot)
	clk := newSafeFakeClock()

	successCh := make(chan resize.Sample)
	successErrCh := make(chan error, 1)
	thirdCallSeen := make(chan struct{}, 1)

	stream := &fakeStreamTelemetry{
		openFn: func(n int) (<-chan resize.Sample, <-chan error, error) {
			if n <= 2 {
				return nil, nil, errStreamSetupTransient
			}
			select {
			case thirdCallSeen <- struct{}{}:
			default:
			}
			return successCh, successErrCh, nil
		},
	}
	ps := growSample(uint64(boot))
	ps.Timestamp = clk.Now()
	stream.pollSample = ps

	g := New(Config{
		Resizer:   resizer,
		Telemetry: stream,
		Headroom:  &fakeHeadroom{ok: true},
		Bounds:    resize.Bounds{MemMinBytes: boot, MemMaxBytes: boot * 4},
		Clock:     clk,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	select {
	case <-resizer.signal:
	case <-time.After(2 * time.Second):
		t.Fatal("poll did not fire resize during backoff after first transient error")
	}

	time.Sleep(20 * time.Millisecond)
	clk.Advance(streamBackoffMin + time.Second)

	time.Sleep(20 * time.Millisecond)
	clk.Advance(2*streamBackoffMin + time.Second)

	select {
	case <-thirdCallSeen:
	case <-time.After(2 * time.Second):
		t.Fatalf("governor did not reach third Stream() call; opens=%d", stream.Opens())
	}
	if stream.Opens() < 3 {
		t.Errorf("expected ≥3 Stream() opens; got %d", stream.Opens())
	}
}
