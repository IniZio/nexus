package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/vmcfg"
	mcpsrv "github.com/IniZio/nexus3/internal/mcp"
)

func init() {
	Register(Command{
		Name:    "mcp",
		Summary: "Run an MCP server over stdio (JSON-RPC on stdin/stdout, diagnostics to stderr)",
		Run:     runMCP,
	})
}

// mcpService wraps *service.Service and satisfies mcpsrv.SandboxService,
// adding a CreateAndBoot method backed by service.CreateAndBoot. All other
// methods are promoted from the embedded *service.Service.
//
// The cacheRoot field mirrors the CLI's filepath.Join(storeRoot, "images")
// convention and is resolved once at MCP server startup.
type mcpService struct {
	*service.Service
	cacheRoot string
}

// CreateAndBoot implements mcpsrv.SandboxService. It opens the image cache,
// builds a per-call cloud-hypervisor driver factory via the shared
// buildSandboxDriverFactory seam, and delegates to service.CreateAndBoot.
// opts.CacheRoot is always set from m.cacheRoot (the tool does not expose
// cache_root as a parameter).
func (m *mcpService) CreateAndBoot(ctx context.Context, project, name string, opts service.CreateAndBootOptions) (domain.Sandbox, error) {
	// Preflight: validate the kernel path before image-cache or driver setup so
	// that a missing/misconfigured NEXUS3_KERNEL_PATH surfaces immediately with
	// an actionable error rather than after expensive work inside CreateAndBoot.
	kernelPath, err := resolveKernelPath()
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("sandbox create (mcp): %w", err)
	}

	opts.CacheRoot = m.cacheRoot

	imgCache, err := image.NewCache(m.cacheRoot)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("open image cache: %w", err)
	}
	// This is a long-lived server: a Cache holds one open flock per image it
	// writes, so a per-request Cache that is never closed leaks a descriptor
	// per request AND leaves those digests uncollectable by every other
	// process. Nothing between here and the return prunes, so releasing the
	// pins on the way out is safe.
	defer imgCache.Close() //nolint:errcheck

	// Resolve auto-resize bounds so MCP-created sandboxes carry the same
	// MemoryMaxMiB / VCPUMax hotplug configuration as CLI-created ones.
	ar := vmcfg.Resolve(vmcfg.Config{BootMemMiB: opts.MemoryMiB, BootVCPUs: opts.VCPUs})

	newDriver := buildSandboxDriverFactory(sandboxDriverSpec{
		KernelPath:   kernelPath,
		MemoryMiB:    opts.MemoryMiB,
		VCPUs:        opts.VCPUs,
		MemoryMaxMiB: ar.MemoryMaxMiB,
		VCPUMax:      ar.VCPUMax,
		NestedVirt:   opts.NestedVirt,
		PID1Args:     ar.PID1Args,
		SBHandle:     project + "/" + name,
	}, nil)

	return service.CreateAndBoot(ctx, m.Service, imgCache, newDriver, vsockProbe, project, name, opts)
}

// Exec implements mcpsrv.SandboxService.Exec. It captures stdout and stderr
// into in-memory buffers and returns them as strings along with the exit code.
// stdin, when non-empty, is piped to the guest process via strings.NewReader.
func (m *mcpService) Exec(ctx context.Context, ref string, argv []string, env map[string]string, cwd, stdin string) (int32, string, string, error) {
	var outBuf, errBuf bytes.Buffer
	execOpts := agent.ExecOptions{
		Argv:   argv,
		Env:    env,
		Cwd:    cwd,
		Stdin:  strings.NewReader(stdin),
		Stdout: &outBuf,
		Stderr: &errBuf,
	}
	code, err := m.Service.Exec(ctx, ref, execOpts)
	return code, outBuf.String(), errBuf.String(), err
}

