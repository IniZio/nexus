//go:build !linux

package supervisor

import (
	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/govern"
)

// governClock is the governor's time source; serve_adopted.go owns it on Linux.
var governClock govern.Clock

// RunAdopt is unsupported off Linux.
func RunAdopt(cfg Config, handoffSockPath string) error {
	return registry.Unsupported(registry.CloudHypervisor, "supervisor adopt")
}

// RunReacquire is unsupported off Linux.
func RunReacquire(cfg Config) error {
	return registry.Unsupported(registry.CloudHypervisor, "supervisor reacquire")
}
