//go:build linux

package cli

import (
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
)

func newCHDriver(binaryPath, kernelPath, diskDir string) (driver.Driver, error) {
	return cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: binaryPath,
		KernelPath: kernelPath,
		DiskDir:    diskDir,
	})
}
