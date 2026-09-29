//go:build !linux

package cli

import (
	"fmt"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/registry"
)

type chDriverParams struct {
	KernelPath, DiskPath, SocketDir, BinaryPath string
	MemoryMiB, VCPUs                            uint32
	MemoryMaxMiB, VCPUMax                       uint32
	ExtraDiskPaths                              []string
	LiveMounts                                  []domain.LiveMount
}

func newCHDriverFromParams(chDriverParams) (driver.Driver, error) {
	return nil, registry.Unsupported(registry.CloudHypervisor, "cloud-hypervisor driver")
}

func isChMissingKernelErr(error) bool { return false }

func isChNoRootDiskErr(error) bool { return false }

func chVirtiofsTag(i int) string { return fmt.Sprintf("nxfs%d", i) }
