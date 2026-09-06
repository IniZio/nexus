package govern

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/resize"
)

// Clock abstracts wall time for testability. All time-dependent code in the
// governor uses clock.Now() and clock.After() so tests can inject a controlled
// timeline without sleeping or spinning on real timers.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// realClock is the production Clock backed by the real wall clock.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// AxisEvaluator is the hook the poll loop calls after each valid telemetry
// sample. Each resize axis (memory, disk, CPU) implements one and registers
// it via Governor.RegisterAxis before Run.
//
// disk.go and cpu.go add axes without editing memory.go or loop.go — that
// is the seam that prevents parallel-slice collision on safety-critical code.
type AxisEvaluator interface {
	Evaluate(ctx context.Context)
}

// axisEvalFunc adapts a func(context.Context) to AxisEvaluator so the memory
// axis can be registered as a plain method value.
type axisEvalFunc func(ctx context.Context)

func (f axisEvalFunc) Evaluate(ctx context.Context) { f(ctx) }

// Governor is the single-tenant auto-resize control loop for one sandbox.
//
// It polls the guest for telemetry over vsock (D-DC-10, D-DC-11) and calls
// each registered AxisEvaluator. The memory axis (memory.go) applies its
// control law and guards grows against host RAM exhaustion via HasHeadroom
// (hostheadroom.go); the disk and vCPU axes apply their own control laws
// without a RAM headroom check.
//
// Single-tenant design: OLD-nexus maintains a workspaceID-keyed map of states
// for N workspaces. nexus3 drops the map entirely — one Governor per supervisor
// process, one sandbox per supervisor, no workspaceID parameters anywhere
// (D-DC-12).
//
// Construct with New and run with Run.
type Governor struct {
	resizer   resize.MemoryResizer
	telemetry resize.TelemetrySource
	headroom  HostHeadroomReader
	bounds    resize.Bounds
	clock     Clock

	// transient control-law state — all fields accessed only from the Run
	// goroutine; no locking required.
	growCount           int
	shrinkCount         int
	lastResizeTime      time.Time
	lastResizeWasShrink bool
	latest              resize.Sample
	lastSampleTime      time.Time
	// prevSwapUsed is the SwapUsed (bytes) from the sample before g.latest.
	// Used by sampleWantsGrow as the reference point for the flow gate
	// (D-RAM-10): grow fires only when SwapUsed has increased since the
	// previous sample. Updated immediately before g.latest is overwritten each
	// poll cycle so evaluate() always sees the prior sample's value.
	prevSwapUsed        uint64
	agentOutdated       bool
	pollErrLogged       bool
	axes                []AxisEvaluator
}

// Config is the Governor's construction parameters.
type Config struct {
	// Resizer is the MemoryResizer to call for grow and shrink operations.
	// Must not be nil.
	Resizer resize.MemoryResizer

	// Telemetry is the source of guest telemetry samples. Must not be nil.
	// In production: NewVsockTelemetry(drv, id). In tests: a fake.
	Telemetry resize.TelemetrySource

	// Headroom checks host memory availability before each memory-axis grow.
	// When nil, NewProcfsHeadroom() is used (reads /proc/meminfo on the host).
	// The disk and vCPU axes do not call this reader.
	Headroom HostHeadroomReader

	// Bounds carries the per-sandbox resource ceilings. When MemMinBytes or
	// MemMaxBytes is zero (or min >= max), the governor runs in passive mode
	// (it polls but never resizes).
	Bounds resize.Bounds

	// Clock controls time for testability. When nil, realClock is used.
	Clock Clock
}

// New constructs a Governor from cfg.
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
	// Wire the memory axis. disk.go and cpu.go call RegisterAxis to add theirs
	// without editing memory.go or loop.go.
	g.axes = []AxisEvaluator{axisEvalFunc(g.evaluate)}
	return g
}

// RegisterAxis appends a to the governor's per-sample evaluation chain.
// Must be called before Run. disk.go and cpu.go use this to attach their
// axes without editing memory.go or loop.go.
func (g *Governor) RegisterAxis(a AxisEvaluator) {
	g.axes = append(g.axes, a)
}

