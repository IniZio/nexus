// Package selfhost provides a build harness for the nexus3 self-hosting base
// ext4 image. The image bootstraps a nexus3 development workspace so that
// nexus3 can be edited, built, and unit-tested entirely in-workspace without
// needing the host environment.
//
// # Image contents
//
//   - Debian bookworm-slim (glibc >= 2.28; VS Code Remote-SSH requirement, ticket 14)
//   - Upstream Go go1.26.5 (satisfies the module's "go 1.25.0" directive;
//     Debian bookworm ships Go 1.19 — too old, prototype-28 finding)
//   - git + ca-certificates; NO gcc / build-essential (CGO_ENABLED=0 throughout)
//   - nexus3-agent compiled CGO_ENABLED=0 static, installed as /sbin/nexus3-agent
//   - nexus3's Go module cache pre-seeded at /usr/local/gopath/pkg/mod so that
//     an in-workspace "go build ./..." needs no network; prototype 28 measured
//     cold build at 32 s (incremental 11 s, per-pkg test 2 s) with this cache.
//
// # Availability guards
//
// Both docker and mke2fs are required. If either is absent the function returns
// a sentinel error ([ErrDockerUnavailable] or [builder.ErrMke2fsUnavailable])
// that callers and tests should treat as a skip signal, not a failure.
//
// # Non-reproducibility note
//
// The deterministic mke2fs flags (fixed UUID, hash seed, SOURCE_DATE_EPOCH=0)
// are inherited from [builder.ext4], but the ext4 digest will still vary
// run-to-run because apt downloads, Go module timestamps, and the Docker layer
// graph are not bit-for-bit reproducible. The digest identifies a specific
// build artifact, not a deterministic content hash.
package selfhost

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

const (
	// GoVersion is the upstream Go toolchain baked into the self-hosting base
	// image. It satisfies the module's "go 1.25.0" directive and was verified
	// available at https://go.dev/dl/ on 2026-08-07.
	GoVersion = "1.26.5"

	// goSHA256AMD64 is the SHA-256 of go1.26.5.linux-amd64.tar.gz.
	// Source: https://go.dev/dl/#go1.26.5
	goSHA256AMD64 = "5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053"

	// selfHostRef is the human-readable tag stamped on the produced Image.
	selfHostRef = "nexus3-selfhost-base"

	// selfHostDockerTag is the docker image tag used during the build.
	selfHostDockerTag = "nexus3-selfhost-base:integration-test"

	// imageSizeBytes is the pre-allocated sparse file size passed to mke2fs.
	// Sized generously to hold: Debian base (~100 MB) + Go toolchain (~500 MB)
	// + seeded module cache (~1.2 GB from prototype-28 + go mod download data)
	// + ext4 metadata headroom. 5 GiB leaves ample room.
	imageSizeBytes = int64(5 * 1024 * 1024 * 1024)

	// deterministicUUID and deterministicHashSeed match the constants in
	// internal/core/builder/ext4.go. Fixed values ensure that two builds of
	// identical content produce identical bytes (content-addressability).
	deterministicUUID     = "00000000-0000-0000-0000-000000000000"
	deterministicHashSeed = "00000000-0000-0000-0000-000000000000"
)

// ErrDockerUnavailable is returned by [BuildSelfHostBaseImage] when docker is
// not found on the host PATH. Tests should treat this as a SKIP signal.
var ErrDockerUnavailable = errors.New("docker not found in PATH (install docker-ce or docker.io)")

