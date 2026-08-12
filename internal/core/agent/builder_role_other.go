//go:build !linux

package agent

import (
	"context"
	"fmt"
)

// RunBuilderRole is a stub on non-Linux platforms.
func RunBuilderRole(_ context.Context, _ BuilderRoleOptions) error {
	return fmt.Errorf("RunBuilderRole: not supported on this platform (Linux only)")
}
