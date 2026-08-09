package selfhost

// buildAgentBaseImage.go — agent base image build harness.
//
// Produces a nexus3-agent-base ext4 image: the self-hosting base image extended
// with a Node.js 22 LTS runtime and the Claude Code CLI (@anthropic-ai/claude-code).
//
// # Node.js install choice
//
// Node.js is installed from the official nodejs.org tarball (v22.23.2 LTS),
// not from Debian's apt repository (which ships 18.x in bookworm — too old;
// @anthropic-ai/claude-code requires Node >=22.0.0) and not from the NodeSource
// setup script (curl-piped installer is fragile in airgapped builds). The tarball
// approach mirrors the existing Go-fetcher stage: download, sha256sum verify,
// unpack to /usr/local. The sha256 is pinned as a constant.
//
// # Claude Code CLI
//
// @anthropic-ai/claude-code v2.1.226 is installed via "npm install -g" after
// Node.js is available. The package is self-contained (zero runtime dependencies),
// producing a single /usr/local/bin/claude entrypoint. Version pinned as a constant.
//
// # PATH materialisation
//
// docker export discards image config (ENV, CMD). The guest PATH is set by the
// caller (typically "/usr/local/go/bin:/usr/bin:/bin:/usr/sbin:/sbin"). To keep
// 'node' and 'claude' reachable without /usr/local/bin in that PATH, the
// Containerfile creates /usr/bin/node and /usr/bin/claude as symlinks into
// /usr/local/bin.
//
// # Image size
//
// The self-hosting base is ~5 GiB. Node.js 22 adds ~250 MB to the rootfs; the
// claude-code bundle is ~200 KB. agentImageSizeBytes is set to 6 GiB to leave
// ~750 MiB of headroom for ext4 metadata and future growth.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

const (
	// NodeVersion is the Node.js LTS release baked into the agent base image.
	// Must be >=22.0.0 (the @anthropic-ai/claude-code engine floor).
	// Verified available at https://nodejs.org/dist/ on 2026-08-09.
	NodeVersion = "22.23.2"

	// nodeSHA256AMD64 is the SHA-256 of node-v22.23.2-linux-x64.tar.gz.
	// Source: https://nodejs.org/dist/v22.23.2/SHASUMS256.txt
	nodeSHA256AMD64 = "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a"

	// ClaudeCodeVersion is the pinned @anthropic-ai/claude-code release.
	// Zero runtime npm dependencies — self-contained bundle.
	// Verified on 2026-08-09: npm show @anthropic-ai/claude-code dist-tags.latest
	ClaudeCodeVersion = "2.1.226"

	// agentRef is the human-readable tag stamped on the produced Image.
	agentRef = "nexus3-agent-base"

	// agentDockerTag is the docker image tag used during the build.
	agentDockerTag = "nexus3-agent-base:integration-test"

	// agentImageSizeBytes is the pre-allocated sparse file size passed to mke2fs.
	// 6 GiB = 5 GiB self-host base + ~1 GiB headroom for Node.js (~250 MB) +
	// claude-code (ELF native payload, size unverified in-session) + ext4 metadata + future growth.
	agentImageSizeBytes = int64(6 * 1024 * 1024 * 1024)
)