// BuildSelfHostBaseImage produces the nexus3 self-hosting base ext4 image and
// stores it in cache keyed by SHA-256 digest.
//
// The caller provides an [image.Cache] to receive the finished artifact.
// The function returns a [domain.Image] with Digest, Ref, Kind, Size, and
// CreatedAt populated; the ext4 raw bytes are retrievable via cache.Open.
//
// Prerequisite checks:
//   - docker in PATH (returns [ErrDockerUnavailable] if absent)
//   - mke2fs in PATH (returns [builder.ErrMke2fsUnavailable] if absent)
//
// Build steps:
//  1. Compile cmd/nexus3-agent CGO_ENABLED=0 GOOS=linux GOARCH=amd64.
//  2. docker build: fetch Go 1.26.5, run go mod download all (GOTOOLCHAIN=local)
//     to seed /usr/local/gopath/pkg/mod, install git+ca-certs, copy agent.
//  3. docker create → docker export → extract tar → rootfs tree.
//  4. mke2fs -d <rootfs> with deterministic flags → raw ext4.
//  5. SHA-256 hash → cache.Put → return domain.Image.
func BuildSelfHostBaseImage(ctx context.Context, cache *image.Cache) (domain.Image, error) {
	// ── Prerequisite checks ───────────────────────────────────────────────────

	if _, err := exec.LookPath("docker"); err != nil {
		return domain.Image{}, ErrDockerUnavailable
	}
	if !builder.Mke2fsAvailable() {
		return domain.Image{}, builder.ErrMke2fsUnavailable
	}

	// ── Locate repo root ──────────────────────────────────────────────────────

	repoRoot, err := findRepoRoot()
	if err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: find repo root: %w", err)
	}

	// ── Working directory ─────────────────────────────────────────────────────

	workDir, err := os.MkdirTemp("", "nexus3-selfhost-build-*")
	if err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: mkdir work: %w", err)
	}
	defer os.RemoveAll(workDir)

	// ── Step 1: build nexus3-agent static binary ──────────────────────────────

	agentBin := filepath.Join(workDir, "nexus3-agent")
	if err := buildAgent(ctx, repoRoot, agentBin); err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: build agent: %w", err)
	}

	// ── Step 2: set up docker build context ───────────────────────────────────

	ctxDir := filepath.Join(workDir, "ctx")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: mkdir ctx: %w", err)
	}

	// Agent binary
	if err := copyFile(agentBin, filepath.Join(ctxDir, "nexus3-agent"), 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: copy agent to ctx: %w", err)
	}

	// go.mod + go.sum for in-Docker module cache seeding
	for _, f := range []string{"go.mod", "go.sum"} {
		if err := copyFile(filepath.Join(repoRoot, f), filepath.Join(ctxDir, f), 0o644); err != nil {
			return domain.Image{}, fmt.Errorf("selfhost: copy %s: %w", f, err)
		}
	}

	// third_party/gvisor-tap-vsock: required because go.mod has a local replace
	// directive pointing to this path (added in D-P1-12 / commit 9f50c6e).
	// Without it, "go mod download all" inside the container fails with
	// "open /seed/third_party/gvisor-tap-vsock/go.mod: no such file".
	thirdPartySrc := filepath.Join(repoRoot, "third_party", "gvisor-tap-vsock")
	thirdPartyDst := filepath.Join(ctxDir, "third_party", "gvisor-tap-vsock")
	if err := copyDir(thirdPartySrc, thirdPartyDst); err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: copy third_party/gvisor-tap-vsock: %w", err)
	}

	// Containerfile (generated)
	cf := generateContainerfile(GoVersion, goSHA256AMD64)
	if err := os.WriteFile(filepath.Join(ctxDir, "Containerfile"), []byte(cf), 0o644); err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: write Containerfile: %w", err)
	}

	// ── Step 3: docker build ──────────────────────────────────────────────────

	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"-f", filepath.Join(ctxDir, "Containerfile"),
		"-t", selfHostDockerTag,
		ctxDir,
	)
	buildCmd.Stdout = os.Stderr // progress visible in test output
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: docker build: %w", err)
	}
	defer func() { _ = exec.Command("docker", "rmi", "--force", selfHostDockerTag).Run() }()

	// ── Step 4: docker export → rootfs tree ───────────────────────────────────

	rootfsDir := filepath.Join(workDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: mkdir rootfs: %w", err)
	}
	if err := exportRootfs(ctx, selfHostDockerTag, rootfsDir); err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: export rootfs: %w", err)
	}

	// ── Step 5: mke2fs → raw ext4 ────────────────────────────────────────────

	ext4Path := filepath.Join(workDir, "rootfs.ext4")
	if err := runMke2fs(ctx, rootfsDir, ext4Path, imageSizeBytes); err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: mke2fs: %w", err)
	}

	// ── Step 6: hash and store in cache ──────────────────────────────────────

	img, err := hashAndStore(ctx, cache, ext4Path)
	if err != nil {
		return domain.Image{}, fmt.Errorf("selfhost: cache put: %w", err)
	}
	return img, nil
}

