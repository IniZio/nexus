//go:build !linux

package cli

import (
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/registry"
)

func newSeamCHDriver(chDriverParams, bool, string) (driver.Driver, string, error) {
	return nil, "", registry.Unsupported(registry.CloudHypervisor, "cloud-hypervisor driver")
}
