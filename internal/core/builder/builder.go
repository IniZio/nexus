// Package builder produces a bootable guest rootfs by driving stock buildkitd
// via BuildKit's Go client, from an OCI base plus a repo-committed
// .nexus/Containerfile, baking nexus3-agent as the final layer, and exporting
// one raw ext4 artifact into the image cache keyed by SHA-256 digest.
//
// # Build flow
//
//  1. Read the project-committed .nexus/Containerfile from the workspace root.
//  2. Verify the nexus3-agent binary exists at Config.AgentBinaryPath.
//  3. Drive the buildkitd via the [BuildkitClient] seam — a narrow interface
//     that isolates all buildkitd protocol details — producing a populated
//     filesystem directory.
//  4. Export that directory to a raw ext4 image (ext4.go) and hand it to the
//     image cache keyed by its SHA-256 digest.
//
// # buildkitd endpoint
//
// Running builds requires a buildkitd process. The intended topology is:
//
//	nexus3 host boots a builder VM (images/builder/Containerfile — stock
//	moby/buildkit image) via the driver seam (internal/core/driver), then
//	connects to buildkitd at the address passed in Config.BuildkitdAddr.
//	Forwarding options: vsock relay, guest-visible TCP port, or direct
//	Unix socket exposure (host-only). The full VM-boot + connect lifecycle is
//	implemented in the integration slice that wires Builder with
//	driver.Start and driver.GuestDialer; until then the endpoint is
//	caller-supplied.
//
// # Bootstrap circularity (flag — not resolved in this slice)
//
// The builder VM image (images/builder/Containerfile) must itself be converted
// to a raw ext4 rootfs before nexus3 can boot it and reach buildkitd. That
// first-time conversion cannot use this package — circular dependency: you
// need a running buildkitd to build a rootfs, but you need the builder rootfs
// to boot buildkitd.
//
// Resolved approach from ticket 14: ship the builder rootfs as a pre-built
// release artifact (built once out-of-band with plain docker/podman and
// converted with "mke2fs -d"). On first run, nexus3 downloads the builder
// rootfs. This package is never responsible for building its own host image.
package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// containerfilePath is the workspace-relative path to the project's build
// definition. The file must exist in the workspace root before Build is called.
const containerfilePath = ".nexus/Containerfile"

// agentInstallPath is the absolute in-guest path where the nexus3-agent binary
// is placed as the final layer of every built rootfs.
//
// Boot contract: the Linux kernel command line must include init=/sbin/nexus3-agent
// so that the guest kernel boots directly into the agent as PID 1.
const agentInstallPath = "/sbin/nexus3-agent"

// Config holds the configuration for a [Builder].
type Config struct {
	// BuildkitdAddr is the gRPC endpoint of the buildkitd daemon.
	// Accepted forms: "unix:///run/buildkit/buildkitd.sock",
	// "tcp://127.0.0.1:1234", or any address accepted by
	// github.com/moby/buildkit/client.New.
	//
	// How this endpoint is provisioned at runtime:
	//
	//   The nexus3 host boots a builder VM (images/builder/Containerfile —
	//   stock moby/buildkit) via internal/core/driver, then forwards
	//   buildkitd's gRPC socket (/run/buildkit/buildkitd.sock inside the
	//   guest) to a host-visible address. The forwarding transport (vsock
	//   relay, guest TCP port, or Unix socket) depends on the substrate.
	//   The full boot-and-connect lifecycle is an integration slice that
	//   wires Builder with driver.Start and driver.GuestDialer.
	BuildkitdAddr string

	// AgentBinaryPath is the host filesystem path to the nexus3-agent binary.
	// It is baked as /sbin/nexus3-agent (0755) as the final layer of every
	// built rootfs.
	AgentBinaryPath string

	// ImageKind is the [domain.ImageKind] stamped on the produced Image.
	// Must be Valid(); if zero, Build falls back to [domain.KindBase].
	ImageKind domain.ImageKind
}

// BuildRequest describes one build invocation.
type BuildRequest struct {
	// BaseRef is the OCI image reference used as the FROM base, e.g.
	// "debian:bookworm-slim". It is the starting point on top of which the
	// project's .nexus/Containerfile instructions are applied.
	BaseRef string

	// WorkspaceDir is the absolute host path to the project workspace root.
	// The builder reads .nexus/Containerfile from this directory.
	WorkspaceDir string

	// Ref is the optional human-readable tag stamped on the resulting
	// domain.Image, e.g. "nexus3-base:20260807". Informational only; not
	// used for cache lookup or equality.
	Ref string

	// ToolRecipe is the profile's declared install recipe for the agent tool.
	// When Packages is non-empty and the project's .nexus/Containerfile does
	// not opt out via the nexus3:recipe-skip directive, the recipe is rendered
	// into the synthesised Dockerfile between the user's instructions and the
	// nexus3-agent COPY. A zero ToolRecipe (no Packages) is silently ignored.
	ToolRecipe cred.ToolRecipe

	// TargetArch is the CPU architecture for which the recipe is rendered,
	// e.g. "x64" (amd64) or "arm64". Required when ToolRecipe.Packages is
	// non-empty; ignored otherwise.
	TargetArch string
}