// ── Private helpers ───────────────────────────────────────────────────────────

// findRepoRoot returns the directory containing go.mod for this module.
func findRepoRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("not inside a Go module (go env GOMOD returned empty or /dev/null)")
	}
	return filepath.Dir(gomod), nil
}

// buildAgent compiles cmd/nexus3-agent as a static CGO_ENABLED=0 linux/amd64
// binary and writes it to dstPath.
//
// The binary is stamped with a build tag of the form "YYYYMMDD-<git-short-sha>"
// via -ldflags so that image staleness can be detected at runtime: when the
// sandbox starts, nexus3-agent logs "starting (pid=1 build=<tag>)" on the
// console, and the operator can compare <tag> against the current git revision.
func buildAgent(ctx context.Context, repoRoot, dstPath string) error {
	buildTag := agentBuildStamp(ctx, repoRoot)
	ldflag := "-X main.agentBuildTag=" + buildTag
	cmd := exec.CommandContext(ctx, "go", "build",
		"-ldflags", ldflag,
		"-o", dstPath,
		"./cmd/nexus3-agent",
	)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build ./cmd/nexus3-agent: %w", err)
	}
	return nil
}

// agentBuildStamp returns a short build tag for embedding in the agent binary.
// Format: "YYYYMMDD-<short-sha>" where short-sha is the first 7 chars of the
// current git HEAD commit.  Falls back to "YYYYMMDD-unknown" if git is
// unavailable.
func agentBuildStamp(ctx context.Context, repoRoot string) string {
	date := time.Now().UTC().Format("20060102")
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short=7", "HEAD").
		Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return date + "-unknown"
	}
	return date + "-" + strings.TrimSpace(string(out))
}

// exportRootfs creates a stopped container from imgTag, streams its filesystem
// as a tar archive, and extracts it into rootfsDir.
func exportRootfs(ctx context.Context, imgTag, rootfsDir string) error {
	// docker create with an explicit command to avoid "No command specified"
	// when the image has no CMD/ENTRYPOINT. We never start the container.
	out, err := exec.CommandContext(ctx, "docker", "create", imgTag, "/bin/true").Output()
	if err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	containerID := strings.TrimSpace(string(out))
	defer func() { _ = exec.Command("docker", "rm", "--force", containerID).Run() }()

	exportCmd := exec.CommandContext(ctx, "docker", "export", containerID)
	exportPipe, err := exportCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("docker export pipe: %w", err)
	}
	if err := exportCmd.Start(); err != nil {
		return fmt.Errorf("docker export start: %w", err)
	}

	if err := extractTar(exportPipe, rootfsDir); err != nil {
		_ = exportCmd.Wait()
		return fmt.Errorf("extract tar: %w", err)
	}
	return exportCmd.Wait()
}

// extractTar extracts a tar stream into dstDir. It handles regular files,
// directories, symlinks, and hard links. Device nodes (char/block/fifo) are
// skipped — for a VM rootfs the guest kernel populates /dev via devtmpfs.
//
// Hard links are deferred and resolved after all regular files are extracted
// so that ordering in the tar does not matter.
func extractTar(r io.Reader, dstDir string) error {
	type hardLink struct{ src, dst string }
	var deferred []hardLink

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		dst := filepath.Join(dstDir, hdr.Name)

		// Path traversal guard: dst must remain inside dstDir.
		rel, err := filepath.Rel(dstDir, dst)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // skip traversal attempts silently
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", dst, err)
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fmt.Errorf("mkdir parent %s: %w", dst, err)
			}
			mode := os.FileMode(hdr.Mode)
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("create %s: %w", dst, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("write %s: %w", dst, err)
			}
			f.Close()

		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fmt.Errorf("mkdir parent for symlink %s: %w", dst, err)
			}
			_ = os.Remove(dst) // idempotent
			if err := os.Symlink(hdr.Linkname, dst); err != nil {
				return fmt.Errorf("symlink %s -> %s: %w", dst, hdr.Linkname, err)
			}

		case tar.TypeLink:
			// Hard link: target might not exist yet — defer until after regular files.
			deferred = append(deferred, hardLink{
				src: filepath.Join(dstDir, hdr.Linkname),
				dst: dst,
			})

			// tar.TypeChar, tar.TypeBlock, tar.TypeFifo: skip — devtmpfs populates /dev.
			// tar.TypeXGlobalHeader, tar.TypeXHeader: handled transparently by archive/tar.
		}
	}

	// Resolve deferred hard links now that all regular files exist.
	for _, hl := range deferred {
		if err := os.MkdirAll(filepath.Dir(hl.dst), 0o755); err != nil {
			return fmt.Errorf("mkdir parent for hard link %s: %w", hl.dst, err)
		}
		_ = os.Remove(hl.dst)
		if err := os.Link(hl.src, hl.dst); err != nil {
			return fmt.Errorf("hard link %s -> %s: %w", hl.dst, hl.src, err)
		}
	}
	return nil
}

