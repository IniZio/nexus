//go:build !linux

package main

// Stub /tmp tmpfs resizer for non-Linux platforms. The real implementation is
// in resize_tmp_linux.go. This stub allows `GOOS=darwin go build ./...` to succeed.

import (
	"context"
	"os"
)

// startTmpfsResizer is a no-op on non-Linux platforms. tmpfs remounting is a
// Linux-specific operation.
func startTmpfsResizer(_ context.Context, _ *os.File) {}