// Builder drives stock buildkitd to produce bootable guest rootfs images.
type Builder struct {
	cfg    Config
	client BuildkitClient
	cache  *image.Cache
}

// New creates a [Builder] backed by a real buildkitd connection at
// cfg.BuildkitdAddr.
func New(cfg Config, cache *image.Cache) (*Builder, error) {
	c, err := NewBuildkitClient(cfg.BuildkitdAddr)
	if err != nil {
		return nil, fmt.Errorf("builder: connect buildkitd %s: %w", cfg.BuildkitdAddr, err)
	}
	return &Builder{cfg: cfg, client: c, cache: cache}, nil
}

// newWithClient creates a [Builder] using the supplied [BuildkitClient].
// Intended for unit tests; exposed via export_test.go.
func newWithClient(cfg Config, client BuildkitClient, cache *image.Cache) *Builder {
	return &Builder{cfg: cfg, client: client, cache: cache}
}

// Build produces a bootable guest rootfs from req and stores it in the cache.
//
// Flow:
//  1. Read .nexus/Containerfile from req.WorkspaceDir.
//  2. Verify the agent binary exists at cfg.AgentBinaryPath.
//  3. Invoke [BuildkitClient.Solve] — builds from the OCI base + Containerfile,
//     with nexus3-agent installed as /sbin/nexus3-agent in the final layer.
//  4. Export the resulting filesystem directory to a raw ext4 image, hash it,
//     and store it in the cache via [image.Cache.Put].
//
// If mke2fs is not available on the host, Build returns an error wrapping
// [ErrMke2fsUnavailable]; callers (and tests) should use [Mke2fsAvailable]
// to skip gracefully.
func (b *Builder) Build(ctx context.Context, req BuildRequest) (domain.Image, error) {
	if req.BaseRef == "" {
		return domain.Image{}, fmt.Errorf("builder: Build: BaseRef is required")
	}
	if req.WorkspaceDir == "" {
		return domain.Image{}, fmt.Errorf("builder: Build: WorkspaceDir is required")
	}

	// 1. Read .nexus/Containerfile.
	cfPath := filepath.Join(req.WorkspaceDir, containerfilePath)
	cfBytes, err := os.ReadFile(cfPath)
	if err != nil {
		return domain.Image{}, fmt.Errorf("builder: Build: read %s: %w", containerfilePath, err)
	}
	if len(cfBytes) == 0 {
		return domain.Image{}, fmt.Errorf("builder: Build: %s is empty", containerfilePath)
	}

	// 2. Verify agent binary is accessible.
	if _, err := os.Stat(b.cfg.AgentBinaryPath); err != nil {
		return domain.Image{}, fmt.Errorf("builder: Build: agent binary %s: %w",
			b.cfg.AgentBinaryPath, err)
	}

	// 3. Invoke the BuildkitClient seam.
	outDir, err := os.MkdirTemp("", "nexus3-builder-*")
	if err != nil {
		return domain.Image{}, fmt.Errorf("builder: Build: create temp dir: %w", err)
	}
	defer os.RemoveAll(outDir)

	if err := b.client.Solve(ctx, SolveRequest{
		BaseRef:            req.BaseRef,
		ContainerfileBytes: cfBytes,
		AgentPath:          b.cfg.AgentBinaryPath,
		AgentInstallPath:   agentInstallPath,
		WorkspaceDir:       req.WorkspaceDir,
		ToolRecipe:         req.ToolRecipe,
		TargetArch:         req.TargetArch,
	}, outDir); err != nil {
		return domain.Image{}, fmt.Errorf("builder: Build: solve: %w", err)
	}

	// 4. Export filesystem to raw ext4, hash, and store in cache.
	kind := b.cfg.ImageKind
	if !kind.Valid() {
		kind = domain.KindBase
	}
	img, err := exportAndCache(ctx, outDir, req.Ref, kind, b.cache)
	if err != nil {
		return domain.Image{}, fmt.Errorf("builder: Build: %w", err)
	}
	return img, nil
}
