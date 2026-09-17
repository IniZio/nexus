//go:build !linux

package main

import (
	"fmt"
	"os"
)

// runSupervisorMain is a stub for non-Linux platforms. The detached supervisor
// is Linux-only (it relies on Setsid, /proc, and Linux namespaces).
func runSupervisorMain(_ []string) {
	fmt.Fprintln(os.Stderr, "supervisor: detached supervisor is not supported on this platform")
	os.Exit(1)
}
