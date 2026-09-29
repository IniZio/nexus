//go:build linux

// Package backends links the platform's driver backends and hosts their
// re-exec child dispatch.
package backends

import (
	"os"

	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
)

// MaybeRunChild runs a netns/virtiofsd child entry point and exits when this
// process was re-exec'd as one; otherwise it returns.
func MaybeRunChild() {
	if os.Getenv(cloudhypervisor.NetnsRunEnv) == "1" {
		cloudhypervisor.RunNetnsChild()
		os.Exit(0)
	}
	if os.Getenv(cloudhypervisor.VirtiofsRunEnv) == "1" {
		cloudhypervisor.RunVirtiofsdChild()
		os.Exit(0)
	}
}
