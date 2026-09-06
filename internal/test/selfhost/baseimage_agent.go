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
// caller (typically "/usr/bin:/bin:/usr/sbin:/sbin") and does NOT include
// /usr/local/bin or /usr/local/go/bin. The Containerfile creates /usr/bin
// symlinks for node, claude, gh, go, and gofmt. It also writes /etc/environment
// and /etc/profile.d/nexus3-go.sh so that nexus3-agent's readEtcEnvironment()
// and login shells both pick up GOPATH, GOMODCACHE, and PATH.
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
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// NodeVersion, nodeSHA256AMD64, and ClaudeCodeVersion are sourced from
// cred.ClaudeCodeProfile.ToolRecipe so that the agent base image and the
// recipe layer always install the same Node.js and claude-code builds.
// They are package-level vars (not consts) because profile fields are vars.
var (
	// NodeVersion is the Node.js LTS release baked into the agent base image.
	// Single source of truth: cred.ClaudeCodeProfile.ToolRecipe.Packages[0].Version.
	NodeVersion = cred.ClaudeCodeProfile.ToolRecipe.Packages[0].Version

	// nodeSHA256AMD64 is the SHA-256 of the Node.js linux/x64 tarball.
	// Single source of truth: cred.ClaudeCodeProfile.ToolRecipe.Packages[0].SHA256ByArch["x64"].
	nodeSHA256AMD64 = cred.ClaudeCodeProfile.ToolRecipe.Packages[0].SHA256ByArch["x64"]

	// ClaudeCodeVersion is the pinned @anthropic-ai/claude-code release.
	// Single source of truth: cred.ClaudeCodeProfile.ToolRecipe.Packages[1].Version.
	ClaudeCodeVersion = cred.ClaudeCodeProfile.ToolRecipe.Packages[1].Version
)

