//go:build !linux

package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr, "nexus-firecracker-agent is only supported on Linux (current: %s)\n", runtime.GOOS)
	os.Exit(1)
}