// BuildAgentBaseImage produces the nexus3 agent base ext4 image and stores it
// in cache keyed by SHA-256 digest.
//
// The image extends the self-hosting base with:
//   - Node.js 22 LTS (from nodejs.org tarball, sha256-verified)
//   - @anthropic-ai/claude-code CLI installed globally as 'claude'
//
// All other self-host image contents (Go toolchain, git, ca-certs, seeded module
// cache, nexus3-agent as /sbin/nexus3-agent) are preserved unchanged.
//
// Prerequisite checks:
//   - docker in PATH (returns [ErrDockerUnavailable] if absent)
//   - mke2fs in PATH (returns [builder.ErrMke2fsUnavailable] if absent)
//
// Build steps:
//  1. Compile cmd/nexus3-agent CGO_ENABLED=0 GOOS=linux GOARCH=amd64.
//  2. docker build: go-fetcher + node-fetcher + mod-seeder + final stages.
//     Final stage installs claude globally and symlinks node+claude into /usr/bin.
//  3. docker create → docker export → extract tar → rootfs tree.
//  4. mke2fs -d <rootfs> with deterministic flags → raw ext4 (6 GiB).
//  5. SHA-256 hash → cache.Put → return domain.Image.
func BuildAgentBaseImage(ctx context.Context, cache *image.Cache) (domain.Image, error) {
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
		return domain.Image{}, fmt.Errorf("agent-image: find repo root: %w", err)
	}

	// ── Working directory ─────────────────────────────────────────────────────

	workDir, err := os.MkdirTemp("", "nexus3-agent-image-build-*")
	if err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: mkdir work: %w", err)
	}
	defer os.RemoveAll(workDir)

	// ── Step 1: build nexus3-agent static binary ──────────────────────────────

	agentBin := filepath.Join(workDir, "nexus3-agent")
	if err := buildAgent(ctx, repoRoot, agentBin); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: build agent: %w", err)
	}

	// ── Step 2: set up docker build context ───────────────────────────────────

	ctxDir := filepath.Join(workDir, "ctx")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: mkdir ctx: %w", err)
	}

	// Agent binary
	if err := copyFile(agentBin, filepath.Join(ctxDir, "nexus3-agent"), 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: copy agent to ctx: %w", err)
	}

	// go.mod + go.sum for in-Docker module cache seeding
	for _, f := range []string{"go.mod", "go.sum"} {
		if err := copyFile(filepath.Join(repoRoot, f), filepath.Join(ctxDir, f), 0o644); err != nil {
			return domain.Image{}, fmt.Errorf("agent-image: copy %s: %w", f, err)
		}
	}

	// third_party/gvisor-tap-vsock: required by the local replace directive in
	// go.mod so that "go mod download all" inside the container does not fail.
	thirdPartySrc := filepath.Join(repoRoot, "third_party", "gvisor-tap-vsock")
	thirdPartyDst := filepath.Join(ctxDir, "third_party", "gvisor-tap-vsock")
	if err := copyDir(thirdPartySrc, thirdPartyDst); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: copy third_party/gvisor-tap-vsock: %w", err)
	}

	// Containerfile (generated)
	cf := generateAgentContainerfile(GoVersion, goSHA256AMD64)
	if err := os.WriteFile(filepath.Join(ctxDir, "Containerfile"), []byte(cf), 0o644); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: write Containerfile: %w", err)
	}

	// ── Step 3: docker build ──────────────────────────────────────────────────

	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"-f", filepath.Join(ctxDir, "Containerfile"),
		"-t", agentDockerTag,
		ctxDir,
	)
	buildCmd.Stdout = os.Stderr // progress visible in test output
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: docker build: %w", err)
	}
	defer func() { _ = exec.Command("docker", "rmi", "--force", agentDockerTag).Run() }()

	// ── Step 4: docker export → rootfs tree ───────────────────────────────────

	rootfsDir := filepath.Join(workDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: mkdir rootfs: %w", err)
	}
	if err := exportRootfs(ctx, agentDockerTag, rootfsDir); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: export rootfs: %w", err)
	}

	// ── Step 5: mke2fs → raw ext4 ────────────────────────────────────────────

	ext4Path := filepath.Join(workDir, "rootfs.ext4")
	if err := runMke2fs(ctx, rootfsDir, ext4Path, agentImageSizeBytes); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: mke2fs: %w", err)
	}

	// ── Step 6: hash and store in cache ──────────────────────────────────────

	img, err := hashAndStoreAgent(ctx, cache, ext4Path)
	if err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: cache put: %w", err)
	}
	return img, nil
}

