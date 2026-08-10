//go:build !linux

package agent

import (
	"context"
	"fmt"
)

// buildInGuestImageLinux is a stub on non-Linux platforms.
// BuildInGuestImage is a Linux-only operation (microVM management).
func buildInGuestImageLinux(_ context.Context, _ InGuestBuildOptions) error {
	return fmt.Errorf("BuildInGuestImage: not supported on this platform (Linux only)")
}
