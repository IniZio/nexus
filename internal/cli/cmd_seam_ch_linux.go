//go:build linux

package cli

import (
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
)

// newSeamCHDriver extends newCHDriverFromParams with the boot-only knobs
// (nested virt, kernel cmdline) that chDriverParams does not carry.
func newSeamCHDriver(p chDriverParams, nested bool, cmdline string) (driver.Driver, string, error) {
	cfg := buildCHConfig(p.KernelPath, p.DiskPath, p.MemoryMiB, p.VCPUs)
	cfg.NestedVirt = nested
	cfg.MemoryMaxMiB = p.MemoryMaxMiB
	cfg.VCPUMax = p.VCPUMax
	for _, path := range p.ExtraDiskPaths {
		cfg.ExtraDisks = append(cfg.ExtraDisks, cloudhypervisor.ExtraDisk{Path: path})
	}
	var virtiofsdPath string
	if len(p.LiveMounts) > 0 {
		vp, err := wireLiveMountsToConfig(&cfg, p.LiveMounts)
		if err != nil {
			return nil, "", err
		}
		virtiofsdPath = vp
	}
	cfg.Cmdline = cmdline
	cfg.SocketDir = p.SocketDir
	cfg.BinaryPath = p.BinaryPath
	d, err := cloudhypervisor.New(cfg)
	return d, virtiofsdPath, err
}