// RunEphemeral implements mcpsrv.SandboxService.RunEphemeral. It uses the
// shared buildSandboxDriverFactory seam (identical wiring as CreateAndBoot),
// delegates to service.RunEphemeral, and returns captured output as strings.
func (m *mcpService) RunEphemeral(ctx context.Context, project, name string, opts service.CreateAndBootOptions, argv []string, env map[string]string, cwd, stdin string) (int32, string, string, error) {
	kernelPath, err := resolveKernelPath()
	if err != nil {
		return 0, "", "", fmt.Errorf("sandbox run (mcp): %w", err)
	}

	opts.CacheRoot = m.cacheRoot

	imgCache, err := image.NewCache(m.cacheRoot)
	if err != nil {
		return 0, "", "", fmt.Errorf("open image cache: %w", err)
	}
	// See CreateAndBoot: per-request Cache in a long-lived server must be
	// closed or its pins outlive the request.
	defer imgCache.Close() //nolint:errcheck

	// Resolve auto-resize bounds so MCP-run sandboxes carry MemoryMaxMiB /
	// VCPUMax hotplug configuration identical to the sandbox-create path.
	ar := vmcfg.Resolve(vmcfg.Config{BootMemMiB: opts.MemoryMiB, BootVCPUs: opts.VCPUs})

	newDriver := buildSandboxDriverFactory(sandboxDriverSpec{
		KernelPath:   kernelPath,
		MemoryMiB:    opts.MemoryMiB,
		VCPUs:        opts.VCPUs,
		MemoryMaxMiB: ar.MemoryMaxMiB,
		VCPUMax:      ar.VCPUMax,
		NestedVirt:   opts.NestedVirt,
		PID1Args:     ar.PID1Args,
		SBHandle:     project + "/" + name,
	}, nil)

	var outBuf, errBuf bytes.Buffer
	code, err := service.RunEphemeral(ctx, m.Service, imgCache, newDriver, vsockProbe,
		project, name, opts,
		service.ExecOptions{Argv: argv, Env: env, Cwd: cwd},
		strings.NewReader(stdin), &outBuf, &errBuf)
	return code, outBuf.String(), errBuf.String(), err
}

// runMCP is the registered Run function for the "mcp" subcommand.
//
// It initialises the sandbox service and runs an MCP server over this
// process's stdin/stdout. The StdioTransport owns stdout exclusively; nothing
// else may write to stdout while the server is running, or the JSON-RPC stream
// will be corrupted. All diagnostics and logging go to stderr.
//
// The function blocks until stdin is closed (EOF) and then returns nil.
// Any error from the server (e.g. malformed JSON-RPC) is logged to stderr and
// returned so that root.go exits with a non-zero code.
func runMCP(ctx context.Context, _ []string, _ *Output) error {
	svc, err := newSandboxService()
	if err != nil {
		// Pre-startup failure: stderr is safe here, server has not started.
		fmt.Fprintln(os.Stderr, "mcp: init service:", err)
		return &CodedError{Code: ErrCodeInternalError, Msg: "mcp: init service: " + err.Error(), Err: err}
	}

	// Resolve the image cache root using the same convention as the CLI create
	// command. Failure here is non-fatal: the server still runs and all tools
	// except sandbox_create with image params continue to work.
	cacheRoot := ""
	if storeRoot, err := store.DefaultRoot(); err == nil {
		cacheRoot = filepath.Join(storeRoot, "images")
	} else {
		fmt.Fprintln(os.Stderr, "mcp: warn: cannot resolve store root for image cache:", err)
	}

	msvc := &mcpService{Service: svc, cacheRoot: cacheRoot}
	srv := mcpsrv.NewServer(msvc)

	// server.Run blocks until stdin EOF (clean shutdown) and returns nil, or
	// returns an error on protocol failure. Either way stdout is JSON-RPC only.
	if err := srv.Run(ctx, &gosdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "mcp: server error:", err)
		return &CodedError{Code: ErrCodeInternalError, Msg: "mcp: " + err.Error(), Err: err}
	}
	return nil
}
