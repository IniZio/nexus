package cli

import (
	"context"
	"os/exec"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/service"
)

// vsockProbe is the shared vsock back-off probe used by all boot paths
// (sandbox create, MCP create/run, and nexus3 run).
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
	MemoryMaxMiB uint32            // 0 → driver default
	VCPUMax      uint32            // 0 → driver default
	NestedVirt   bool
	PID1Args     string            // from vmcfg.Resolve; "" → no extra pid1 args
	SBHandle     string            // "project/name" for cmdline; "" → omit handle
	LiveMounts   []domain.LiveMount  // nil for MCP/run paths
	GuestMounts  []agent.GuestMount  // nil for MCP/run paths
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
		cfg := buildCHConfig(spec.KernelPath, ext4Path, spec.MemoryMiB, spec.VCPUs)
		cfg.NestedVirt = spec.NestedVirt
		if spec.MemoryMaxMiB > 0 {
			cfg.MemoryMaxMiB = spec.MemoryMaxMiB
		}
		if spec.VCPUMax > 0 {
			cfg.VCPUMax = spec.VCPUMax
		}
		var capturedExtras []string
		for _, ed := range extraDisks {
			cfg.ExtraDisks = append(cfg.ExtraDisks, cloudhypervisor.ExtraDisk{Path: ed.Path})
			capturedExtras = append(capturedExtras, ed.Path)
		}
		// Wire virtiofs live mounts (sandbox create --mount path only).
		var virtiofsdPath string
		if len(spec.LiveMounts) > 0 {
			vp, verr := wireLiveMountsToConfig(&cfg, spec.LiveMounts)
			if verr != nil {
				return nil, verr
			}
			virtiofsdPath = vp
		}
		// Assemble kernel cmdline.
		// When SBHandle is set or GuestMounts are present, use guestBootCmdline.
		// Otherwise fall back to the simple disk-boot base + PID1Args form that
		// ephemeral/run paths use (preserves pre-existing behavior there).
		if spec.SBHandle != "" || len(spec.GuestMounts) > 0 {
			cfg.Cmdline = guestBootCmdline(spec.GuestMounts, spec.PID1Args, spec.SBHandle)
		} else if spec.PID1Args != "" {
			cfg.Cmdline = diskBootCmdlineBase + " --" + spec.PID1Args
		}
		// Resolve socket directory consistently across CLI, MCP, and supervisor.
		var socketDir string
		if sd, err := orcaSocketDir(); err == nil {
			cfg.SocketDir = sd
			socketDir = sd
		}
		// Resolve cloud-hypervisor binary.
		if p, err := exec.LookPath("cloud-hypervisor"); err == nil {
			cfg.BinaryPath = p
		}
		// Populate captures for supervisor handoff (sandbox create path only).
		if caps != nil {
			caps.DiskPath = ext4Path
			caps.ExtraDisks = capturedExtras
			caps.VirtiofsdPath = virtiofsdPath
			caps.SocketDir = socketDir
			caps.Cmdline = cfg.Cmdline
			caps.CHBin = cfg.BinaryPath
		}
		return cloudhypervisor.New(cfg)
	}
}
