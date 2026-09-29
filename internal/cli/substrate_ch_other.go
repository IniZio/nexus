//go:build !linux

package cli

import (
	"errors"

	"github.com/IniZio/nexus/internal/core/driver"
)

func newCHDriver(_, _, _ string) (driver.Driver, error) {
	return nil, errors.New("cloud-hypervisor backend requires linux")
}