// Run starts the adaptive polling loop. It blocks until ctx is cancelled.
// Intended to run as a goroutine inside the detached supervisor process.
//
// Boot delay (memoryResizeBootDelay = 10 s) gives the guest time to settle
// before the first sample; this mirrors OLD memoryResizeBootDelay.
//
// Adaptive interval: 5 s nominal, 2 s once a sample shows pressure.
func (g *Governor) Run(ctx context.Context) {
	// Guard: skip the poll loop when bounds are unconfigured. Without this gate
	// every sandbox that does not opt into auto-resize dials vsock:3002 on every
	// poll cycle and fills the log with govern.poll_error (~17k/day per sandbox).
	// Passive-mode logic inside evaluate() is not sufficient: it only suppresses
	// resizes, not polls. One Info line is all that is needed.
	if g.bounds.MemMinBytes == 0 || g.bounds.MemMaxBytes == 0 ||
		g.bounds.MemMinBytes >= g.bounds.MemMaxBytes {
		slog.Info("govern.loop.skipped", "reason", "bounds_not_configured")
		return
	}

	// Boot delay.
	select {
	case <-ctx.Done():
		return
	case <-g.clock.After(memoryResizeBootDelay):
	}

	slog.Info("govern.loop.started",
		"min_bytes", g.bounds.MemMinBytes,
		"max_bytes", g.bounds.MemMaxBytes,
	)

	interval := memoryEvalInterval
	for {
		// Poll with a timeout smaller than the eval interval.
		pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		sample, pollErr := g.telemetry.Poll(pollCtx)
		cancel()

		if ctx.Err() != nil {
			return
		}

		if pollErr != nil {
			// Dedup poll_error: log only on first occurrence, clear on success.
			// Follows the same agentOutdated pattern used for govern.sample_stale
			// nine lines below — consistent treatment for the two transient fault
			// modes in this loop.
			if !g.pollErrLogged {
				g.pollErrLogged = true
				slog.Warn("govern.poll_error", "err", pollErr)
			}
		} else {
			g.pollErrLogged = false
			// Validate sample age. Reject stale samples to prevent a
			// stale-sample resize cascade after VM suspend/resume.
			age := g.clock.Now().Sub(sample.Timestamp)
			if age < 0 {
				age = -age // tolerate small clock skew
			}
			if age > resize.SampleMaxAge {
				if !g.agentOutdated {
					g.agentOutdated = true
					slog.Warn("govern.sample_stale",
						"age", age,
						"max_age", resize.SampleMaxAge,
					)
				}
			} else {
				g.agentOutdated = false
				// Capture SwapUsed from the current g.latest BEFORE
				// overwriting it. This becomes the previous-sample reference
				// point for the flow gate in sampleWantsGrow (D-RAM-10).
				if g.latest.SwapTotalBytes > 0 && g.latest.SwapFreeBytes <= g.latest.SwapTotalBytes {
					g.prevSwapUsed = g.latest.SwapTotalBytes - g.latest.SwapFreeBytes
				} else {
					g.prevSwapUsed = 0
				}
				g.latest = sample
				g.lastSampleTime = g.clock.Now()
				for _, a := range g.axes {
					a.Evaluate(ctx)
				}
			}
		}

		// Adaptive interval: fast-poll while the guest shows grow pressure.
		// The interval is read from g.latest AFTER evaluate returns, so the first
		// shrink sample following a grow sample is evaluated at the 2s cadence
		// (not 5s). Because the memory axis uses count-based shrink tracking, a
		// 5-sample shrink run that immediately follows a grow sample spans
		// 2+5+5+5+5=22s rather than the nominal 4×5=20s: a ~10% deviation, once
		// per grow→shrink transition. CPU is unaffected (time-based window).
		if sampleWantsGrow(g.latest, g.prevSwapUsed) {
			interval = memoryPressurePollInterval
		} else {
			interval = memoryEvalInterval
		}

		select {
		case <-ctx.Done():
			return
		case <-g.clock.After(interval):
		}
	}
}

// vsockTelemetry implements resize.TelemetrySource via the proven DialGuest
// path (HB-P7, D-DC-10). It dials vsock port resize.TelemetryVsockPort (3002)
// on every Poll call, sends a SampleRequest, and decodes the SampleResponse.
//
// There is no OLD-nexus equivalent: OLD used event-driven guest→host push;
// nexus3 uses host→guest polling so no host-side hybrid-vsock listener is
// needed.
type vsockTelemetry struct {
	dialer driver.GuestDialer
	id     domain.SandboxID
}

// NewVsockTelemetry returns a resize.TelemetrySource that polls the guest via
// vsock using the DialGuest path (D-DC-10, proven in production by the
// port-forward mux and sshd bridge).
func NewVsockTelemetry(dialer driver.GuestDialer, id domain.SandboxID) resize.TelemetrySource {
	return &vsockTelemetry{dialer: dialer, id: id}
}

// Poll implements resize.TelemetrySource.
func (v *vsockTelemetry) Poll(ctx context.Context) (resize.Sample, error) {
	conn, err := v.dialer.DialGuest(ctx, v.id, resize.TelemetryVsockPort)
	if err != nil {
		return resize.Sample{}, fmt.Errorf("govern: vsock dial port %d: %w",
			resize.TelemetryVsockPort, err)
	}
	defer conn.Close()

	// Apply context deadline to the connection so we don't block indefinitely.
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
