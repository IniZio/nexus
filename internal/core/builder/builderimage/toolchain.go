//go:build linux

package builderimage

// toolchain.go ensures the moby/buildkit-based builder rootfs has the
// prerequisites required to run buildkitd and execute OCI builds inside a
// nexus microVM.
//
// # Architecture: buildkit-executor-provides-userland
//
// The critical architectural distinction (and the Prototype-A spike's failure
// point) is which component provides the userland for Dockerfile RUN steps:
//
//   - Wrong assumption (spike): the builder VM rootfs must have apt, npm, etc.
//     installed so that RUN apt-get / RUN npm can execute on the builder's
//     own filesystem.
//
//   - Correct architecture: buildkitd's OCI executor (runc) runs each RUN step
//     inside a container built from the Dockerfile's FROM image. When the
//     Dockerfile says "FROM debian:stable-slim", buildkitd pulls that image,
//     sets up an OCI bundle with debian's rootfs, and executes RUN commands
//     inside that container. The builder VM's own filesystem is completely
//     separate — it never participates in executing RUN steps.
//
// Therefore the builder VM rootfs (moby/buildkit) only needs:
//   - A working glibc environment (moby/buildkit is a Debian-derived image)
//   - buildkitd binary and its bundled runc (buildkit-runc)
//   - Kernel support: cgroup v2, overlayfs or native snapshotter, proc, sysfs
//   - Working network to pull FROM images (provided by the nexus perimeter)
//
// This file adds missing runtime prerequisites via [addToolchainLayers], called
// as part of [EnsureBuilderImage], and exports [DiscoverBuildkitdPath] so
// callers (tests, G3 lifecycle) can locate the actual buildkitd binary without
// hard-coding a path that may differ between image versions.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// buildkitdCandidates is the ordered list of paths to search for buildkitd
// inside the builder rootfs. moby/buildkit v0.19+ installs to /usr/bin.
var buildkitdCandidates = []string{
	"/usr/bin/buildkitd",
	"/usr/local/bin/buildkitd",
}

// runcCandidates is the ordered list of paths to search for the bundled runc.
var runcCandidates = []string{
	"/usr/bin/buildkit-runc",
	"/usr/local/bin/buildkit-runc",
	"/usr/bin/runc",
	"/usr/local/bin/runc",
}

// buildctlCandidates is the ordered list of paths to search for the buildctl
// client binary.
var buildctlCandidates = []string{
	"/usr/bin/buildctl",
	"/usr/local/bin/buildctl",
}

// addToolchainLayers augments the extracted moby/buildkit rootfs staging
// directory with the runtime prerequisites required for buildkitd to operate
// inside a nexus microVM.
//
// It creates directories that may be absent in the stock OCI image but are
// required at VM runtime, and creates compatibility symlinks so that callers
// using hard-coded /usr/local/bin paths (e.g. InGuestBuildOptions defaults)
// can reach the actual binaries regardless of where moby/buildkit installed
// them.
//
// Called by [EnsureBuilderImage] immediately after [addBootLayers].
func addToolchainLayers(ctx context.Context, stagingDir string) error {
	// ── 1. Runtime directories ────────────────────────────────────────────────
	// Create directories that must exist at VM boot but may not be present in
	// the stock moby/buildkit OCI image. Failures are non-fatal because many
	// of these are standard paths already populated by moby/buildkit's base OS.
	runtimeDirs := []struct {
		rel  string
		mode os.FileMode
	}{
		{"tmp", 0o1777},             // scratch space, sticky bit
		{"build-context", 0o755},    // builder_role_linux.go context mount point
		{"sys/fs/cgroup", 0o755},    // cgroup2 mount point (runc, buildkitd)
		{"run/buildkit", 0o755},     // buildkitd socket directory
		{"var/lib/buildkit", 0o711}, // buildkitd metadata / snapshotter root
	}
	for _, d := range runtimeDirs {
		p := filepath.Join(stagingDir, d.rel)
		if err := os.MkdirAll(p, d.mode); err != nil {
			return fmt.Errorf("toolchain: mkdir %s: %w", d.rel, err)
		}
		// Ensure the permission is set even if MkdirAll found the dir existing
		// (MkdirAll does not chmod existing dirs).
		if err := os.Chmod(p, d.mode); err != nil {
			return fmt.Errorf("toolchain: chmod %s: %w", d.rel, err)
		}
	}

	// ── 2. Compatibility symlinks ─────────────────────────────────────────────
	// agent/buildkit.go (InGuestBuildOptions) defaults to /usr/local/bin/buildkitd.
	// moby/buildkit v0.19+ installs to /usr/bin/. Bridge the gap with a symlink
	// so callers using the default path work without changes.
	symlinks := []struct {
		target string // existing binary (must be one of the candidates above)
		link   string // additional alias to create if the alias does not exist
	}{
		{"/usr/bin/buildkitd", "/usr/local/bin/buildkitd"},
		{"/usr/bin/buildkit-runc", "/usr/local/bin/buildkit-runc"},
		{"/usr/bin/buildctl", "/usr/local/bin/buildctl"},
	}
	for _, sl := range symlinks {
		targetInStaging := filepath.Join(stagingDir, sl.target[1:])
		linkInStaging := filepath.Join(stagingDir, sl.link[1:])

		// Only create the symlink if the target binary exists AND the link
		// destination does not yet exist.
		if _, err := os.Lstat(targetInStaging); err != nil {
			continue // target not present; skip
		}
		if _, err := os.Lstat(linkInStaging); err == nil {
			continue // link already exists (or is the binary itself)
		}
		if err := os.MkdirAll(filepath.Dir(linkInStaging), 0o755); err != nil {
			return fmt.Errorf("toolchain: mkdir for symlink %s: %w", sl.link, err)
		}
		// Use the target path as a relative symlink so the rootfs is
		// self-contained regardless of mount point.
		rel, err := filepath.Rel(filepath.Dir(linkInStaging), targetInStaging)
		if err != nil {
			return fmt.Errorf("toolchain: rel path for symlink %s: %w", sl.link, err)
		}
		if err := os.Symlink(rel, linkInStaging); err != nil {
			return fmt.Errorf("toolchain: symlink %s → %s: %w", sl.link, sl.target, err)
		}
	}

	// ── 3. Inject toolchain packages for in-guest operations ─────────────────
	// RunBuilderRole uses mke2fs (e2fsprogs) to write the solved container
	// rootfs to /dev/vdc as a raw ext4 image. The Alpine base may have a
	// busybox stub at /sbin/mke2fs that does not support the -d flag.
	// nexus-agent also sets up zram swap via zramctl (util-linux-misc); the
	// moby/buildkit Alpine base ships no zramctl at all.
	if err := injectToolchain(ctx, stagingDir); err != nil {
		return fmt.Errorf("toolchain: inject toolchain packages: %w", err)
	}

	return nil
}

