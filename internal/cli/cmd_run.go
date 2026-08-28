package cli

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/vmcfg"
)

func init() {
	Register(Command{
		Name:    "run",
		Summary: "Create a sandbox, run a command, and remove it — guaranteed cleanup on exit",
		Run:     runRun,
	})
}

// runRun implements `nexus3 run [flags] <image-ref> -- <command> [args...]`.
//
// Flags:
//
//	--memory MiB   guest RAM in mebibytes (default: driver default)
//	--vcpus N      number of virtual CPUs (default: driver default)
//	--name NAME    sandbox name suffix (default: generated)
//	--project P    sandbox project (default: "ephemeral")
func runRun(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		memoryFlag  = fs.Uint("memory", 0, "guest RAM in MiB (0 = driver default)")
		vcpusFlag   = fs.Uint("vcpus", 0, "number of virtual CPUs (0 = driver default)")
		nameFlag    = fs.String("name", "", "sandbox name (default: generated)")
		projectFlag = fs.String("project", "ephemeral", "sandbox project")
		forceFlag   = fs.Bool("force", false, "skip the disk-space preflight")
	)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "run: " + err.Error()}
	}

	positional := fs.Args()
	if len(positional) < 2 {
		return &UsageError{Msg: "run: usage: run [flags] <image-ref> -- <command> [args...]"}
	}

	imageRef := positional[0]
	// Strip the conventional "--" separator: flag.Parse stops at the first
	// positional and never consumes it. See stripArgvSeparator.
	argv := stripArgvSeparator(positional[1:])

	// Resolve kernel path before any expensive work (preflight validation).
	kernelPath, err := resolveKernelPath()
	if err != nil {
		return errSandbox("run", fmt.Errorf("kernel: %w", err))
	}

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "run: " + err.Error(), Err: err}
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return errSandbox("run", fmt.Errorf("resolve state directory: %w", err))
	}
	cacheRoot := filepath.Join(storeRoot, "images")

	imgCache, err := image.NewCache(cacheRoot)
	if err != nil {
		return errSandbox("run", fmt.Errorf("open image cache: %w", err))
	}

	// Generate a sandbox name when none is provided.
	name := *nameFlag
	if name == "" {
		name = fmt.Sprintf("run-%08x", rand.Uint32())
	}

	memoryMiB := uint32(*memoryFlag)
	vcpus := uint32(*vcpusFlag)

	// Resolve auto-resize bounds — mirrors cmd_sandbox.go:runSandboxCreate.
	// DiskMaxGiB, MemMaxMiB, VCPUsMax left at zero so defaults apply.
	ar := vmcfg.Resolve(vmcfg.Config{
		BootMemMiB: memoryMiB,
		BootVCPUs:  vcpus,
	})

	// newDriver constructs a CHDriver instance for the resolved ext4 path.
	// Mirrors the closure in cmd_sandbox.go:runSandboxCreate exactly:
	// buildCHConfig is called, auto-resize ceilings are stamped, extra disks
	// are forwarded, and cloud-hypervisor is located in PATH.
	newDriver := func(ext4Path string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		cfg := buildCHConfig(kernelPath, ext4Path, memoryMiB, vcpus)
		cfg.MemoryMaxMiB = ar.MemoryMaxMiB
		cfg.VCPUMax = ar.VCPUMax
		cfg.Cmdline = diskBootCmdlineBase + " --" + ar.PID1Args
		for _, ed := range extraDisks {
			cfg.ExtraDisks = append(cfg.ExtraDisks, cloudhypervisor.ExtraDisk{Path: ed.Path})
		}
		if p, binErr := exec.LookPath("cloud-hypervisor"); binErr == nil {
			cfg.BinaryPath = p
		}
		return cloudhypervisor.New(cfg)
	}

	// Locate the nexus3-agent binary to inject into OCI images on a cache miss.
	// mirrors cmd_sandbox.go's lookup; best-effort — a nil slice is fine for
	// images already in the cache and will produce a clear error on a cache miss.
	var agentBytes []byte
	if agentBin, lookErr := exec.LookPath("nexus3-agent"); lookErr == nil {
		agentBytes, _ = os.ReadFile(agentBin)
	}

	opts := service.CreateAndBootOptions{
		Image:               service.ImageSpec{Ref: imageRef},
		CacheRoot:           cacheRoot,
		AgentBytes:          agentBytes,
		MemoryMiB:           memoryMiB,
		VCPUs:               vcpus,
		ReachabilityTimeout: 30 * time.Second,
		ForceDiskSpace:      *forceFlag,
	}

	// probe polls vsock until the guest agent is listening or ReachabilityTimeout
	// expires. A single-shot attempt is not enough: CH's vsock multiplexer returns
	// EOF while the virtio-vsock device is still being negotiated by the guest
	// (typically < 1 s after vm.boot returns), so a retry loop is required.
	// Mirrors cmd_sandbox.go's probe closure exactly.
	probe := func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		gd, ok := drv.(driver.GuestDialer)
		if !ok {
			return nil
		}
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			conn, err := gd.DialGuest(dialCtx, id, driver.AgentControlPort)
			cancel()
			if err == nil {
				_ = conn.Close()
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	exitCode, runErr := service.RunEphemeral(
		ctx,
		svc,
		imgCache,
		newDriver,
		probe,
		*projectFlag,
		name,
		opts,
		argv,
		os.Stdin,
		os.Stdout,
		os.Stderr,
	)
	if runErr != nil {
		return errSandbox("run", runErr)
	}
	if exitCode != 0 {
		return &ExitCodeError{Code: exitCode}
	}
	return nil
}
