//go:build linux

package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus3/internal/core/builder"
)

const (
	inGuestBuildkitdPath  = "/usr/local/bin/buildkitd"
	inGuestBuildkitSock   = "/run/buildkit/buildkitd.sock"
	inGuestBuildkitState  = "/var/lib/buildkit"
	inGuestRuncWrapper    = "/run/buildkit/nexus-runc"

	inGuestDefaultBase    = "ubuntu:24.04"
	inGuestDefaultImgSize = 2 << 30 // 2 GiB
)

// buildInGuestImageLinux is the Linux implementation of BuildInGuestImage.
func buildInGuestImageLinux(ctx context.Context, opts InGuestBuildOptions) error {
	bkdPath := opts.BuildkitdPath
	if bkdPath == "" {
		bkdPath = inGuestBuildkitdPath
	}
	baseRef := opts.BaseRef
	if baseRef == "" {
		baseRef = inGuestDefaultBase
	}
	imgSize := opts.ImageSizeBytes
	if imgSize == 0 {
		imgSize = inGuestDefaultImgSize
	}

	// ── Step 1: mount kernel pseudo-FSes ─────────────────────────────────────
	// The nexus3-agent runs as PID 1; no init system mounts /proc, /sys etc.
	// These mounts are idempotent — already-mounted targets log and continue.
	mountKernelFS()

	// ── Step 2: tmpfs for buildkitd state ────────────────────────────────────
	// xattr operations on virtiofs fail during buildkitd layer squash; state
	// must be on local tmpfs. The output (type=local rootfs dir) can be on
	// virtiofs.
	if err := os.MkdirAll(inGuestBuildkitState, 0o700); err != nil {
		return fmt.Errorf("in-guest build: mkdir buildkit state: %w", err)
	}
	if err := mountTmpFS(inGuestBuildkitState, "4g"); err != nil {
		log.Printf("in-guest build: WARNING: tmpfs on %s failed (%v); state will be on virtiofs",
			inGuestBuildkitState, err)
	}

	// ── Step 3: runc wrapper (--no-new-keyring) ───────────────────────────────
	runcPath, err := findRuncBinary()
	if err != nil {
		return fmt.Errorf("in-guest build: find runc: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(inGuestRuncWrapper), 0o755); err != nil {
		return fmt.Errorf("in-guest build: mkdir wrapper dir: %w", err)
	}
	if err := writeRuncWrapper(inGuestRuncWrapper, runcPath); err != nil {
		return fmt.Errorf("in-guest build: write runc wrapper: %w", err)
	}

	// ── Step 4: start buildkitd ───────────────────────────────────────────────
	sockPath := inGuestBuildkitSock
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		return fmt.Errorf("in-guest build: mkdir sock dir: %w", err)
	}
	_ = os.Remove(sockPath) // clear any stale socket

	bkCtx, bkCancel := context.WithCancel(context.Background())
	defer bkCancel()

	bkCmd := exec.CommandContext(bkCtx, bkdPath,
		"--root", filepath.Join(inGuestBuildkitState, "root"),
		"--addr", "unix://"+sockPath,
		"--oci-worker-snapshotter=native",
		"--oci-worker-binary="+inGuestRuncWrapper,
		// D-ORCH-11: run OCI worker steps in the guest's host network namespace so
		// build steps inherit DNS (192.168.127.1 via gvproxy) and outbound egress.
		// Verified against buildkitd v0.18.2 (--help: --oci-worker-net value).
		"--oci-worker-net=host",
	)
	bkCmd.Env = append(os.Environ(),
		"BUILDKITD_SNAPSHOTTER=native",
		// buildkitd does not inherit the system CA bundle; set explicitly so
		// registry pulls (e.g. ubuntu:24.04 from registry-1.docker.io) succeed.
		"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		"SSL_CERT_DIR=/etc/ssl/certs",
	)
	bkCmd.Stdout = os.Stderr // buildkitd logs → serial console
	bkCmd.Stderr = os.Stderr
	bkCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := bkCmd.Start(); err != nil {
		return fmt.Errorf("in-guest build: start buildkitd: %w", err)
	}
	// Reap buildkitd in background so it doesn't zombie when bkCancel fires.
	go func() { _ = bkCmd.Wait() }()
	log.Printf("in-guest build: buildkitd started pid=%d sock=%s", bkCmd.Process.Pid, sockPath)

	// ── Step 5: wait for buildkitd socket ────────────────────────────────────
	waitCtx, waitCancel := context.WithTimeout(ctx, 90*time.Second)
	defer waitCancel()
	if err := waitForBuildkitSocket(waitCtx, sockPath); err != nil {
		return fmt.Errorf("in-guest build: buildkitd socket not ready: %w", err)
	}
	log.Printf("in-guest build: buildkitd ready")

	// ── Step 6: build rootfs via nexus3 BuildkitClient ────────────────────────
	// Reuse the existing nexus3 buildkit seam — NewBuildkitClient + Solve.
	// The local buildkitd is addressed via its Unix socket.
	bkClient, err := builder.NewBuildkitClient("unix://" + sockPath)
	if err != nil {
		return fmt.Errorf("in-guest build: create buildkit client: %w", err)
	}

	rootfsDir, err := os.MkdirTemp("", "nexus3-inguestbuild-rootfs-")
	if err != nil {
		return fmt.Errorf("in-guest build: mkdir rootfs: %w", err)
	}
	defer os.RemoveAll(rootfsDir) //nolint:errcheck

	solveCtx, solveCancel := context.WithTimeout(ctx, 25*time.Minute)
	defer solveCancel()

	if err := bkClient.Solve(solveCtx, builder.SolveRequest{
		BaseRef:            baseRef,
		ContainerfileBytes: opts.ContainerfileBytes,
		AgentPath:          opts.AgentPath,
		AgentInstallPath:   "/sbin/nexus3-agent",
	}, rootfsDir); err != nil {
		return fmt.Errorf("in-guest build: buildkit solve: %w", err)
	}
	log.Printf("in-guest build: rootfs at %s", rootfsDir)

	// ── Step 7: rootfs directory → raw ext4 image ────────────────────────────
	if err := runMke2fsInGuest(ctx, rootfsDir, opts.OutputExt4, imgSize); err != nil {
		return fmt.Errorf("in-guest build: mke2fs: %w", err)
	}
	log.Printf("in-guest build: ext4 image at %s", opts.OutputExt4)
	return nil
}

