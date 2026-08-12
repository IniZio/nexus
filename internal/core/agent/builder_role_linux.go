//go:build linux

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// contextMountPoint is the tmpfs-free, stable mount point for the build
// context inside the builder VM.
const contextMountPoint = "/build-context"

// RunBuilderRole executes the in-guest builder lifecycle. It is called from
// cmd/nexus3-agent when the --builder-role flag is present. The VM must have
// been booted with the following virtio-blk layout:
//
//	vda — builder rootfs (this VM's own disk)
//	vdb — context ext4 packed by G4 ContextToDisk (read-only)
//	vdc — pre-allocated artifact disk; this function writes the rootfs ext4
//	vdd+ — optional ecosystem cache disks (mounted by the caller before
//	        invoking RunBuilderRole, or by this function via opts)
//
// Steps:
//  1. Mount the context disk (opts.ContextDev) read-only at /build-context.
//  2. Read the Containerfile from the mounted context.
//  3. Call [BuildInGuestImage] which spawns buildkitd on a local Unix socket,
//     drives the build, and writes a raw ext4 rootfs to opts.ArtifactDev.
//  4. Call syscall.Sync() to flush all writes (including the artifact disk) to
//     the virtio-blk backend before the host tears down the VMM.
//  5. Unmount the context disk.
//
// On success RunBuilderRole returns nil. The caller (cmd/nexus3-agent) should
// os.Exit(0) immediately after, letting the agent terminate cleanly.
func RunBuilderRole(ctx context.Context, opts BuilderRoleOptions) error {
	contextDev := opts.ContextDev
	if contextDev == "" {
		contextDev = "/dev/vdb"
	}
	artifactDev := opts.ArtifactDev
	if artifactDev == "" {
		artifactDev = "/dev/vdc"
	}
	containerfileRel := opts.ContainerfileRel
	if containerfileRel == "" {
		containerfileRel = ".nexus/Containerfile"
	}
	agentPath := opts.AgentPath
	if agentPath == "" {
		agentPath = "/sbin/nexus3-agent"
	}

	// ── 1. Mount the context disk ─────────────────────────────────────────────
	if err := os.MkdirAll(contextMountPoint, 0o755); err != nil {
		return fmt.Errorf("builder role: mkdir %s: %w", contextMountPoint, err)
	}
	if err := syscall.Mount(contextDev, contextMountPoint, "ext4", syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("builder role: mount context %s → %s: %w", contextDev, contextMountPoint, err)
	}
	defer func() {
		if err := syscall.Unmount(contextMountPoint, 0); err != nil {
			// Non-fatal: the VM is about to exit anyway.
			fmt.Fprintf(os.Stderr, "builder role: unmount context: %v\n", err)
		}
	}()

	// ── 2. Read the Containerfile ─────────────────────────────────────────────
	cfPath := filepath.Join(contextMountPoint, filepath.Clean(containerfileRel))
	cfBytes, err := os.ReadFile(cfPath)
	if err != nil {
		return fmt.Errorf("builder role: read %s: %w", cfPath, err)
	}

	// ── 2.5. Mount ecosystem cache disks ─────────────────────────────────────
	// Mount each cache disk at its canonical guest path before buildkitd starts.
	// The buildkit cache disk (conventionally first) must be at /var/lib/buildkit
	// before BuildInGuestImage so that buildkitd detects a persistent mount and
	// skips its internal tmpfs (buildkit_linux.go:buildkitStateIsPersistent).
	// Errors are non-fatal: the build proceeds with ephemeral (tmpfs) state.
	for _, cd := range opts.CacheDisks {
		if err := mountCacheDisk(cd.Device, cd.MountPath); err != nil {
			fmt.Fprintf(os.Stderr, "builder role: mount cache disk %s → %s: %v (non-fatal, continuing)\n",
				cd.Device, cd.MountPath, err)
		} else {
			fmt.Fprintf(os.Stderr, "builder role: mounted cache disk %s → %s\n", cd.Device, cd.MountPath)
		}
	}

	// ── 3. Build ──────────────────────────────────────────────────────────────
	// BuildInGuestImage (buildkit_linux.go) starts buildkitd on
	// /run/buildkit/buildkitd.sock, drives the solve, and writes the rootfs
	// as a raw ext4 image to OutputExt4. Writing directly to the block device
	// path is intentional: the host reads back the raw device image file.
	buildOpts := InGuestBuildOptions{
		ContainerfileBytes: cfBytes,
		BaseRef:            opts.BaseRef,
		AgentPath:          agentPath,
		OutputExt4:         artifactDev,
		BuildkitdPath:      opts.BuildkitdPath,
		ContextDir:         contextMountPoint, // /build-context — vdb mounted read-only above
	}
	if err := BuildInGuestImage(ctx, buildOpts); err != nil {
		return fmt.Errorf("builder role: build: %w", err)
	}

	// ── 4. Sync all writes to virtio-blk backends ─────────────────────────────
	// This must happen BEFORE the host calls drv.Stop, so that the artifact
	// disk image on the host reflects the completed build output. The host
	// orchestrator (vmbuilder.go) also issues a "sync" exec via the agent,
	// but this in-guest call is the definitive flush.
	syscall.Sync()

	return nil
}

// mountCacheDisk mounts the ext4 block device at device onto mountPath,
// creating the directory if necessary. It is used by RunBuilderRole to attach
// ecosystem cache disks (vdd+) before the build step runs.
func mountCacheDisk(device, mountPath string) error {
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", mountPath, err)
	}
	if err := syscall.Mount(device, mountPath, "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount %s → %s: %w", device, mountPath, err)
	}
	return nil
}
