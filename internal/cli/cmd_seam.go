package cli

import (
	"context"
	"time"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/hostbin"
	"github.com/IniZio/nexus/internal/core/service"
)

// vsockProbe is the shared vsock back-off probe used by all boot paths
// (sandbox create, MCP create/run, and nexus run).
// Poll with 300 ms back-off: CH's vsock multiplexer returns EOF while the
// virtio-vsock device is still being negotiated by the guest.
func vsockProbe(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
	gd, ok := drv.(driver.GuestDialer)
	if !ok {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := gd.DialGuest(dialCtx, id, driver.AgentControlPort)
		cancel()
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// sandboxDriverSpec holds the resolved parameters for building a CH driver.
type sandboxDriverSpec struct {
	KernelPath   string
	MemoryMiB    uint32
	VCPUs        uint32
	MemoryMaxMiB uint32 // 0 → driver default
	VCPUMax      uint32 // 0 → driver default
	NestedVirt   bool
	PID1Args     string // from vmcfg.Resolve; "" → no extra pid1 args
	SBHandle     string // "project/name" for cmdline; "" → omit handle
	HostHome     string
	LiveMounts   []domain.LiveMount // nil for MCP/run paths
	GuestMounts  []agent.GuestMount // nil for MCP/run paths
	// HasScratchDisk is true when a scratch disk was actually attached as the
	// last ExtraDisk. Set from the same condition that controls service scratch
	// creation (workspace present && !NoScratchDisk). Do not infer from
	// GuestMounts — live mounts and no-scratch sandboxes have mounts but no
	// scratch disk, and pointing mkfs at the wrong device is data loss (D-SD-01).
	HasScratchDisk bool
}

// sandboxDriverCaptures is populated by buildSandboxDriverFactory on each
// factory invocation. Pass non-nil only on the sandbox-create path where
// supervisor handoff needs the resolved values.
type sandboxDriverCaptures struct {
	DiskPath      string
	ExtraDisks    []string
	VirtiofsdPath string
	SocketDir     string
	Cmdline       string
	CHBin         string
}

// buildSandboxDriverFactory returns a service.DriverFactory that produces a
// fully-wired CHDriver for each ext4 image — the single authoritative CH
// driver construction path across sandbox create, MCP create/run, and run.
//
// When caps is non-nil each call populates it with the resolved values for
// supervisor handoff (sandbox create path only).
func buildSandboxDriverFactory(spec sandboxDriverSpec, caps *sandboxDriverCaptures) service.DriverFactory {
	return func(ext4Path string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		p := chDriverParams{
			KernelPath: spec.KernelPath, DiskPath: ext4Path,
			MemoryMiB: spec.MemoryMiB, VCPUs: spec.VCPUs,
			LiveMounts: spec.LiveMounts,
		}
		effectiveBootMem := spec.MemoryMiB
		if effectiveBootMem == 0 {
			effectiveBootMem = 512
		}
		if spec.MemoryMaxMiB > effectiveBootMem {
			p.MemoryMaxMiB = spec.MemoryMaxMiB
		}
		effectiveBootCPUs := spec.VCPUs
		if effectiveBootCPUs == 0 {
			effectiveBootCPUs = 1
		}
		if spec.VCPUMax > effectiveBootCPUs {
			p.VCPUMax = spec.VCPUMax
		}
		var capturedExtras []string
		for _, ed := range extraDisks {
			p.ExtraDiskPaths = append(p.ExtraDiskPaths, ed.Path)
			capturedExtras = append(capturedExtras, ed.Path)
		}
		// Assemble kernel cmdline.
		// When SBHandle is set or GuestMounts are present, use guestBootCmdline.
		// Otherwise fall back to the simple disk-boot base + PID1Args form that
		// ephemeral/run paths use (preserves pre-existing behavior there).
		var cmdline string
		if spec.SBHandle != "" || len(spec.GuestMounts) > 0 {
			// scratchIdx is -1 unless a scratch disk was explicitly attached.
			// Use spec.HasScratchDisk — not len(GuestMounts) — as the guard:
			// live-mount-only and NoScratchDisk sandboxes have mounts but no
			// scratch disk; pointing mkfs at the wrong device is data loss (D-SD-01).
			// Invariant when true: scratch is always len(ExtraDisks)-1 (D-DC-32).
			scratchIdx := -1
			if spec.HasScratchDisk {
				scratchIdx = len(p.ExtraDiskPaths) - 1
			}
			cmdline = guestBootCmdline(spec.GuestMounts, spec.PID1Args, spec.SBHandle, scratchIdx, spec.HostHome)
		} else if spec.PID1Args != "" {
			cmdline = diskBootCmdlineBase + " --" + spec.PID1Args
		}
		// Resolve socket directory consistently across CLI, MCP, and supervisor.
		var socketDir string
		if sd, err := orcaSocketDir(); err == nil {
			p.SocketDir = sd
			socketDir = sd
		}
		// Resolve cloud-hypervisor binary.
		if bin, err := hostbin.Resolve(context.Background(), hostbin.CloudHypervisor); err == nil {
			p.BinaryPath = bin
		}
		drv, virtiofsdPath, err := newSeamCHDriver(p, spec.NestedVirt, cmdline)
		if err != nil {
			return nil, err
		}
		// Populate captures for supervisor handoff (sandbox create path only).
		if caps != nil {
			caps.DiskPath = ext4Path
			caps.ExtraDisks = capturedExtras
			caps.VirtiofsdPath = virtiofsdPath
			caps.SocketDir = socketDir
			caps.Cmdline = cmdline
			caps.CHBin = p.BinaryPath
		}
		return drv, nil
	}
}
