//go:build linux

package main

import (
	"fmt"
	"os"
)

// firecrackerInstallPath is the destination path for the firecracker binary.
// It is a package-level variable so tests can override it.
var firecrackerInstallPath = "/usr/local/bin/firecracker"

// maybeInstallFirecracker writes the embedded firecracker binary to
// firecrackerInstallPath if it is not already present. It returns an error
// if the embedded binary is empty (unsupported architecture) or if the write
// fails (e.g. permission denied). The daemon must not start if this fails.
func maybeInstallFirecracker() error {
	if len(embeddedFirecracker) == 0 {
		return fmt.Errorf("no embedded firecracker binary for this architecture; run 'nexus init' to install firecracker manually")
	}

	if _, err := os.Stat(firecrackerInstallPath); err == nil {
		// Already installed — idempotent.
		return nil
	}

	if err := os.WriteFile(firecrackerInstallPath, embeddedFirecracker, 0o755); err != nil {
		return fmt.Errorf("install firecracker to %s: %w", firecrackerInstallPath, err)
	}

	return nil
}
