//go:build linux

package agent

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec" // used for exec.CommandContext (buildkitd) and exec.LookPath (runc fallback)
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus3/internal/core/builder"
)

const (
	inGuestBuildkitdPath = "/usr/local/bin/buildkitd"
	inGuestBuildkitSock  = "/run/buildkit/buildkitd.sock"
	inGuestBuildkitState = "/var/lib/buildkit"

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

	// ── Step 2: buildkitd state directory ────────────────────────────────────
	// G6: when a cache disk is attached (G5 EnsureCacheDisk("buildkit")), G3
	// mounts it at /var/lib/buildkit as a persistent ext4 virtio-blk device
	// before this function runs. In that case we reuse the persistent mount as
	// buildkitd's --root so the layer cache survives across builds.
	//
	// Without a cache disk, /var/lib/buildkit is not a mountpoint; we mount a
	// 4 GiB tmpfs so that xattr operations during layer squash succeed (they
	// fail on virtiofs). This is the original behavior (TestBuildDogfood path).
	if err := os.MkdirAll(inGuestBuildkitState, 0o700); err != nil {
		return fmt.Errorf("in-guest build: mkdir buildkit state: %w", err)
	}
	if buildkitStateIsPersistent(inGuestBuildkitState) {
		log.Printf("in-guest build: /var/lib/buildkit is a persistent ext4 mount — skipping tmpfs, layer cache will persist")
	} else {
		if err := mountTmpFS(inGuestBuildkitState, "4g"); err != nil {
			log.Printf("in-guest build: WARNING: tmpfs on %s failed (%v); state will be on virtiofs",
				inGuestBuildkitState, err)
		}
	}

	// ── Step 3: locate the runc binary (absolute path, no PATH lookup needed) ──
	// buildkitd's internal runc check uses exec.LookPath(binary). The kernel
	// init PATH (/sbin:/usr/sbin:/bin:/usr/bin) often omits /usr/local/bin where
	// moby/buildkit ships buildkit-runc. Passing --oci-worker-binary with an
	// absolute path bypasses PATH lookup entirely and matches what G2's shell
	// script achieves by inheriting a richer shell environment.
	runcPath, err := findRuncBinary()
	if err != nil {
		return fmt.Errorf("in-guest build: find runc: %w", err)
	}
	log.Printf("in-guest build: runc binary at %s", runcPath)

	if err := os.MkdirAll(filepath.Dir(inGuestBuildkitSock), 0o755); err != nil {
		return fmt.Errorf("in-guest build: mkdir buildkit dir: %w", err)
	}

	// ── Step 4: start buildkitd ───────────────────────────────────────────────
	sockPath := inGuestBuildkitSock
	_ = os.Remove(sockPath) // clear any stale socket

	bkCtx, bkCancel := context.WithCancel(context.Background())
	defer bkCancel()

	// CRITICAL: redirect buildkitd's output to a log file, NOT to os.Stderr
	// (the vsock exec pipe).  If buildkitd inherits the exec pipe's write-end,
	// the ring reader (feedRingFromReader) cannot get EOF while buildkitd is
	// alive, which prevents the Exit frame from ever reaching the host.
	// G2's shell script uses the same pattern: > /tmp/bkd.log 2>&1 &
	bkLogPath := "/tmp/nexus3-bkd.log"
	bkLogFile, err := os.Create(bkLogPath)
	if err != nil {
		return fmt.Errorf("in-guest build: create bkd log: %w", err)
	}

	bkCmd := exec.CommandContext(bkCtx, bkdPath,
		"--root", inGuestBuildkitState, // same as G2: --root=/var/lib/buildkit
		"--addr", "unix://"+sockPath,
		"--oci-worker-snapshotter=native",
		// Pass the absolute runc path so buildkitd doesn't need PATH lookup.
		// The kernel init PATH omits /usr/local/bin (where buildkit-runc lives),
		// so exec.LookPath("buildkit-runc") would fail without this.
		"--oci-worker-binary="+runcPath,
		// D-ORCH-11: run OCI worker steps in the guest's host network namespace so
		// build steps inherit DNS (192.168.127.1 via gvproxy) and outbound egress.
		"--oci-worker-net=host",
	)
	bkCmd.Env = append(os.Environ(),
		"BUILDKITD_SNAPSHOTTER=native",
		// Ensure /usr/local/bin is in PATH so buildkitd can find its bundled
		// runc even if the kernel init PATH omits it. We also pass
		// --oci-worker-binary with an absolute path (above) as the primary fix.
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		// buildkitd does not inherit the system CA bundle; set explicitly so
		// registry pulls (e.g. ubuntu:24.04 from registry-1.docker.io) succeed.
		"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		"SSL_CERT_DIR=/etc/ssl/certs",
	)
	bkCmd.Stdout = bkLogFile
	bkCmd.Stderr = bkLogFile
	bkCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := bkCmd.Start(); err != nil {
		bkLogFile.Close()
		return fmt.Errorf("in-guest build: start buildkitd: %w", err)
	}
	// Parent closes its copy of the log file FD — buildkitd holds its own.
	// This ensures the exec-pipe is not held open by buildkitd and the host
	// gets EOF (and the Exit frame) when builder-role exits normally.
	bkLogFile.Close()

	// Forward buildkitd log to os.Stderr (→ ring → host execBuf) in background.
	// Tail-loop exits when buildkitd closes the file (i.e. when bkCmd exits).
	go func() {
		f, err := os.Open(bkLogPath)
		if err != nil {
			log.Printf("in-guest build: cannot open bkd log: %v", err)
			return
		}
		defer f.Close()
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				os.Stderr.Write(buf[:n]) //nolint:errcheck
			}
			if err != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// Reap buildkitd in background so it doesn't zombie when bkCancel fires.
	go func() { _ = bkCmd.Wait() }()
	log.Printf("in-guest build: buildkitd started pid=%d sock=%s log=%s", bkCmd.Process.Pid, sockPath, bkLogPath)

	// ── Step 5: wait for buildkitd socket ────────────────────────────────────
	waitCtx, waitCancel := context.WithTimeout(ctx, 90*time.Second)
	defer waitCancel()
	if err := waitForBuildkitSocket(waitCtx, sockPath); err != nil {
		// Dump buildkitd's startup log so the host sees the real worker-init error.
		if bkdLog, readErr := os.ReadFile(bkLogPath); readErr == nil {
			log.Printf("in-guest build: buildkitd startup log:\n%s", bkdLog)
		}
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

	// Back the rootfs export scratch on the buildkit cache DISK rather than a
	// RAM tmpfs. The cache disk (/var/lib/buildkit) is a large auto-growing ext4
	// volume mounted before BuildInGuestImage is called (G5 EnsureCacheDisk +
	// builder_role_linux.go mountCacheDisk). Using disk capacity decouples export
	// headroom from builder VM RAM — a right-sized builder (e.g. 4 GiB) can
	// export a 2+ GiB rootfs without exhausting guest memory pages.
	//
	// The previous approach (4 GiB RAM-backed tmpfs) caused hollow exports
	// (files present but zero-length) on small builders because the tmpfs +
	// buildkitd working set together exceeded available guest RAM.
	//
	// Fallback: if the cache disk dir can't be created (e.g. disk not mounted),
	// we fall back to the rootfs /tmp. We do NOT fall back to the RAM tmpfs —
	// that is the thing we are removing.
	const buildkitCacheMountPath = "/var/lib/buildkit"
	const exportScratchDir = buildkitCacheMountPath + "/nexus3-export"
	exportBase := "" // empty → os.TempDir() = rootfs /tmp (fallback)
	if mkErr := os.MkdirAll(exportScratchDir, 0o700); mkErr == nil {
		exportBase = exportScratchDir
		defer os.RemoveAll(exportScratchDir) //nolint:errcheck
	} else {
		log.Printf("in-guest build: WARNING: cannot create export scratch on cache disk (%v); falling back to rootfs /tmp", mkErr)
	}

	rootfsDir, err := os.MkdirTemp(exportBase, "nexus3-inguestbuild-rootfs-")
	if err != nil {
		return fmt.Errorf("in-guest build: mkdir rootfs: %w", err)
	}
	defer os.RemoveAll(rootfsDir) //nolint:errcheck

	// Prototype finding (2026-08): apt-heavy images (e.g. ubuntu:24.04 with
	// docker.io + docker-compose-v2) routinely exceed the old 25-minute limit.
	// Default raised to 45 minutes; override via NEXUS3_BUILD_SOLVE_TIMEOUT.
	solveTimeout := solveBuildTimeout()
	log.Printf("in-guest build: solve timeout %v (set NEXUS3_BUILD_SOLVE_TIMEOUT to override)", solveTimeout)
	solveCtx, solveCancel := context.WithTimeout(ctx, solveTimeout)
	defer solveCancel()

	if err := bkClient.Solve(solveCtx, builder.SolveRequest{
		BaseRef:            baseRef,
		ContainerfileBytes: opts.ContainerfileBytes,
		AgentPath:          opts.AgentPath,
		AgentInstallPath:   "/sbin/nexus3-agent",
		WorkspaceDir:       opts.ContextDir, // vdb mount point; empty means no user context files
	}, rootfsDir); err != nil {
		// Prototype finding (2026-08): the async log-forward goroutine is cut off
		// at shutdown, so the buildkitd failure reason never reaches the host.
		// Synchronously flush the buildkitd log and write the solve error to
		// stderr here so the host captures the real cause before exit.
		fmt.Fprintf(os.Stderr, "in-guest build: solve failed: %v\n", err)
		if bkdLog, readErr := os.ReadFile(bkLogPath); readErr == nil {
			fmt.Fprintf(os.Stderr, "in-guest build: buildkitd log:\n%s\n", bkdLog)
		}
		return fmt.Errorf("in-guest build: buildkit solve: %w", err)
	}
	log.Printf("in-guest build: rootfs at %s", rootfsDir)

	// ── Integrity gate: reject a hollow export before it becomes an image ─────
	// A buildkit export that yields correct directory structure but empty
	// regular-file contents (observed intermittently: ~99.96% of files
	// zero-length) must not be silently converted to an ext4 image, cached, and
	// booted — that only surfaces later as "exec format error" at runtime. Fail
	// the build here so it is retried instead.
	if err := verifyRootfsPopulated(rootfsDir); err != nil {
		fmt.Fprintf(os.Stderr, "in-guest build: %v\n", err)
		return fmt.Errorf("in-guest build: %w", err)
	}

	// ── Integrity gate: reject a truncated agent export before it becomes an image ──
	// An intermittent export bug silently caps files > 32 MiB to exactly 32 MiB.
	// The agent binary is the ideal canary: its exact source size is known and
	// any mismatch proves corruption. Fail the build here so it is retried.
	if err := verifyAgentIntegrity(rootfsDir, "/sbin/nexus3-agent", opts.AgentPath); err != nil {
		fmt.Fprintf(os.Stderr, "in-guest build: %v\n", err)
		return fmt.Errorf("in-guest build: %w", err)
	}

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

// buildkitStateIsPersistent reports whether path is a DISTINCT mountpoint —
// i.e. a separately-mounted device (e.g. a virtio-blk ext4 cache disk from G5)
// rather than a plain directory on the guest rootfs.
//
// Detection: compare the st_dev (device ID) of path with the st_dev of its
// parent directory. A real mountpoint has a different st_dev than its parent;
// a plain directory shares its parent's st_dev. This works regardless of the
// underlying filesystem type (ext4, tmpfs, …) and requires no CAP_SYS_ADMIN.
//
// The guest rootfs itself is virtio-blk ext4, so checking for EXT4_SUPER_MAGIC
// alone would be wrong — every directory on the rootfs would match. Only a
// separately-attached cache disk (G5 EnsureCacheDisk("buildkit") → G3 mount)
// produces a distinct st_dev.
func buildkitStateIsPersistent(path string) bool {
	var stPath, stParent unix.Stat_t
	if err := unix.Stat(path, &stPath); err != nil {
		return false
	}
	if err := unix.Stat(filepath.Dir(path), &stParent); err != nil {
		return false
	}
	return stPath.Dev != stParent.Dev
}

// findRuncBinary returns the absolute path to the runc binary in the current
// root filesystem. The moby/buildkit image ships it as buildkit-runc; plain
// system installs use runc. We use an absolute path to avoid relying on PATH
// (the kernel init PATH often omits /usr/local/bin).
func findRuncBinary() (string, error) {
	candidates := []string{
		"/usr/local/bin/buildkit-runc",
		"/usr/bin/buildkit-runc",
		"/usr/local/bin/runc",
		"/usr/bin/runc",
		"/usr/sbin/runc",
		"/sbin/runc",
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

// findMke2fs returns the absolute path to mke2fs by probing well-known
// locations. exec.LookPath relies on os.Getenv("PATH"), which is empty when
// the agent runs as PID 1 (init=/sbin/nexus3-agent); absolute-path probing
// avoids that dependency.
func findMke2fs() (string, error) {
	candidates := []string{
		"/sbin/mke2fs",
		"/usr/sbin/mke2fs",
		"/usr/bin/mke2fs",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Fallback: try PATH lookup (works on non-PID-1 environments).
	if p, err := exec.LookPath("mke2fs"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("mke2fs not found; tried %v and PATH", candidates)
}

// solveBuildTimeout returns the buildkit solve timeout. It reads
// NEXUS3_BUILD_SOLVE_TIMEOUT (a Go duration string, e.g. "60m"); when unset
// or unparseable it defaults to 45 minutes.
func solveBuildTimeout() time.Duration {
	if s := os.Getenv("NEXUS3_BUILD_SOLVE_TIMEOUT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	return 45 * time.Minute
}

// waitForBuildkitSocket polls sockPath at 200 ms intervals until a TCP-style
// probe connection succeeds or ctx expires. Stat-only polling is insufficient:
// buildkitd creates the socket file at bind() but only accepts connections
// after listen() completes — probing with an actual Dial catches the listen()
// boundary and avoids an ECONNREFUSED race in the subsequent gRPC Solve.
func waitForBuildkitSocket(ctx context.Context, sockPath string) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
			if err == nil {
				conn.Close()
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

	// For a regular file, pre-allocate so mke2fs knows the image size.
	// For a block device (e.g. /dev/vdc inside the builder VM), truncate is
	// not supported — mke2fs probes the device size directly via ioctl.
	fi, statErr := os.Stat(imgPath)
	isBlockDev := statErr == nil && fi.Mode()&os.ModeDevice != 0
	if !isBlockDev {
		f, err := os.Create(imgPath)
		if err != nil {
			return fmt.Errorf("create image file %s: %w", imgPath, err)
		}
		if err := f.Truncate(sizeBytes); err != nil {
			f.Close()
			return fmt.Errorf("truncate image file: %w", err)
		}
		f.Close()
	}

	// Locate mke2fs. exec.LookPath uses the PROCESS's os.Getenv("PATH"), not
	// cmd.Env. When nexus3-agent runs as PID 1 (init=/sbin/nexus3-agent) the
	// kernel provides no PATH, so LookPath("mke2fs") fails. Probe candidates
	// with absolute paths instead.
	mke2fsPath, err := findMke2fs()
	if err != nil {
		return fmt.Errorf("mke2fs: %w", err)
	}

	// mke2fs -d populates the ext4 image directly from srcDir.
	cmd := exec.CommandContext(ctx, mke2fsPath,
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
