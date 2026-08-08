package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	bkclient "github.com/moby/buildkit/client"
)

// SolveRequest is the fully-resolved build specification handed to a
// [BuildkitClient]. It carries everything needed to describe one complete
// build without any filesystem traversal on the client side.
type SolveRequest struct {
	// BaseRef is the OCI image reference used as the FROM base, e.g.
	// "debian:bookworm-slim".
	BaseRef string

	// ContainerfileBytes is the content of the project's .nexus/Containerfile,
	// applied on top of BaseRef.
	ContainerfileBytes []byte

	// AgentPath is the host filesystem path to the nexus3-agent binary.
	AgentPath string

	// AgentInstallPath is the absolute in-guest path where the agent binary is
	// placed as the final layer. Always "/sbin/nexus3-agent" in production.
	AgentInstallPath string
}

// BuildkitClient is the seam between [Builder] and a running buildkitd daemon.
//
// # Production implementation
//
// [realBuildkitClient] wraps github.com/moby/buildkit/client. It synthesises a
// combined Dockerfile (user's Containerfile + agent-as-final-layer COPY) and
// drives the build via the Dockerfile frontend, exporting the result as a local
// filesystem directory.
//
// # Test implementation
//
// Tests supply a fake that records the [SolveRequest] and populates outDir with
// a minimal synthetic tree. No buildkitd process or network access is required.
//
// # Seam contract
//
// Solve MUST:
//   - Start the build from req.BaseRef.
//   - Apply req.ContainerfileBytes instructions on top of the base.
//   - Install req.AgentPath as req.AgentInstallPath (mode 0755) as the final
//     layer, encoding the boot contract init=<AgentInstallPath>.
//   - Write the resulting POSIX filesystem tree into outDir (caller-owned).
type BuildkitClient interface {
	// Solve drives one build against a remote buildkitd, writing the resulting
	// filesystem tree into outDir. outDir is created by the caller; Solve
	// populates it.
	Solve(ctx context.Context, req SolveRequest, outDir string) error
}

// realBuildkitClient wraps github.com/moby/buildkit/client and implements
// [BuildkitClient].
//
// # Bootstrap note
//
// Calling Solve requires a running buildkitd reachable at c.addr. The
// intended runtime topology is:
//
//  1. nexus3 host calls driver.Start with the builder VM image
//     (images/builder/Containerfile — stock moby/buildkit) and waits for
//     the VM to report Running.
//  2. The host dials buildkitd's gRPC socket via driver.GuestDialer (vsock
//     or TCP forward) and obtains a host-side address.
//  3. That address is passed here as c.addr.
//  4. After the build, the host calls driver.Stop on the builder VM.
//
// This lifecycle (boot → connect → build → stop) is the integration slice
// that wires Builder with internal/core/driver; it is out of scope for S2.
type realBuildkitClient struct {
	addr string
}

// NewBuildkitClient constructs a [BuildkitClient] that connects to buildkitd
// at addr on each Solve call. addr must be in the form accepted by
// github.com/moby/buildkit/client.New, e.g.
//   - "unix:///run/buildkit/buildkitd.sock"
//   - "tcp://127.0.0.1:1234"
func NewBuildkitClient(addr string) (BuildkitClient, error) {
	if addr == "" {
		return nil, fmt.Errorf("buildkit: addr is required")
	}
	return &realBuildkitClient{addr: addr}, nil
}

// Solve implements [BuildkitClient].
//
// It connects to buildkitd, synthesises a combined Dockerfile that applies
// req.ContainerfileBytes on top of req.BaseRef and then installs the agent
// binary as the final layer, then drives the build via the Dockerfile v0
// frontend and exports the filesystem into outDir.
//
// The synthetic Dockerfile follows the pattern:
//
//	<content of req.ContainerfileBytes>
//
//	# Final layer: bake the nexus3-agent (boot contract: init=/sbin/nexus3-agent)
//	COPY --chmod=0755 nexus3-agent <req.AgentInstallPath>
//
// Placing the agent COPY last means an agent version bump only invalidates
// that single layer; all Containerfile layers above are cache-hits in buildkitd.
func (c *realBuildkitClient) Solve(ctx context.Context, req SolveRequest, outDir string) error {
	// Connect to buildkitd (one connection per Solve; acceptable for the
	// current slice — a connection pool is an optimisation for later).
	bk, err := bkclient.New(ctx, c.addr)
	if err != nil {
		return fmt.Errorf("buildkit: connect %s: %w", c.addr, err)
	}
	defer bk.Close()

	// Synthesise a combined Dockerfile: user instructions + agent final layer.
	finalLayer := fmt.Sprintf(
		"\n\n# Final layer: bake the nexus3-agent (boot contract: init=%s)\nCOPY --chmod=0755 nexus3-agent %s\n",
		req.AgentInstallPath, req.AgentInstallPath,
	)
	synthDF := append(append([]byte(nil), req.ContainerfileBytes...), []byte(finalLayer)...)

	// Build context directory: holds the synthetic Dockerfile and the agent
	// binary (referenced by the COPY instruction).
	ctxDir, err := os.MkdirTemp("", "nexus3-bkctx-*")
	if err != nil {
		return fmt.Errorf("buildkit: create context dir: %w", err)
	}
	defer os.RemoveAll(ctxDir)

	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), synthDF, 0600); err != nil {
		return fmt.Errorf("buildkit: write Dockerfile: %w", err)
	}

	// Copy the agent binary into the build context so the COPY instruction
	// can resolve it.
	agentBytes, err := os.ReadFile(req.AgentPath)
	if err != nil {
		return fmt.Errorf("buildkit: read agent binary %s: %w", req.AgentPath, err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "nexus3-agent"), agentBytes, 0755); err != nil {
		return fmt.Errorf("buildkit: write agent to context: %w", err)
	}

	_, err = bk.Solve(ctx, nil, bkclient.SolveOpt{
		Frontend: "dockerfile.v0",
		FrontendAttrs: map[string]string{
			// Tell the Dockerfile frontend which file to use (default is
			// "Dockerfile" but being explicit avoids any path ambiguity).
			"filename": "Dockerfile",
		},
		// LocalDirs is deprecated in favour of LocalMounts, but it remains
		// fully supported in moby/buildkit v0.18 and its replacement
		// (fsutil.FS) would add a heavier import for no benefit here.
		LocalDirs: map[string]string{
			"context":    ctxDir,
			"dockerfile": ctxDir,
		},
		Exports: []bkclient.ExportEntry{
			{
				Type:      bkclient.ExporterLocal,
				OutputDir: outDir,
			},
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("buildkit: solve: %w", err)
	}
	return nil
}
