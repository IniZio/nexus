//go:build !linux

package agent

import "fmt"

// MountWorkspace is a stub on non-Linux platforms.
func MountWorkspace(_ []GuestMount) error {
	return fmt.Errorf("MountWorkspace: not supported on this platform (Linux only)")
}
