//go:build linux

package supervisor

import (
	"path/filepath"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/resize"
)

func newSupervisorDriver(cfg Config, memMaxMiB, vcpuMax uint32) (supervisorDriver, error) {
	extraDisks := make([]cloudhypervisor.ExtraDisk, 0, len(cfg.ExtraDisks))
	for _, p := range cfg.ExtraDisks {
		extraDisks = append(extraDisks, cloudhypervisor.ExtraDisk{Path: p})
	}
	return cloudhypervisor.New(buildSupervisorDriverConfig(cfg, memMaxMiB, vcpuMax, extraDisks))
}

func newSandboxResizer(drv supervisorDriver, id domain.SandboxID, bounds resize.Bounds, bootMemBytes int64, bootVCPUs int32) (supervisorResizer, error) {
	return cloudhypervisor.NewSandboxResizer(drv.(*cloudhypervisor.CHDriver), id, bounds, bootMemBytes, bootVCPUs), nil
}

// buildSupervisorDriverConfig assembles the cloudhypervisor.Config the detached
// supervisor boots its VM with.
//
// It exists as a separate function so a test can observe the config WITHOUT
// booting a VM. That matters because of the bug this function was extracted to
// prevent: VCPUs was simply absent from this literal, so the driver fell back
// to its 1-vCPU default while VCPUMax still advertised the hotplug ceiling.
// Every supervisor-backed sandbox therefore booted with exactly one CPU and
// N-1 empty slots (/sys/devices/system/cpu present=0, possible=0-15) no matter
// what --vcpus asked for.
//
// It stayed invisible because BootVCPUs WAS forwarded over argv as
// --boot-vcpus and WAS consumed by the resize governor, so the argv test kept
// passing: it asserted the value was TRANSPORTED, never that it reached the VM.
// A test over this function closes the config half only — that the supervisor
// actually calls it is proven by booting a sandbox and reading nproc.
func buildSupervisorDriverConfig(
	cfg Config,
	memMaxMiB, vcpuMax uint32,
	extraDisks []cloudhypervisor.ExtraDisk,
) cloudhypervisor.Config {
	return cloudhypervisor.Config{
		BinaryPath:        cfg.CHBin,
		SocketDir:         cfg.SocketDir,
		KernelPath:        cfg.KernelPath,
		DiskImagePath:     cfg.DiskPath,
		StartTimeout:      30 * time.Second,
		MemoryMiB:         cfg.MemoryMiB,
		MemoryMaxMiB:      memMaxMiB,
		VCPUs:             cfg.BootVCPUs, // boot_vcpus — see doc comment above
		VCPUMax:           vcpuMax,
		ExtraDisks:        extraDisks,
		Cmdline:           cfg.Cmdline,
		LiveMounts:        cfg.LiveMounts,
		VirtiofsdPath:     cfg.VirtiofsdPath,
		FreePageReporting: true,
		NestedVirt:        cfg.NestedVirt,
		ConsoleLogPath:    filepath.Join(cfg.StateDir, "console.log"),
	}
}
