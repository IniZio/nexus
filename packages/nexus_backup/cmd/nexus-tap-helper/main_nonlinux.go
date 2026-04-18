//go:build !linux

package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr, "nexus-tap-helper is only supported on Linux (current: %s)\n", runtime.GOOS)
	os.Exit(1)
}