// runMke2fs creates a raw ext4 image at dstPath from the contents of srcDir.
//
// Uses deterministic parameters matching internal/core/builder/ext4.go:
//   - fixed UUID (no random bytes)
//   - fixed HTree hash seed
//   - SOURCE_DATE_EPOCH=0 (epoch timestamps in superblock)
//
// Requires mke2fs from e2fsprogs; returns [builder.ErrMke2fsUnavailable] if absent.
func runMke2fs(ctx context.Context, srcDir, dstPath string, sizeBytes int64) error {
	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		return builder.ErrMke2fsUnavailable
	}

	// Pre-allocate a sparse file; mke2fs reads its size automatically.
	f, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create image file: %w", err)
	}
	f.Close()
	if err := os.Truncate(dstPath, sizeBytes); err != nil {
		return fmt.Errorf("truncate image file to %d bytes: %w", sizeBytes, err)
	}

	cmd := exec.CommandContext(ctx, mke2fsPath,
		"-t", "ext4",
		"-d", srcDir,
		"-U", deterministicUUID,
		"-E", "hash_seed="+deterministicHashSeed,
		dstPath,
	)
	// SOURCE_DATE_EPOCH=0 forces all superblock timestamps to the Unix epoch.
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=0")
	// Use CombinedOutput only (do not also set cmd.Stdout — that conflicts).
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mke2fs failed: %w\n%s", err, out)
	}
	return nil
}

// hashAndStore hashes the ext4 file at ext4Path, constructs a domain.Image,
// and stores it in cache via cache.Put. The cache re-hashes the stream to
// verify integrity, so the file must remain present and readable.
func hashAndStore(ctx context.Context, cache *image.Cache, ext4Path string) (domain.Image, error) {
	f, err := os.Open(ext4Path)
	if err != nil {
		return domain.Image{}, fmt.Errorf("open ext4: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return domain.Image{}, fmt.Errorf("stat ext4: %w", err)
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return domain.Image{}, fmt.Errorf("hash ext4: %w", err)
	}
	digest, err := domain.ParseDigest("sha256:" + hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return domain.Image{}, fmt.Errorf("parse digest: %w", err)
	}

	img := domain.Image{
		Digest:    digest,
		Ref:       selfHostRef,
		Kind:      domain.KindBase,
		Size:      info.Size(),
		CreatedAt: time.Now().UTC(),
	}

	// Seek back to start so cache.Put can re-stream and verify.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return domain.Image{}, fmt.Errorf("seek ext4: %w", err)
	}
	if err := cache.Put(ctx, img, f); err != nil {
		return domain.Image{}, fmt.Errorf("cache.Put: %w", err)
	}
	return img, nil
}

// copyDir recursively copies all files from srcDir into dstDir, preserving
// relative paths. Directory entries are created with 0o755; files inherit their
// source permissions. This is used to stage local replace-directive modules
// (e.g. third_party/gvisor-tap-vsock) into the Docker build context so that
// "go mod download all" inside the container can resolve the replace path.
func copyDir(srcDir, dstDir string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		return copyFile(path, dst, info.Mode())
	})
}

