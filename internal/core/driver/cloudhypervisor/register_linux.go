//go:build linux

package cloudhypervisor

import (
	"fmt"

	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/registry"
)

func init() {
	registry.Register(registry.CloudHypervisor, func(cfg any) (driver.Driver, error) {
		c, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("cloudhypervisor: config must be cloudhypervisor.Config, got %T", cfg)
		}
		return New(c)
	})
}
