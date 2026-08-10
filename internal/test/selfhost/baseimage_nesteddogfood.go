// Package selfhost — nested-dogfood image builder.
//
// BuildNestedDogfoodImage produces the outer VM image used by
// TestNestedDogfood. It is a test fixture: a ubuntu:24.04 base with
// buildkitd+buildctl+runc staged (the in-guest build toolchain),
// mke2fs (for ext4 conversion), cloud-hypervisor (for inner VM boot),
// and a kernel at /boot/vmlinux (staged from the host repo).
//
// nexus3-agent is baked in as the final layer by the docker build (via
// the nestedDogfoodContainerfile COPY instruction) and serves as PID 1.
package selfhost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

const (
	nestedDogfoodDockerTag   = "nexus3-nested-dogfood-test:dev"
	nestedDogfoodImageSizeGB = 3 << 30 // 3 GiB: ubuntu + Go tools + buildkitd
)

// nestedDogfoodContainerfile is the Dockerfile for the outer dogfood VM image.
//
// Staged binaries / tooling:
//   - buildkitd, buildctl, buildkit-runc from moby/buildkit v0.18.2 — the
//     in-guest build toolchain, matching the host go.mod moby/buildkit version.
//   - mke2fs (e2fsprogs) — converts the built rootfs directory to a raw ext4.
//   - cloud-hypervisor — launches the inner nexus3 microVM.
//   - vmlinux (staged from host build context) — the inner VM kernel.
//   - nexus3-agent — PID 1 of the outer guest; ALSO baked into inner images
//     as /sbin/nexus3-agent by the in-guest build.
//
// Boot contract: root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0
//
// This Containerfile is intentionally separate from the repo-root
// .nexus/Containerfile; it is a test fixture that names all versions
// explicitly and adds test-only tooling (mke2fs).
const nestedDogfoodContainerfile = `# nexus3 nested-dogfood test fixture — outer VM image.
# This image is built by BuildNestedDogfoodImage (internal/test/selfhost).
FROM ubuntu:24.04

# ── Base tools ────────────────────────────────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        e2fsprogs \
        iproute2 \
    && rm -rf /var/lib/apt/lists/*

# ── buildkitd + buildctl + buildkit-runc (moby/buildkit v0.18.2) ─────────────
# Matches the host go.mod moby/buildkit dependency so host and guest speak
# the same buildkit protocol. buildkitd runs rootful inside the microVM.
ARG BUILDKIT_VERSION=v0.18.2
RUN TARBALL="buildkit-${BUILDKIT_VERSION}.linux-amd64.tar.gz" \
    && curl -fsSL --retry 5 --retry-delay 2 \
        "https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/${TARBALL}" \
        -o "/tmp/${TARBALL}" \
    && tar -C /tmp -xzf "/tmp/${TARBALL}" \
    && install -m 755 /tmp/bin/buildkitd     /usr/local/bin/buildkitd \
    && install -m 755 /tmp/bin/buildctl      /usr/local/bin/buildctl \
    && install -m 755 /tmp/bin/buildkit-runc /usr/local/bin/buildkit-runc \
    && rm -rf /tmp/bin "/tmp/${TARBALL}"

# ── cloud-hypervisor static binary ───────────────────────────────────────────
RUN curl -fsSL --retry 5 --retry-delay 2 \
        "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/latest/download/cloud-hypervisor-static" \
        -o /usr/local/bin/cloud-hypervisor \
    && chmod +x /usr/local/bin/cloud-hypervisor

# ── Inner-VM kernel ───────────────────────────────────────────────────────────
# Staged from the build context (images/kernel/vmlinux-x86_64 on the host,
# renamed vmlinux in the context by BuildNestedDogfoodImage).
COPY vmlinux /boot/vmlinux

# ── Guest agent (outer PID 1 + inner image ingredient) ───────────────────────
# Boot contract: root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0
COPY nexus3-agent /sbin/nexus3-agent
RUN chmod 755 /sbin/nexus3-agent

ENV IS_SANDBOX=1
`

// BuildNestedDogfoodImage produces the outer dogfood VM ext4 image and stores
// it in cache keyed by SHA-256 digest.
//
// Prerequisite checks:
//   - docker in PATH (returns [ErrDockerUnavailable] if absent)
//   - mke2fs in PATH (returns [builder.ErrMke2fsUnavailable] if absent)
//   - images/kernel/vmlinux-x86_64 present under the repo root
func BuildNestedDogfoodImage(ctx context.Context, cache *image.Cache) (domain.Image, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return domain.Image{}, ErrDockerUnavailable
	}
	if !builder.Mke2fsAvailable() {
		return domain.Image{}, builder.ErrMke2fsUnavailable
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: find repo root: %w", err)
	}

	// Check for kernel asset.
	kernelSrc := filepath.Join(repoRoot, "images", "kernel", "vmlinux-x86_64")
	if _, err := os.Stat(kernelSrc); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: kernel not found at %s: %w", kernelSrc, err)
	}

	// Compile nexus3-agent.
	workDir, err := os.MkdirTemp("", "nexus3-nesteddogfood-build-")
	if err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: mktemp: %w", err)
	}
	defer os.RemoveAll(workDir) //nolint:errcheck

	agentBin := filepath.Join(workDir, "nexus3-agent")
	buildAgentCmd := exec.CommandContext(ctx,
		"go", "build",
		"-o", agentBin,
		"./cmd/nexus3-agent",
	)
	buildAgentCmd.Dir = repoRoot
	buildAgentCmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	buildAgentCmd.Stdout = os.Stderr
	buildAgentCmd.Stderr = os.Stderr
	if err := buildAgentCmd.Run(); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: compile nexus3-agent: %w", err)
	}

	// Prepare docker build context.
	ctxDir := filepath.Join(workDir, "ctx")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: mkdir ctx: %w", err)
	}

	cfPath := filepath.Join(ctxDir, "Containerfile")
	if err := os.WriteFile(cfPath, []byte(nestedDogfoodContainerfile), 0o644); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: write Containerfile: %w", err)
	}

	// Copy vmlinux-x86_64 into context as "vmlinux".
	if err := copyFile(kernelSrc, filepath.Join(ctxDir, "vmlinux"), 0o644); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: copy kernel: %w", err)
	}

	// Copy nexus3-agent into context.
	if err := copyFile(agentBin, filepath.Join(ctxDir, "nexus3-agent"), 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: copy agent: %w", err)
	}

	// ── docker build ─────────────────────────────────────────────────────────
	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"-f", cfPath,
		"-t", nestedDogfoodDockerTag,
		ctxDir,
	)
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: docker build: %w", err)
	}
	defer func() { _ = exec.Command("docker", "rmi", "--force", nestedDogfoodDockerTag).Run() }()

	// ── docker create → docker export → extract tar ───────────────────────────
	rootfsDir := filepath.Join(workDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: mkdir rootfs: %w", err)
	}
	if err := exportRootfs(ctx, nestedDogfoodDockerTag, rootfsDir); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: export rootfs: %w", err)
	}

	// ── mke2fs ────────────────────────────────────────────────────────────────
	ext4Path := filepath.Join(workDir, "nested-dogfood.ext4")
	if err := runMke2fs(ctx, rootfsDir, ext4Path, nestedDogfoodImageSizeGB); err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: mke2fs: %w", err)
	}

	// ── hash and store ────────────────────────────────────────────────────────
	img, err := hashAndStore(ctx, cache, ext4Path)
	if err != nil {
		return domain.Image{}, fmt.Errorf("nested-dogfood-image: cache store: %w", err)
	}
	return img, nil
}