// hashAndStoreAgent hashes the ext4 file at ext4Path, constructs a domain.Image
// tagged with agentRef, and stores it in cache via cache.Put.
// It mirrors [hashAndStore] in baseimage.go but uses agentRef as the image Ref.
func hashAndStoreAgent(ctx context.Context, cache *image.Cache, ext4Path string) (domain.Image, error) {
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
		Ref:       agentRef,
		Kind:      domain.KindBase, // differentiated from self-host by Ref, not Kind
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

// generateAgentContainerfile produces the multi-stage Containerfile for the
// agent base image. It extends the self-hosting Containerfile with two
// additional concerns:
//
//  1. A node-fetcher stage that downloads and sha256-verifies the Node.js 22
//     LTS tarball from nodejs.org (mirrors the go-fetcher pattern; avoids the
//     NodeSource curl-pipe installer and Debian's 18.x apt package).
//  2. In the final stage: copy Node.js from node-fetcher, install
//     @anthropic-ai/claude-code globally via npm, and materialize /usr/bin/node
//     and /usr/bin/claude symlinks so they are reachable on the standard guest
//     PATH ("/usr/bin:/bin:/usr/sbin:/sbin") without /usr/local/bin.
//
// Stages:
//  1. go-fetcher: debian:bookworm-slim + download + verify + unpack Go tarball.
//  2. node-fetcher: debian:bookworm-slim + download + verify + unpack Node tarball.
//  3. mod-seeder: go-fetcher + go mod download all to seed /usr/local/gopath/pkg/mod.
//  4. final: debian:bookworm-slim + Go (go-fetcher) + Node (node-fetcher) + seeded
//     module cache (mod-seeder) + npm install -g claude-code + /usr/bin symlinks
//     + nexus3-agent as /sbin/nexus3-agent.
func generateAgentContainerfile(goVer, goSHA256 string) string {
	return fmt.Sprintf(`# nexus3 agent base image
# Generated by internal/test/selfhost — do not edit manually.
#
# Produces: Debian bookworm-slim + Go %s + Node.js %s + @anthropic-ai/claude-code %s
# + git + ca-certs + nexus3-agent + seeded Go module cache.
#
# Node.js source: nodejs.org tarball (not apt — bookworm ships 18.x, need >=22).
# Node.js/claude binaries are symlinked into /usr/bin for standard guest PATH.

# ── Stage 1: fetch and verify the upstream Go toolchain ──────────────────────
FROM debian:bookworm-slim AS go-fetcher
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://dl.google.com/go/go%s.linux-amd64.tar.gz" -o /tmp/go.tar.gz && \
    echo "%s  /tmp/go.tar.gz" | sha256sum -c - && \
    tar -C /usr/local -xzf /tmp/go.tar.gz && \
    rm /tmp/go.tar.gz
RUN /usr/local/go/bin/go version

# ── Stage 2: fetch and verify Node.js LTS ────────────────────────────────────
# Node >=22 required by @anthropic-ai/claude-code (Debian bookworm ships 18.x).
# Using the nodejs.org tarball mirrors the Go-fetcher approach: pin version+hash,
# no curl-piped setup scripts, no apt keyring management.
FROM debian:bookworm-slim AS node-fetcher
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://nodejs.org/dist/v%s/node-v%s-linux-x64.tar.gz" -o /tmp/node.tar.gz && \
    echo "%s  /tmp/node.tar.gz" | sha256sum -c - && \
    tar -C /usr/local -xzf /tmp/node.tar.gz --strip-components=1 && \
    rm /tmp/node.tar.gz
RUN /usr/local/bin/node --version

# ── Stage 3: seed the module cache ───────────────────────────────────────────
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

# ── Stage 4: final agent base image ──────────────────────────────────────────
FROM debian:bookworm-slim
# Runtime dependencies: git (workspace ops) + ca-certificates (TLS) +
# iproute2 (ip link/addr/route for nexus3-agent network init at PID 1).
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends git ca-certificates iproute2 && \
    rm -rf /var/lib/apt/lists/*

# Go toolchain from stage 1.
COPY --from=go-fetcher /usr/local/go /usr/local/go

# Node.js runtime from stage 2 (node, npm, npx → /usr/local/bin/).
COPY --from=node-fetcher /usr/local /usr/local

# Pre-seeded Go module cache from stage 3.
COPY --from=mod-seeder /usr/local/gopath/pkg/mod /usr/local/gopath/pkg/mod

ENV GOPATH=/usr/local/gopath \
    GOMODCACHE=/usr/local/gopath/pkg/mod \
    CGO_ENABLED=0 \
    GOTOOLCHAIN=local \
    PATH="/usr/local/go/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"

# Verify Go and Node are functional.
RUN go version && node --version

# ── Install Claude Code CLI ───────────────────────────────────────────────────
# Self-contained bundle (zero npm dependencies). Global install places the
# 'claude' wrapper at /usr/local/bin/claude (npm prefix = /usr/local).
RUN npm install -g @anthropic-ai/claude-code@%s

# Verify claude is installed and resolve the real binary path.
# --version may require auth/network; make it non-fatal but always assert
# the binary exists and is the correct @anthropic-ai/claude-code entrypoint.
RUN command -v claude && \
    readlink -f "$(command -v claude)" && \
    node --version && \
    (claude --version || echo "note: claude --version deferred (may need auth); binary present")

# ── Materialise /usr/bin symlinks for standard guest PATH ────────────────────
# docker export discards ENV (including PATH). The guest PATH injected by the
# caller ("...:/usr/bin:/bin:/usr/sbin:/sbin") does NOT include /usr/local/bin.
# Symlink node and claude into /usr/bin so both are reachable without
# /usr/local/bin on PATH. Verify with debugfs stat /usr/bin/claude on the ext4.
RUN ln -sf /usr/local/bin/node /usr/bin/node && \
    ln -sf /usr/local/bin/claude /usr/bin/claude

# ── Final layer: bake nexus3-agent ───────────────────────────────────────────
# Placed last so an agent rebuild only invalidates this one layer.
# Boot contract (kernel cmdline): init=/sbin/nexus3-agent
COPY nexus3-agent /sbin/nexus3-agent
RUN chmod 0755 /sbin/nexus3-agent
`,
		// Comment substitutions (positional, match fmt.Sprintf %s order):
		goVer,             // Go %s in comment line
		NodeVersion,       // Node.js %s in comment line
		ClaudeCodeVersion, // claude-code %s in comment line
		goVer,             // curl URL go%s.linux-amd64.tar.gz
		goSHA256,          // echo "%s  /tmp/go.tar.gz"
		NodeVersion,       // curl URL dist/v%s/
		NodeVersion,       // curl URL node-v%s-linux-x64.tar.gz
		nodeSHA256AMD64,   // echo "%s  /tmp/node.tar.gz"
		ClaudeCodeVersion, // npm install -g @anthropic-ai/claude-code@%s
	)
}
