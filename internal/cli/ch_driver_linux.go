//go:build linux

package cli

import (
	"errors"
	"fmt"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
)

// chDriverParams is the CH-type-free description of a cloud-hypervisor driver.
type chDriverParams struct {
	KernelPath, DiskPath, SocketDir, BinaryPath string
	MemoryMiB, VCPUs                            uint32
	MemoryMaxMiB, VCPUMax                       uint32
	ExtraDiskPaths                              []string
	LiveMounts                                  []domain.LiveMount
}

func newCHDriverFromParams(p chDriverParams) (driver.Driver, error) {
	cfg := buildCHConfig(p.KernelPath, p.DiskPath, p.MemoryMiB, p.VCPUs)
	cfg.SocketDir = p.SocketDir
	cfg.BinaryPath = p.BinaryPath
	cfg.MemoryMaxMiB = p.MemoryMaxMiB
	cfg.VCPUMax = p.VCPUMax
	for _, path := range p.ExtraDiskPaths {
		cfg.ExtraDisks = append(cfg.ExtraDisks, cloudhypervisor.ExtraDisk{Path: path})
	}
	if len(p.LiveMounts) > 0 {
		if _, err := wireLiveMountsToConfig(&cfg, p.LiveMounts); err != nil {
			return nil, err
		}
	}
	return cloudhypervisor.New(cfg)
}

func isChMissingKernelErr(err error) bool {
	return errors.Is(err, cloudhypervisor.ErrNoKernelConfigured)
}

func isChNoRootDiskErr(err error) bool {
	return errors.Is(err, cloudhypervisor.ErrNoRootDisk)
}

func chVirtiofsTag(i int) string { return cloudhypervisor.VirtiofsTag(i) }

func buildCHConfig(kernelPath, ext4Path string, memMiB, vcpus uint32) cloudhypervisor.Config {
	cfg := cloudhypervisor.Config{
		KernelPath:    kernelPath,
		DiskImagePath: ext4Path,
	}
	if memMiB > 0 {
		cfg.MemoryMiB = memMiB
	}
	if vcpus > 0 {
		cfg.VCPUs = vcpus
	}
	return cfg
}

func wireLiveMountsToConfig(cfg *cloudhypervisor.Config, mounts []domain.LiveMount) (virtiofsdPath string, err error) {
	cfg.LiveMounts = mounts
	if len(mounts) == 0 {
		return "", nil
	}
	vres, verr := resolveVirtiofsdPath()
	if verr != nil {
		return "", fmt.Errorf("--mount requires virtiofsd: %w", verr)
	}
	cfg.VirtiofsdPath = vres.Path
	return vres.Path, nil
}