// mountKernelFS mounts the kernel pseudo-filesystems needed by buildkitd and
// runc. The nexus3-agent runs as PID 1 so no init system does this setup.
// Errors are non-fatal: already-mounted targets return EBUSY which is logged
// and skipped.
func mountKernelFS() {
	type mountSpec struct {
		source, target, fstype string
		flags                  uintptr
		data                   string
	}
	mounts := []mountSpec{
		{"proc", "/proc", "proc", 0, ""},
		{"sysfs", "/sys", "sysfs", 0, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, ""},
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "newinstance,ptmxmode=0666"},
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", 0, ""},
	}
	for _, m := range mounts {
		if err := os.MkdirAll(m.target, 0o755); err != nil {
			log.Printf("in-guest build: mountKernelFS: mkdir %s: %v", m.target, err)
			continue
		}
		if err := unix.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			log.Printf("in-guest build: mountKernelFS: mount %s: %v (non-fatal)", m.target, err)
		} else {
			log.Printf("in-guest build: mountKernelFS: mounted %s", m.target)
		}
	}
}

// mountTmpFS mounts a tmpfs at path with the given size string (e.g. "4g").
func mountTmpFS(path, size string) error {
	return unix.Mount("tmpfs", path, "tmpfs", 0, "size="+size)
}

// findRuncBinary returns the absolute path to the runc binary. The
// moby/buildkit release ships it as "buildkit-runc" in /usr/local/bin; a
// system install uses /usr/local/bin/runc or /usr/bin/runc.
func findRuncBinary() (string, error) {
	candidates := []string{
		"/usr/local/bin/buildkit-runc",
		"/usr/bin/buildkit-runc",
		"/usr/local/bin/runc",
		"/usr/bin/runc",
		"/usr/sbin/runc",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	for _, name := range []string{"buildkit-runc", "runc"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("runc not found; tried %v and PATH", candidates)
}

// writeRuncWrapper writes an executable wrapper at wrapperPath that delegates
// to realRuncPath, injecting --no-new-keyring after the first run/create
// subcommand. This prevents session-keyring exhaustion during large builds.
// /bin/bash is used when available; POSIX /bin/sh otherwise.
func writeRuncWrapper(wrapperPath, realRuncPath string) error {
	var script string
	if _, err := os.Stat("/bin/bash"); err == nil {
		script = fmt.Sprintf(`#!/bin/bash
set -euo pipefail
REAL_RUNC=%q
args=()
injected=false
for arg in "$@"; do
  args+=("$arg")
  if [[ "$injected" == false && "$arg" != -* ]]; then
    case "$arg" in
      run|create)
        args+=("--no-new-keyring")
        injected=true
        ;;
    esac
  fi
done
exec "$REAL_RUNC" "${args[@]}"
`, realRuncPath)
	} else {
		script = fmt.Sprintf(`#!/bin/sh
set -eu
REAL_RUNC=%q
quote() { printf '%%s\n' "$1" | sed "s/'/'\\\\''/g; 1s/^/'/; \$s/\$/'/"; }
args=""
injected=false
for arg in "$@"; do
  args="$args $(quote "$arg")"
  if [ "$injected" = false ] && [ "${arg#-}" = "$arg" ]; then
    case "$arg" in
      run|create)
        args="$args $(quote --no-new-keyring)"
        injected=true
        ;;
    esac
  fi
done
eval exec "$REAL_RUNC" $args
`, realRuncPath)
	}
	return os.WriteFile(wrapperPath, []byte(script), 0o755)
}

// waitForBuildkitSocket polls sockPath at 200 ms intervals until it appears
// (is stat-able) or ctx expires.
func waitForBuildkitSocket(ctx context.Context, sockPath string) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(sockPath); err == nil {
				return nil
			}
		}
	}
}

// runMke2fsInGuest converts a rootfs directory to a raw ext4 image using
// mke2fs -d. sizeBytes is the image size; a minimum of 256 MiB is enforced.
func runMke2fsInGuest(ctx context.Context, srcDir, imgPath string, sizeBytes int64) error {
	const minSize = 256 * 1024 * 1024
	if sizeBytes < minSize {
		sizeBytes = minSize
	}

	// Pre-allocate the image file.
	f, err := os.Create(imgPath)
	if err != nil {
		return fmt.Errorf("create image file %s: %w", imgPath, err)
	}
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		return fmt.Errorf("truncate image file: %w", err)
	}
	f.Close()

	// mke2fs -d populates the ext4 image directly from srcDir.
	cmd := exec.CommandContext(ctx, "mke2fs",
		"-t", "ext4",
		"-d", srcDir,
		"-L", "nexus3-root",
		"-U", "00000000-0000-0000-0000-000000000000", // deterministic UUID
		"-E", "hash_seed=00000000-0000-0000-0000-000000000000",
		imgPath,
	)
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mke2fs: %w\noutput:\n%s", err, out)
	}
	return nil
}
