package main

// Telemetry server and resize-service coordinator for the guest auto-resize
// subsystem (AR-GA). This file is platform-agnostic; platform-specific work
// (sample collection, disk grow, CPU onliner, ZRAM, /tmp resize) is in the
// _linux.go / _other.go companions.
//
// Design: D-DC-10 (two transports share vsock port 3002):
//   (a) host-poll path — host opens a connection, writes "sample.request",
//       reads one "sample.response", and closes (one request → one reply).
//   (b) guest-push streaming — host opens a connection, writes "sample.stream";
//       the guest sends a "sample.response" frame immediately (Trigger=heartbeat),
//       then one frame per PSI trigger (Trigger=psi_mem or psi_cpu, leading-edge
//       coalesced to ≤1 per 500 ms), plus a heartbeat every 5 s, until either
//       side closes. Old guests that do not know "sample.stream" reply with an
//       ErrorResponse containing "unknown kind"; the host detects this via
//       resize.IsStreamUnsupported and falls back to poll.
// D-DC-11: vsock port 3002, adjacent to the port-forward mux (3001).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/IniZio/nexus/internal/core/resize"
	"github.com/mdlayher/vsock"
)

// resizeEnvelope mirrors the unexported resize/wire.envelope, used here to
// inspect the Kind before dispatching to the appropriate typed handler.
// Version and Kind are the only fields we need; Payload is dispatched by
// unmarshal into the correct concrete type.
type resizeEnvelope struct {
	Version int             `json:"v"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// psiWatcherFunc is the PSI trigger watcher factory used by serveStream.
// Replaced in tests to inject a fake trigger channel.
var psiWatcherFunc = newPSIWatcher

// startResizeServices starts all auto-resize subsystems. Auto-resize is
// unconditional: the agent starts these services whenever it runs as PID 1,
// with no opt-in token required. Call order matters:
//
//  1. setupZRAMSwap — synchronous, must complete before vsock listeners open
//     so compressed swap is active before any workload can start (spec-08:67,
//     §2.4 MUST). ZRAM converts a burst-OOM kill into a recoverable stall
//     while vm.resize completes.
//  2. startResizeTelemetryServer — vsock:3002 listener (goroutine).
//  3. startCPUOnliner — 3 s ticker bringing hot-plugged vCPUs online (goroutine).
//  4. startTmpfsResizer — 10 s ticker remounting /tmp as MemTotal grows (goroutine).
//
// disks is the list of resizable (index, mountPath) pairs for this VM, used by
// the telemetry server to report per-disk DiskStats on each sample poll.
//
// memCeilingBytes is the boot-time RAM ceiling delivered via --mem-ceiling= on
// the kernel cmdline (TBD-DC-9, seam B). It is stored for future AR-DRV /
// governor use; /tmp sizing uses live MemTotal, not the ceiling.
func startResizeServices(ctx context.Context, con *os.File, disks []resizableDisk, memCeilingBytes int64) {
	// ZRAM — synchronous, before the workload can start.
	setupZRAMSwap(con)

	// telemetry server — handles sample.request, sample.stream, and disk.grow.
	go startResizeTelemetryServer(ctx, con, disks)

	// vCPU onliner — brings hot-plugged CPUs online on a 3 s ticker.
	startCPUOnliner(ctx)

	// /tmp resizer — remounts /tmp tmpfs as live MemTotal grows.
	startTmpfsResizer(ctx, con)

	// memCeilingBytes is available for AR-DRV/governor; /tmp sizing ignores it.
	_ = memCeilingBytes
}

// startResizeTelemetryServer binds vsock port [resize.TelemetryVsockPort]
// (3002) and serves the resize wire protocol. Three request kinds are handled
// per connection:
//
//   - "sample.request" → collectSample → "sample.response" (one-shot, then close)
//   - "sample.stream"  → serveStream (long-lived push; see D-DC-10b)
//   - "disk.grow"      → handleDiskGrow → "disk.grew" (one-shot, then close)
//
// disks is the list of resizable (index, mountPath) pairs to report per-disk
// DiskStats for on each sample.request poll.
//
// Runs until ctx is cancelled. Per-connection errors are logged and
// best-effort; a listener bind failure is logged and returns (the caller
// treats it as a non-fatal degradation when auto-resize is disabled).
func startResizeTelemetryServer(ctx context.Context, con *os.File, disks []resizableDisk) {
	lis, err := vsock.Listen(resize.TelemetryVsockPort, nil)
	if err != nil {
		consoleLog(con, "nexus-agent: resize-telemetry: vsock.Listen %d: %v\n",
			resize.TelemetryVsockPort, err)
		return
	}
	go func() {
		<-ctx.Done()
		lis.Close()
	}()
	consoleLog(con, "nexus-agent: resize-telemetry: listening on vsock port %d\n",
		resize.TelemetryVsockPort)

	for {
		conn, err := lis.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			consoleLog(con, "nexus-agent: resize-telemetry: accept: %v\n", err)
			return
		}
		go handleResizeConn(con, conn, disks)
	}
}

// handleResizeConn reads one request from conn and dispatches it. For
// "sample.request" and "disk.grow" the connection is one-shot (one reply,
// then close). For "sample.stream" the connection is kept open by serveStream
// until a write error or until the underlying context ends. All errors are
// best-effort logged. disks is passed through to collectSample for per-disk
// telemetry. Unknown kinds return an ErrorResponse so old hosts fail cleanly.
func handleResizeConn(con *os.File, conn net.Conn, disks []resizableDisk) {
	defer conn.Close()

	var env resizeEnvelope
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&env); err != nil {
		consoleLog(con, "nexus-agent: resize-telemetry: decode envelope: %v\n", err)
		_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{
			Message: fmt.Sprintf("decode envelope: %v", err),
		})
		return
	}
	if env.Version != 1 {
		msg := fmt.Sprintf("version mismatch: got %d, want 1 (rebuild guest agent or host binary)", env.Version)
		consoleLog(con, "nexus-agent: resize-telemetry: %s\n", msg)
		_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{Message: msg})
		return
	}

	switch env.Kind {
	case "sample.request":
		s, err := collectSample(disks)
		if err != nil {
			consoleLog(con, "nexus-agent: resize-telemetry: collectSample: %v\n", err)
			_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{
				Message: fmt.Sprintf("collectSample: %v", err),
			})
			return
		}
		if err := resize.EncodeSampleResponse(conn, resize.SampleResponse{Sample: s}); err != nil {
			consoleLog(con, "nexus-agent: resize-telemetry: encode sample response: %v\n", err)
		}

	case "sample.stream":
		serveStream(context.Background(), con, conn, disks)

	case "disk.grow":
		var req resize.GrowRequest
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			msg := fmt.Sprintf("unmarshal disk.grow payload: %v", err)
			consoleLog(con, "nexus-agent: resize-telemetry: %s\n", msg)
			_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{Message: msg})
			return
		}
		resp := handleDiskGrow(req)
		if resp.Error != "" {
			consoleLog(con, "nexus-agent: resize-telemetry: disk.grow index=%d: %s\n",
				req.DiskIndex, resp.Error)
		}
		if err := resize.EncodeGrowResponse(conn, resp); err != nil {
			consoleLog(con, "nexus-agent: resize-telemetry: encode grow response: %v\n", err)
		}

	default:
		msg := fmt.Sprintf("unknown request kind %q", env.Kind)
		consoleLog(con, "nexus-agent: resize-telemetry: %s\n", msg)
		_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{Message: msg})
	}
}

// serveStream implements the D-DC-10b guest-push streaming path. It sends a
// "sample.response" frame immediately on connect (Trigger=heartbeat), then one
// frame whenever psiWatcherFunc fires a PSI event (Trigger=psi_mem or psi_cpu),
// plus a heartbeat every 5 s. Coalescing is leading-edge: the first trigger in
// any 500 ms window is sent immediately; subsequent triggers within the window
// are suppressed to avoid flooding the host governor. Returns when conn write
// fails or parentCtx is done. A derived context ensures the PSI watcher
// goroutine exits on return even when parentCtx is context.Background().
func serveStream(parentCtx context.Context, con *os.File, conn net.Conn, disks []resizableDisk) {
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	psiCh := psiWatcherFunc(ctx, con)
	hb := time.NewTicker(5 * time.Second)
	defer hb.Stop()

	if err := sendStreamFrame(con, conn, disks, resize.TriggerHeartbeat); err != nil {
		return
	}

	var lastTriggerSent time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case trigger, ok := <-psiCh:
			if !ok {
				return
			}
			if time.Since(lastTriggerSent) < 500*time.Millisecond {
				continue
			}
			lastTriggerSent = time.Now()
			if err := sendStreamFrame(con, conn, disks, trigger); err != nil {
				return
			}
		case <-hb.C:
			if err := sendStreamFrame(con, conn, disks, resize.TriggerHeartbeat); err != nil {
				return
			}
		}
	}
}

// sendStreamFrame collects a sample, sets its Trigger field, and encodes it
// as a "sample.response" envelope on conn. The frame is byte-identical to a
// one-shot "sample.request" reply, satisfying the StreamDecoder contract.
// Returns any encode error so the caller can stop the stream.
func sendStreamFrame(con *os.File, conn net.Conn, disks []resizableDisk, trigger string) error {
	s, err := collectSample(disks)
	if err != nil {
		consoleLog(con, "nexus-agent: resize-stream: collectSample: %v\n", err)
		return err
	}
	s.Trigger = trigger
	return resize.EncodeSampleResponse(conn, resize.SampleResponse{Sample: s})
}