const (
	// GHVersion is the pinned GitHub CLI (gh) release baked into the agent base image.
	// gh is NOT in Debian bookworm's default apt repos; installed from the upstream
	// Linux tarball following the same pattern as GoVersion/NodeVersion.
	// Verified available at https://github.com/cli/cli/releases on 2026-08-21.
	GHVersion = "2.98.0"

	// ghSHA256AMD64 is the SHA-256 of gh_2.98.0_linux_amd64.tar.gz.
	// Source: https://github.com/cli/cli/releases/download/v2.98.0/gh_2.98.0_checksums.txt
	// (published by cli/cli as part of the v2.98.0 release on 2026-08-20).
	ghSHA256AMD64 = "3b8ac6b30336802fc1a858d7c084e11cdf24ac1a761ca90b68022d7d729208de"

	// agentRef is the human-readable tag stamped on the produced Image.
	agentRef = "nexus3-agent-base"

	// agentDockerTag is the docker image tag used during the build.
	agentDockerTag = "nexus3-agent-base:integration-test"

	// agentImageSizeBytes is the pre-allocated sparse file size passed to mke2fs.
	// 6 GiB = 5 GiB self-host base + ~1 GiB headroom for Node.js (~250 MB) +
	// claude-code (~200 KB npm bundle) + gh binary (~45 MB) + ext4 metadata + future growth.
	// curl (libcurl4 ~2 MB) is also included. Total additions remain well under the
	// ~750 MiB headroom; agentImageSizeBytes does not need to be bumped.
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

	// Source tree: internal/, cmd/, pkg/, third_party/ — required for in-guest
	// go build/test.  third_party/ is included so that go.mod replace directives
	// (e.g. ./third_party/gvisor-tap-vsock) resolve correctly at /workspace.
	// Copied after the explicit third_party copy above so the loop's copyDir
	// is idempotent (the destination may already exist from the gvisor copy).
	// Only dirs that actually exist are added; the Containerfile is generated from
	// the same list so COPY directives never reference an absent context dir.
	var srcDirs []string
	for _, srcDir := range []string{"internal", "cmd", "pkg", "third_party"} {
		src := filepath.Join(repoRoot, srcDir)
		dst := filepath.Join(ctxDir, srcDir)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue // skip absent dirs (e.g. pkg may not exist yet)
		}
		if err := copyDir(src, dst); err != nil {
			return domain.Image{}, fmt.Errorf("agent-image: copy %s: %w", srcDir, err)
		}
		srcDirs = append(srcDirs, srcDir)
	}

	// Containerfile (generated)
	cf := generateAgentContainerfile(GoVersion, goSHA256AMD64, srcDirs)
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
//  3. gh-fetcher: debian:bookworm-slim + download + verify + extract gh binary.
//  4. mod-seeder: go-fetcher + go mod download all to seed /usr/local/gopath/pkg/mod.
//  5. final: debian:bookworm-slim + Go + Node + gh + seeded module cache +
//     npm install -g claude-code + /usr/bin symlinks + nexus3-agent as /sbin/nexus3-agent.
func generateAgentContainerfile(goVer, goSHA256 string, srcDirs []string) string {
	// Build the source-tree COPY block from the dirs that were actually populated
	// in the build context; avoids referencing absent dirs that would fail docker build.
	var srcCopyLines []string
	for _, d := range srcDirs {
		srcCopyLines = append(srcCopyLines, fmt.Sprintf("COPY %s/ ./%s/", d, d))
	}
	srcCopyBlock := strings.Join(srcCopyLines, "\n")

	return fmt.Sprintf(`# nexus3 agent base image
# Generated by internal/test/selfhost — do not edit manually.
#
# Produces: Debian bookworm-slim + Go %s + Node.js %s + @anthropic-ai/claude-code %s + gh %s
# + git + curl + ca-certs + nexus3-agent + seeded Go module cache.
#
# Node.js source: nodejs.org tarball (not apt — bookworm ships 18.x, need >=22).
# gh source: github.com/cli/cli tarball (not GitHub apt repo — avoids keyring setup).
# Node.js/claude/gh binaries are symlinked into /usr/bin for standard guest PATH.

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

# ── Stage 3: fetch and verify the GitHub CLI (gh) ────────────────────────────
# gh is not in Debian bookworm's default apt repos; mirrors the Go/Node tarball
# pattern: pin version+hash, no apt keyring management, no curl-piped installer.
# Only the gh binary is extracted; the rest of the tarball is discarded.
FROM debian:bookworm-slim AS gh-fetcher
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://github.com/cli/cli/releases/download/v%s/gh_%s_linux_amd64.tar.gz" -o /tmp/gh.tar.gz && \
    echo "%s  /tmp/gh.tar.gz" | sha256sum -c - && \
    tar -xzf /tmp/gh.tar.gz -C /usr/local/bin --strip-components=2 "gh_%s_linux_amd64/bin/gh" && \
    rm /tmp/gh.tar.gz && \
    chmod 0755 /usr/local/bin/gh
RUN /usr/local/bin/gh version

# ── Stage 4: seed the module cache ───────────────────────────────────────────
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

# ── Stage 5: final agent base image ──────────────────────────────────────────
FROM debian:bookworm-slim
# Runtime dependencies: git (workspace ops) + curl (HTTPS requests from the agent) +
# ca-certificates (TLS) + iproute2 (ip link/addr/route for nexus3-agent network init
# at PID 1) + openssh-server (sshd for ORCA vsock:22 SSH bridge).
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends git curl ca-certificates iproute2 openssh-server && \
    rm -rf /var/lib/apt/lists/*

# sshd configuration for ORCA pubkey-only root login.
# authorized_keys are injected at runtime by the seeder to /root/.ssh/authorized_keys
# (GuestAuthorizedKeysPath), which is OpenSSH's default — no AuthorizedKeysFile override needed.
RUN printf 'PermitRootLogin prohibit-password\nPasswordAuthentication no\nPubkeyAuthentication yes\n' \
    > /etc/ssh/sshd_config.d/99-nexus3-orca.conf

# Pre-generate host keys for deterministic image layers; startSSHD also runs
# ssh-keygen -A at boot as a safety net.
RUN ssh-keygen -A

# Go toolchain from stage 1.
COPY --from=go-fetcher /usr/local/go /usr/local/go

# Node.js runtime from stage 2 (node, npm, npx → /usr/local/bin/).
COPY --from=node-fetcher /usr/local /usr/local

# gh binary from stage 3.
COPY --from=gh-fetcher /usr/local/bin/gh /usr/local/bin/gh

# Pre-seeded Go module cache from stage 4.
COPY --from=mod-seeder /usr/local/gopath/pkg/mod /usr/local/gopath/pkg/mod

# Working directory for in-guest go build / go test.
WORKDIR /workspace

# go.mod + go.sum: stable files; placed before source for layer-cache efficiency.
COPY go.mod go.sum ./

# Source tree: volatile; placed after the module-seed layers so that source
# changes do not bust the go mod download cache in the mod-seeder stage.
%s
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
# caller ("...:/usr/bin:/bin:/usr/sbin:/sbin") does NOT include /usr/local/bin
# or /usr/local/go/bin.  Symlink node, claude, gh, and the Go toolchain binaries
# into /usr/bin so all are reachable on the caller-injected PATH.
RUN ln -sf /usr/local/bin/node /usr/bin/node && \
    ln -sf /usr/local/bin/claude /usr/bin/claude && \
    ln -sf /usr/local/bin/gh /usr/bin/gh && \
    ln -sf /usr/local/go/bin/go /usr/bin/go && \
    ln -sf /usr/local/go/bin/gofmt /usr/bin/gofmt

# ── Materialise /etc/environment and /etc/profile.d so env vars survive ext4 ─
# OCI ENV metadata lives only in the image config JSON and is never read by the
# guest: the VM boots init=/sbin/nexus3-agent directly from the ext4 rootfs, so
# no container runtime ever reads Config.Env.  nexus3-agent reads /etc/environment
# via readEtcEnvironment() and merges it into every exec'd process env.
# Values DERIVED from the ENV declarations above — not re-typed literals — so
# that editing the ENV block cannot silently leave these files at stale values.
RUN printf '%%s=%%s\n' \
        GOPATH "$GOPATH" \
        GOMODCACHE "$GOMODCACHE" \
        CGO_ENABLED "$CGO_ENABLED" \
        GOTOOLCHAIN "$GOTOOLCHAIN" \
        PATH "$PATH" \
    > /etc/environment && \
    { echo '# nexus3: Go toolchain env — generated from the Containerfile ENV block.'; \
      echo '# Do not edit; edit the ENV declarations and rebuild.'; \
      sed 's/^/export /' /etc/environment; } \
    > /etc/profile.d/nexus3-go.sh && \
    chmod 0644 /etc/environment /etc/profile.d/nexus3-go.sh && \
    mkdir -p "$GOMODCACHE"

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
		GHVersion,         // gh %s in comment line
		goVer,             // curl URL go%s.linux-amd64.tar.gz
		goSHA256,          // echo "%s  /tmp/go.tar.gz"
		NodeVersion,       // curl URL dist/v%s/
		NodeVersion,       // curl URL node-v%s-linux-x64.tar.gz
		nodeSHA256AMD64,   // echo "%s  /tmp/node.tar.gz"
		GHVersion,         // gh-fetcher curl URL releases/download/v%s/
		GHVersion,         // gh-fetcher curl URL gh_%s_linux_amd64.tar.gz
		ghSHA256AMD64,     // gh-fetcher echo "%s  /tmp/gh.tar.gz"
		GHVersion,         // gh-fetcher tar --strip-components=2 "gh_%s_linux_amd64/bin/gh"
		srcCopyBlock,      // source-tree COPY lines (only dirs present in context)
		ClaudeCodeVersion, // npm install -g @anthropic-ai/claude-code@%s
	)
}
