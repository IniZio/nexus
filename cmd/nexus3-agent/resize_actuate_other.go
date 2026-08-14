//go:build !linux

package main

// Stub implementations of resize actuators for non-Linux platforms (darwin,
// windows, etc.). The real implementations are in resize_actuate_linux.go.
// These stubs allow `GOOS=darwin go build ./...` to succeed for CI and
// developer cross-compile checks.

import (
	"context"
	"time"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// collectSample returns a minimal Sample with only Timestamp populated. PSI,
// disk, and vCPU fields are zero/false — the host governor must not act on
// them when running against a non-Linux stub.
func collectSample(_ string) (resize.Sample, error) {
	return resize.Sample{Timestamp: time.Now().UTC()}, nil
}

// handleDiskGrow reports "not supported on this platform" so the host governor
// can log the error without panicking.
func handleDiskGrow(req resize.GrowRequest) resize.GrowResponse {
	return resize.GrowResponse{
		Error: "resize2fs not supported on this platform",
	}
}

// startCPUOnliner is a no-op on non-Linux platforms.
func startCPUOnliner(_ context.Context) {}
