// Package agent — in-guest buildkitd helper.
//
// BuildInGuestImage starts buildkitd inside a nexus3 microVM, waits for it to
// be ready, drives the existing nexus3 BuildkitClient.Solve seam against the
// local socket to produce a rootfs directory, then converts the directory to a
// raw ext4 disk image consumable by cloud-hypervisor.
//
// This function is intended to be called from inside the microVM (e.g. by
// cmd/nexus3-agent when it needs to build an inner guest image). The microVM
// itself is the isolation boundary, so buildkitd runs rootful without
// rootlesskit.
//
// Prerequisites (staged in .nexus/Containerfile):
//   - /usr/local/bin/buildkitd (moby/buildkit v0.18.2)
//   - /usr/local/bin/buildkit-runc  (bundled runc; see findRuncBinary)
//   - mke2fs in PATH (e2fsprogs)
package agent

import (
	"context"
	"fmt"
)

// InGuestBuildOptions configures [BuildInGuestImage].
type InGuestBuildOptions struct {
	// ContainerfileBytes is the Dockerfile/Containerfile content to build.
	// Applied on top of BaseRef as the build definition.
	ContainerfileBytes []byte

	// BaseRef is the OCI image reference used as the FROM base
	// (e.g. "ubuntu:24.04"). Defaults to "ubuntu:24.04" when empty.
	BaseRef string

	// AgentPath is the absolute in-guest path to the nexus3-agent binary.
	// It is baked into the inner image at /sbin/nexus3-agent (the boot
	// contract: init=/sbin/nexus3-agent).
	// Typically the outer guest's own /sbin/nexus3-agent.
	AgentPath string

	// OutputExt4 is the destination path for the produced raw ext4 image.
	// The directory must already exist.
	OutputExt4 string

	// BuildkitdPath overrides the path to the buildkitd binary.
	// Defaults to /usr/local/bin/buildkitd.
	BuildkitdPath string

	// ImageSizeBytes is the size of the raw ext4 image to create.
	// Defaults to 2 GiB when zero.
	ImageSizeBytes int64
}

// BuildInGuestImage performs a full in-guest image build and ext4 conversion.
// It is implemented only on Linux (see buildkit_linux.go). On other platforms
// it always returns an error.
//
// Steps:
//  1. Mount kernel pseudo-filesystems (/proc, /sys, /dev/pts, /sys/fs/cgroup)
//     so that buildkitd and runc can operate — idempotent, non-fatal if already
//     mounted.
//  2. Mount a per-build tmpfs at /var/lib/buildkit so that buildkitd's internal
//     OCI snapshot state is NOT on virtiofs (xattr operations on virtiofs fail
//     during build layer squash).
//  3. Write a runc wrapper at /run/buildkit/nexus-runc that injects
//     --no-new-keyring into runc run/create subcommands, preventing session
//     keyring exhaustion on long builds.
//  4. Start buildkitd rootful with --oci-worker-snapshotter=native (overlay
//     cannot be nested on virtiofs).
//  5. Poll the buildkitd Unix socket for readiness (up to 90 s).
//  6. Drive builder.NewBuildkitClient("unix:///run/buildkit/buildkitd.sock")
//     + client.Solve(SolveRequest{...}, rootfsDir) to produce a rootfs tree.
//  7. Convert the rootfs directory to a raw ext4 image via mke2fs -d.
func BuildInGuestImage(ctx context.Context, opts InGuestBuildOptions) error {
	if len(opts.ContainerfileBytes) == 0 {
		return fmt.Errorf("BuildInGuestImage: ContainerfileBytes is required")
	}
	if opts.AgentPath == "" {
		return fmt.Errorf("BuildInGuestImage: AgentPath is required")
	}
	if opts.OutputExt4 == "" {
		return fmt.Errorf("BuildInGuestImage: OutputExt4 is required")
	}
	return buildInGuestImageLinux(ctx, opts)
}
