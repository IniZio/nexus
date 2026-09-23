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

	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/core/vmcfg"
)

func init() {
	Register(Command{
		Name:    "run",
		Summary: "Create a sandbox, run a command, and remove it — guaranteed cleanup on exit",
		Run:     runRun,
	})
}

// runRun implements `nexus run [flags] <image-ref> -- <command> [args...]`.
func runRun(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		memoryFlag    = fs.Uint("memory", 0, "guest RAM in MiB — hard cap when set without --memory-max (0 = driver default with 4× hotplug headroom)")
		memoryMaxFlag = fs.Uint("memory-max", 0, "RAM ceiling for hotplug in MiB (0 = pin at --memory; ignored when --memory is 0)")
		vcpusFlag     = fs.Uint("vcpus", 0, "number of virtual CPUs — hard cap when set without --vcpus-max (0 = driver default with 4× hotplug headroom)")
		vcpusMaxFlag  = fs.Uint("vcpus-max", 0, "vCPU ceiling for hotplug (0 = pin at --vcpus; ignored when --vcpus is 0)")
		nameFlag      = fs.String("name", "", "sandbox name (default: generated)")
		projectFlag   = fs.String("project", "ephemeral", "sandbox project")
		forceFlag     = fs.Bool("force", false, "skip the disk-space preflight")
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
	memoryMaxMiB := uint32(*memoryMaxFlag)
	vcpus := uint32(*vcpusFlag)
	vcpusMax := uint32(*vcpusMaxFlag)

	sizing, sizingErr := resolveRunSizing(memoryMiB, memoryMaxMiB, vcpus, vcpusMax)
	if sizingErr != nil {
		return sizingErr
	}

	newDriver := buildSandboxDriverFactory(sandboxDriverSpec{
		KernelPath:   kernelPath,
		MemoryMiB:    memoryMiB,
		VCPUs:        vcpus,
		MemoryMaxMiB: sizing.DriverMemoryMaxMiB,
		VCPUMax:      sizing.DriverVCPUMax,
		PID1Args:     sizing.PID1Args,
	}, nil)

	// Locate the nexus-agent binary to inject into OCI images on a cache miss.
	// D2: a present-but-unreadable binary must surface a clear error rather than
	// silently passing a nil slice (which produces a misleading "no agent binary"
	// error downstream on a cache miss).
	var agentBytes []byte
	if agentBin, lookErr := exec.LookPath("nexus-agent"); lookErr == nil {
		agentBytes, err = os.ReadFile(agentBin)
		if err != nil {
			return errSandbox("run", fmt.Errorf("found nexus-agent but cannot read %s: %w", agentBin, err))
		}
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

	// vsockProbe polls until the guest agent is listening or ReachabilityTimeout
	// expires — shared with MCP and sandbox-create paths via cmd_seam.go.
	exitCode, runErr := service.RunEphemeral(
		ctx,
		svc,
		imgCache,
		newDriver,
		vsockProbe,
		*projectFlag,
		name,
		opts,
		service.ExecOptions{Argv: argv},
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

type runSizingResult struct {
	PID1Args           string
	DriverMemoryMaxMiB uint32
	DriverVCPUMax      uint32
}

func resolveRunSizing(memoryMiB, memoryMaxMiB, vcpus, vcpusMax uint32) (runSizingResult, error) {
	if memoryMaxMiB > 0 && memoryMiB > 0 && memoryMaxMiB < memoryMiB {
		return runSizingResult{}, &UsageError{Msg: fmt.Sprintf(
			"run: --memory-max %d MiB is less than --memory %d MiB", memoryMaxMiB, memoryMiB)}
	}
	if vcpusMax > 0 && vcpus > 0 && vcpusMax < vcpus {
		return runSizingResult{}, &UsageError{Msg: fmt.Sprintf(
			"run: --vcpus-max %d is less than --vcpus %d", vcpusMax, vcpus)}
	}

	cfg := vmcfg.Config{BootMemMiB: memoryMiB, BootVCPUs: vcpus}
	if memoryMiB > 0 && memoryMaxMiB == 0 {
		cfg.MemMaxMiB = memoryMiB
	} else {
		cfg.MemMaxMiB = memoryMaxMiB
	}
	if vcpus > 0 && vcpusMax == 0 {
		cfg.VCPUsMax = vcpus
	} else {
		cfg.VCPUsMax = vcpusMax
	}

	ar := vmcfg.Resolve(cfg)

	effectiveBoot := memoryMiB
	if effectiveBoot == 0 {
		effectiveBoot = 512
	}
	driverMem := uint32(0)
	if ar.MemoryMaxMiB > effectiveBoot {
		driverMem = ar.MemoryMaxMiB
	}

	effectiveCPUs := vcpus
	if effectiveCPUs == 0 {
		effectiveCPUs = 1
	}
	driverVCPU := uint32(0)
	if ar.VCPUMax > effectiveCPUs {
		driverVCPU = ar.VCPUMax
	}

	return runSizingResult{
		PID1Args:           ar.PID1Args,
		DriverMemoryMaxMiB: driverMem,
		DriverVCPUMax:      driverVCPU,
	}, nil
}
