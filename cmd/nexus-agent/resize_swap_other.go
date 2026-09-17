//go:build !linux

package main

// Stub ZRAM swap setup for non-Linux platforms. The real implementation is in
// resize_swap_linux.go. This stub allows `GOOS=darwin go build ./...` to succeed.

import "os"

// setupZRAMSwap is a no-op on non-Linux platforms. ZRAM is a Linux kernel
// feature; there is no cross-platform equivalent.
func setupZRAMSwap(_ *os.File) {}
