package main

// Telemetry server and resize-service coordinator for the guest auto-resize
// subsystem (AR-GA). This file is platform-agnostic; platform-specific work
// (sample collection, disk grow, CPU onliner, ZRAM, /tmp resize) is in the
// _linux.go / _other.go companions.
//
// Design: D-DC-10 (serve-and-poll, host connects per sample), D-DC-11
// (vsock port 3002). The server is intentionally stateless — each connection
// is one request→one reply → close.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/mdlayher/vsock"
	"github.com/IniZio/nexus3/internal/core/resize"
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
// memCeilingBytes is the boot-time RAM ceiling delivered via --mem-ceiling= on
// the kernel cmdline (TBD-DC-9, seam B). It is stored for future AR-DRV /
// governor use; /tmp sizing uses live MemTotal, not the ceiling.
func startResizeServices(ctx context.Context, con *os.File, workspacePath string, memCeilingBytes int64) {
	// ZRAM — synchronous, before the workload can start.
	setupZRAMSwap(con)

	// telemetry server — handles sample.request and disk.grow.
	go startResizeTelemetryServer(ctx, con, workspacePath)

	// vCPU onliner — brings hot-plugged CPUs online on a 3 s ticker.
	startCPUOnliner(ctx)

	// /tmp resizer — remounts /tmp tmpfs as live MemTotal grows.
	startTmpfsResizer(ctx, con)

	// memCeilingBytes is available for AR-DRV/governor; /tmp sizing ignores it.
	_ = memCeilingBytes
}

// startResizeTelemetryServer binds vsock port [resize.TelemetryVsockPort]
// (3002) and serves the resize wire protocol. Per D-DC-10 the host polls:
// it connects, sends one request, reads one reply, and closes. Two request
// kinds are handled per connection:
//
//   - "sample.request" → collectSample → "sample.response"
//   - "disk.grow"      → handleDiskGrow → "disk.grew"
//
// Runs until ctx is cancelled. Per-connection errors are logged and
// best-effort; a listener bind failure is logged and returns (the caller
// treats it as a non-fatal degradation when auto-resize is disabled).
func startResizeTelemetryServer(ctx context.Context, con *os.File, workspacePath string) {
	lis, err := vsock.Listen(resize.TelemetryVsockPort, nil)
	if err != nil {
		consoleLog(con, "nexus3-agent: resize-telemetry: vsock.Listen %d: %v\n",
			resize.TelemetryVsockPort, err)
		return
	}
	go func() {
		<-ctx.Done()
		lis.Close()
	}()
	consoleLog(con, "nexus3-agent: resize-telemetry: listening on vsock port %d\n",
		resize.TelemetryVsockPort)

	for {
		conn, err := lis.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			consoleLog(con, "nexus3-agent: resize-telemetry: accept: %v\n", err)
			return
		}
		go handleResizeConn(con, conn, workspacePath)
	}
}

// handleResizeConn reads one request from conn, dispatches it, and writes one
// reply. The connection is closed on return. All errors are best-effort logged.
func handleResizeConn(con *os.File, conn net.Conn, workspacePath string) {
	defer conn.Close()

	var env resizeEnvelope
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&env); err != nil {
		consoleLog(con, "nexus3-agent: resize-telemetry: decode envelope: %v\n", err)
		_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{
			Message: fmt.Sprintf("decode envelope: %v", err),
		})
		return
	}
	if env.Version != 1 {
		msg := fmt.Sprintf("version mismatch: got %d, want 1 (rebuild guest agent or host binary)", env.Version)
		consoleLog(con, "nexus3-agent: resize-telemetry: %s\n", msg)
		_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{Message: msg})
		return
	}

	switch env.Kind {
	case "sample.request":
		s, err := collectSample(workspacePath)
		if err != nil {
			consoleLog(con, "nexus3-agent: resize-telemetry: collectSample: %v\n", err)
			_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{
				Message: fmt.Sprintf("collectSample: %v", err),
			})
			return
		}
		if err := resize.EncodeSampleResponse(conn, resize.SampleResponse{Sample: s}); err != nil {
			consoleLog(con, "nexus3-agent: resize-telemetry: encode sample response: %v\n", err)
		}

	case "disk.grow":
		var req resize.GrowRequest
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			msg := fmt.Sprintf("unmarshal disk.grow payload: %v", err)
			consoleLog(con, "nexus3-agent: resize-telemetry: %s\n", msg)
			_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{Message: msg})
			return
		}
		resp := handleDiskGrow(req)
		if resp.Error != "" {
			consoleLog(con, "nexus3-agent: resize-telemetry: disk.grow index=%d: %s\n",
				req.DiskIndex, resp.Error)
		}
		if err := resize.EncodeGrowResponse(conn, resp); err != nil {
			consoleLog(con, "nexus3-agent: resize-telemetry: encode grow response: %v\n", err)
		}

	default:
		msg := fmt.Sprintf("unknown request kind %q", env.Kind)
		consoleLog(con, "nexus3-agent: resize-telemetry: %s\n", msg)
		_ = resize.EncodeErrorResponse(conn, resize.ErrorResponse{Message: msg})
	}
}
