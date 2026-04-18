//go:build !linux

package main

// maybeInstallFirecracker is a no-op on non-Linux platforms.
// macOS uses the Lima-hosted firecracker instance.
func maybeInstallFirecracker() error {
	return nil
}
