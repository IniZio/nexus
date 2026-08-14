// Package vmcfg resolves VM auto-resize boot configuration from caller-supplied
// sizing inputs. Both the real sandbox path and the ephemeral builder-VM path
// call Resolve so that ceiling defaults, floor enforcement, and PID-1 cmdline
// assembly have a single source of truth (UNI-CFG slice).
//
// Auto-resize is unconditional in nexus3 (D-DC-30 revised 2026-08-14): every
// VM receives a hotplug region at boot. There is no on/off flag.
package vmcfg

import (
	"fmt"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// Config holds the inputs for resolving VM auto-resize boot configuration.
// Zero values for the ceiling fields request the default 4× rule.
type Config struct {
	// BootMemMiB is the initial RAM allocation in MiB passed to the driver.
	// 0 is treated as the driver default (512 MiB).
	BootMemMiB uint32

	// BootVCPUs is the initial vCPU count passed to the driver.
	// 0 is treated as the driver default (1 vCPU).
	BootVCPUs uint32

	// MemMaxMiB is the explicit RAM ceiling in MiB.
	// 0 applies the default: max(4 × BootMemMiB, 4096 MiB).
	MemMaxMiB uint32

	// VCPUsMax is the explicit vCPU ceiling.
	// 0 applies the default: max(4 × BootVCPUs, 4).
	VCPUsMax uint32

	// DiskMaxGiB is the explicit disk-grow ceiling in GiB.
	// 0 applies the default: 100 GiB (matches OLD-nexus diskMaxBytes, D-DC-20).
	DiskMaxGiB uint32
}

// Result holds the resolved auto-resize boot configuration.
// All fields are ready to be assigned directly to a driver Config and cmdline.
type Result struct {
	// Bounds is the governor policy envelope passed to resize.Governor.
	Bounds resize.Bounds

	// MemoryMaxMiB is the resolved RAM ceiling in MiB.
	// Assign to driver.Config.MemoryMaxMiB; the driver uses it to size the
	// VirtioMem hotplug region and emit memhp kernel parameters (driver.go:65-66).
	MemoryMaxMiB uint32

	// VCPUMax is the resolved vCPU ceiling count.
	// Assign to driver.Config.VCPUMax.
	VCPUMax uint32

	// PID1Args is the PID-1 argv fragment for auto-resize.
	// It is formatted as " --mem-ceiling=<bytes>" (leading space included) and
	// belongs after "--" in the kernel cmdline.  The driver inserts the required
	// memhp kernel params (memhp_default_state=online,
	// memory_hotplug.online_policy=auto-movable) BEFORE "--" independently.
	PID1Args string
}

// Resolve computes VM auto-resize boot configuration from c.
//
// It enforces default ceiling rules (4× boot size with per-resource floors) and
// returns all derived values a caller needs to configure the driver and assemble
// the kernel cmdline.
//
// Ceiling defaults (applied when the corresponding Config field is 0):
//   - MemMaxMiB:  4× BootMemMiB, minimum 4096 MiB.
//     Rationale: the nested-build OOM workload that motivated auto-resize
//     consumed >4 GiB; 4096 MiB is the measured lower bound.  4× reaches
//     4096 MiB only when boot memory ≥ 1024 MiB; the floor prevents a
//     512 MiB default sandbox from getting only 2048 MiB.
//   - VCPUsMax:   4× BootVCPUs, minimum 4.
//   - DiskMaxGiB: 100 GiB.
//
// Driver defaults (substituted when BootMemMiB or BootVCPUs is 0):
//   - BootMemMiB = 0 → 512 MiB (driver.Config.MemoryMiB zero-value).
//   - BootVCPUs  = 0 → 1 vCPU (driver.Config.VCPUs zero-value).
func Resolve(c Config) Result {
	// Resolve driver defaults so ceiling multiples and floor checks are computed
	// against the actual VM sizing, not a raw zero.
	bootMem := c.BootMemMiB
	if bootMem == 0 {
		bootMem = 512 // driver default (Config.MemoryMiB = 0 → 512 MiB)
	}
	bootCPUs := c.BootVCPUs
	if bootCPUs == 0 {
		bootCPUs = 1 // driver default (Config.VCPUs = 0 → 1 vCPU)
	}

	// Ceiling defaults.
	memMax := c.MemMaxMiB
	if memMax == 0 {
		memMax = bootMem * 4
		if memMax < 4096 {
			memMax = 4096
		}
	}
	vcpuMax := c.VCPUsMax
	if vcpuMax == 0 {
		vcpuMax = bootCPUs * 4
		if vcpuMax < 4 {
			vcpuMax = 4
		}
	}
	diskMax := c.DiskMaxGiB
	if diskMax == 0 {
		diskMax = 100
	}

	bounds := resize.Bounds{
		MemMinBytes:  int64(bootMem) * 1024 * 1024,
		MemMaxBytes:  int64(memMax) * 1024 * 1024,
		VCPUMin:      int32(bootCPUs),
		VCPUMax:      int32(vcpuMax),
		DiskMaxBytes: int64(diskMax) * 1024 * 1024 * 1024,
	}

	return Result{
		Bounds:       bounds,
		MemoryMaxMiB: memMax,
		VCPUMax:      vcpuMax,
		// Leading space is intentional: the caller concatenates this after
		// "--" (e.g. diskBootCmdlineBase + " --" + result.PID1Args).
		PID1Args: fmt.Sprintf(" --mem-ceiling=%d", int64(memMax)*1024*1024),
	}
}