// copyFile copies src to dst with the given file mode.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// generateContainerfile produces the multi-stage Containerfile content for the
// self-hosting base image.
//
// Stages:
//  1. go-fetcher: debian:bookworm-slim + download + verify + unpack Go tarball.
//  2. mod-seeder: go-fetcher + GOTOOLCHAIN=local + go mod download all to seed
//     /usr/local/gopath/pkg/mod without re-downloading the Go toolchain binary.
//  3. final: debian:bookworm-slim + Go toolchain (from go-fetcher) + seeded module
//     cache (from mod-seeder) + git + ca-certificates + nexus3-agent as /sbin/init.
func generateContainerfile(goVer, goSHA256 string) string {
	return fmt.Sprintf(`# nexus3 self-hosting base image
# Generated by internal/test/selfhost — do not edit manually.
#
# Produces: Debian bookworm-slim + Go %s + git + ca-certs + nexus3-agent
# + seeded Go module cache for offline in-workspace builds.

# ── Stage 1: fetch and verify the upstream Go toolchain ──────────────────────
FROM debian:bookworm-slim AS go-fetcher
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://dl.google.com/go/go%s.linux-amd64.tar.gz" -o /tmp/go.tar.gz && \
    echo "%s  /tmp/go.tar.gz" | sha256sum -c - && \
    tar -C /usr/local -xzf /tmp/go.tar.gz && \
    rm /tmp/go.tar.gz
# Verify Go is functional before proceeding.
RUN /usr/local/go/bin/go version

# ── Stage 2: seed the module cache ───────────────────────────────────────────
# Run go mod download all inside the build so the seeded cache is baked in as a
# Docker layer. GOTOOLCHAIN=local prevents a redundant toolchain re-download
# (go %s is already installed above and satisfies the module's "go 1.25.0" directive).
FROM go-fetcher AS mod-seeder
WORKDIR /seed
COPY go.mod go.sum ./
COPY third_party/ ./third_party/
ENV GOPATH=/usr/local/gopath \
    GOMODCACHE=/usr/local/gopath/pkg/mod \
    CGO_ENABLED=0 \
    GOTOOLCHAIN=local \
    PATH="/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
RUN go mod download all

# ── Stage 3: final base image ─────────────────────────────────────────────────
FROM debian:bookworm-slim
# Runtime dependencies: git (workspace operations) + ca-certificates (TLS).
# Explicitly excluded: gcc, build-essential, binutils (CGO_ENABLED=0 throughout).
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends git ca-certificates openssh-server && \
    rm -rf /var/lib/apt/lists/*
RUN printf 'PermitRootLogin prohibit-password\nPasswordAuthentication no\nPubkeyAuthentication yes\n' > /etc/ssh/sshd_config.d/99-nexus3-orca.conf
RUN ssh-keygen -A

# Install the upstream Go toolchain from stage 1.
COPY --from=go-fetcher /usr/local/go /usr/local/go

# Bake in the pre-seeded module cache from stage 2 so in-workspace builds
# resolve all dependencies without network access.
COPY --from=mod-seeder /usr/local/gopath/pkg/mod /usr/local/gopath/pkg/mod

ENV GOPATH=/usr/local/gopath \
    GOMODCACHE=/usr/local/gopath/pkg/mod \
    CGO_ENABLED=0 \
    GOTOOLCHAIN=local \
    PATH="/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

# Verify Go is functional in the final stage.
RUN go version

# Fix /root ownership: apt-get package installs (e.g. openssh-server postinst)
# may allocate uid 1003 and write skel files (.bashrc, .profile, .config) into
# /root, leaving the directory owned by that uid. sshd StrictModes rejects
# pubkey login for root when root's home dir is not owned by root (uid 0).
# This chown runs after all apt installs and ssh-keygen so nothing re-owns /root.
RUN chown -R root:root /root

# ── Final layer: bake nexus3-agent ───────────────────────────────────────────
# Placed last so an agent rebuild only invalidates this one layer.
# Boot contract (kernel cmdline): init=/sbin/nexus3-agent
COPY nexus3-agent /sbin/nexus3-agent
RUN chmod 0755 /sbin/nexus3-agent
`, goVer, goVer, goSHA256, goVer)
}
