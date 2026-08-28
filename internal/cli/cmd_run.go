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

	// buildSandboxDriverFactory wires MemoryMaxMiB, VCPUMax, cmdline, and
	// orcaSocketDir — identical to the sandbox-create path. No live mounts or
	// SBHandle are needed for ephemeral run sandboxes.
	newDriver := buildSandboxDriverFactory(sandboxDriverSpec{
		KernelPath:   kernelPath,
		MemoryMiB:    memoryMiB,
		VCPUs:        vcpus,
		MemoryMaxMiB: ar.MemoryMaxMiB,
		VCPUMax:      ar.VCPUMax,
		PID1Args:     ar.PID1Args,
	}, nil)

	// Locate the nexus3-agent binary to inject into OCI images on a cache miss.
	// D2: a present-but-unreadable binary must surface a clear error rather than
	// silently passing a nil slice (which produces a misleading "no agent binary"
	// error downstream on a cache miss).
	var agentBytes []byte
	if agentBin, lookErr := exec.LookPath("nexus3-agent"); lookErr == nil {
		agentBytes, err = os.ReadFile(agentBin)
		if err != nil {
			return errSandbox("run", fmt.Errorf("found nexus3-agent but cannot read %s: %w", agentBin, err))
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