// toolchainPackages: libraries precede the binaries that need them; any change re-keys the image cache filename.
var toolchainPackages = []string{
	"e2fsprogs-libs-1.47.1-r1.apk",
	"libcom_err-1.47.1-r1.apk",
	"libeconf-0.6.3-r0.apk", // libblkid.so.1 → libeconf.so.0
	"libblkid-2.40.4-r1.apk",
	"libuuid-2.40.4-r1.apk",
	"libmount-2.40.4-r1.apk",
	"e2fsprogs-1.47.1-r1.apk",
	"e2fsprogs-extra-1.47.1-r1.apk", // resize2fs
	"libsmartcols-2.40.4-r1.apk",    // libsmartcols.so.1 (needed by zramctl)
	"util-linux-misc-2.40.4-r1.apk", // zramctl at sbin/zramctl; ELF needs only libsmartcols+musl
}

func toolchainFingerprint(pkgs []string) string {
	sum := sha256.Sum256([]byte(strings.Join(pkgs, "\n")))
	return fmt.Sprintf("%x", sum[:4])
}

// injectToolchain downloads toolchainPackages (e2fsprogs + util-linux-misc and
// their Alpine shared library dependencies) into stagingDir.
func injectToolchain(ctx context.Context, stagingDir string) error {
	const alpineBase = "https://dl-cdn.alpinelinux.org/alpine/v3.21/main/x86_64"
	slog.Info("builderimage: injecting toolchain packages into builder rootfs", "packages", len(toolchainPackages))
	for _, pkg := range toolchainPackages {
		slog.Info("builderimage: downloading Alpine package", "pkg", pkg)
		if err := extractAlpinePkg(ctx, alpineBase+"/"+pkg, stagingDir); err != nil {
			return fmt.Errorf("%s: %w", pkg, err)
		}
	}
	slog.Info("builderimage: toolchain injection complete")
	return nil
}

// extractAlpinePkg downloads an Alpine .apk file from url and extracts its
// non-metadata contents into dstDir. Alpine .apk files are gzip-compressed
// tar archives; entries with names beginning with '.' are metadata and are
// skipped.
func extractAlpinePkg(ctx context.Context, url, dstDir string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		// Skip Alpine package metadata (signature, PKGINFO, install hooks).
		if strings.HasPrefix(hdr.Name, ".") {
			continue
		}
		dstPath := filepath.Join(dstDir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dstPath, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("mkdir %s: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return fmt.Errorf("mkdir parent %s: %w", hdr.Name, err)
			}
			_ = os.Remove(dstPath)
			f, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create %s: %w", hdr.Name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("write %s: %w", hdr.Name, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close %s: %w", hdr.Name, err)
			}
		case tar.TypeSymlink:
			_ = os.Remove(dstPath)
			if err := os.Symlink(hdr.Linkname, dstPath); err != nil {
				return fmt.Errorf("symlink %s: %w", hdr.Name, err)
			}
		}
	}
	return nil
}

// DiscoverBuildkitdPath returns the in-rootfs absolute path at which buildkitd
// is installed in the staging directory. It searches [buildkitdCandidates] in
// order and returns the first match, or an error if none are found.
//
// The returned path is an absolute in-rootfs path (e.g. "/usr/bin/buildkitd"),
// suitable for use in CH cmdline flags and InGuestBuildOptions.BuildkitdPath.
func DiscoverBuildkitdPath(stagingDir string) (string, error) {
	for _, c := range buildkitdCandidates {
		if _, err := os.Lstat(filepath.Join(stagingDir, c[1:])); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("buildkitd not found in rootfs %s; tried %v",
		stagingDir, buildkitdCandidates)
}

// DiscoverBuildctlPath returns the in-rootfs absolute path of the buildctl
// client binary, searching [buildctlCandidates] in order.
func DiscoverBuildctlPath(stagingDir string) (string, error) {
	for _, c := range buildctlCandidates {
		if _, err := os.Lstat(filepath.Join(stagingDir, c[1:])); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("buildctl not found in rootfs %s; tried %v",
		stagingDir, buildctlCandidates)
}
