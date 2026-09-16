package selfhost

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

// Version vars sourced from cred.ClaudeCodeProfile.ToolRecipe so agent image and recipe layer install the same builds.
var (
	NodeVersion       = cred.ClaudeCodeProfile.ToolRecipe.Packages[0].Version
	nodeSHA256AMD64   = cred.ClaudeCodeProfile.ToolRecipe.Packages[0].SHA256ByArch["x64"]
	ClaudeCodeVersion = cred.ClaudeCodeProfile.ToolRecipe.Packages[1].Version
)

const (
	GHVersion = "2.98.0"
	// ghSHA256AMD64 source: https://github.com/cli/cli/releases/download/v2.98.0/gh_2.98.0_checksums.txt
	ghSHA256AMD64       = "3b8ac6b30336802fc1a858d7c084e11cdf24ac1a761ca90b68022d7d729208de"
	agentRef            = "nexus3-agent-base"
	agentDockerTag      = "nexus3-agent-base:integration-test"
	agentImageSizeBytes = int64(6 * 1024 * 1024 * 1024)
)

func BuildAgentBaseImage(ctx context.Context, cache *image.Cache) (domain.Image, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return domain.Image{}, ErrDockerUnavailable
	}
	if !builder.Mke2fsAvailable() {
		return domain.Image{}, builder.ErrMke2fsUnavailable
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: find repo root: %w", err)
	}

	workDir, err := os.MkdirTemp("", "nexus3-agent-image-build-*")
	if err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: mkdir work: %w", err)
	}
	defer os.RemoveAll(workDir)

	agentBin := filepath.Join(workDir, "nexus3-agent")
	if err := buildAgent(ctx, repoRoot, agentBin); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: build agent: %w", err)
	}

	ctxDir := filepath.Join(workDir, "ctx")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: mkdir ctx: %w", err)
	}

	if err := copyFile(agentBin, filepath.Join(ctxDir, "nexus3-agent"), 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: copy agent to ctx: %w", err)
	}

	for _, f := range []string{"go.mod", "go.sum"} {
		if err := copyFile(filepath.Join(repoRoot, f), filepath.Join(ctxDir, f), 0o644); err != nil {
			return domain.Image{}, fmt.Errorf("agent-image: copy %s: %w", f, err)
		}
	}

	// third_party/gvisor-tap-vsock required by go.mod replace directive so "go mod download all" does not fail in-container.
	thirdPartySrc := filepath.Join(repoRoot, "third_party", "gvisor-tap-vsock")
	thirdPartyDst := filepath.Join(ctxDir, "third_party", "gvisor-tap-vsock")
	if err := copyDir(thirdPartySrc, thirdPartyDst); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: copy third_party/gvisor-tap-vsock: %w", err)
	}

	var srcDirs []string
	for _, srcDir := range []string{"internal", "cmd", "pkg", "third_party"} {
		src := filepath.Join(repoRoot, srcDir)
		dst := filepath.Join(ctxDir, srcDir)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		if err := copyDir(src, dst); err != nil {
			return domain.Image{}, fmt.Errorf("agent-image: copy %s: %w", srcDir, err)
		}
		srcDirs = append(srcDirs, srcDir)
	}

	cf := generateAgentContainerfile(GoVersion, goSHA256AMD64, srcDirs)
	if err := os.WriteFile(filepath.Join(ctxDir, "Containerfile"), []byte(cf), 0o644); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: write Containerfile: %w", err)
	}

	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"-f", filepath.Join(ctxDir, "Containerfile"),
		"-t", agentDockerTag,
		ctxDir,
	)
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: docker build: %w", err)
	}
	defer func() { _ = exec.Command("docker", "rmi", "--force", agentDockerTag).Run() }()

	rootfsDir := filepath.Join(workDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: mkdir rootfs: %w", err)
	}
	if err := exportRootfs(ctx, agentDockerTag, rootfsDir); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: export rootfs: %w", err)
	}

	ext4Path := filepath.Join(workDir, "rootfs.ext4")
	if err := runMke2fs(ctx, rootfsDir, ext4Path, agentImageSizeBytes); err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: mke2fs: %w", err)
	}

	img, err := hashAndStoreAgent(ctx, cache, ext4Path)
	if err != nil {
		return domain.Image{}, fmt.Errorf("agent-image: cache put: %w", err)
	}
	return img, nil
}

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

func generateAgentContainerfile(goVer, goSHA256 string, srcDirs []string) string {
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
# at PID 1) + openssh-server (sshd for ORCA vsock:22 SSH bridge) + unzip/wget/
# xz-utils/procps (F17: agents fell back to python zipfile / curl without them).
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends git curl ca-certificates iproute2 openssh-server unzip wget xz-utils procps && \
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
		// Positional, and the template has no named verbs: reorder these and the
		// image builds with a checksum matched against the wrong tarball. Order:
		// header go/node/claude/gh; go url+sha; node url x2 + sha; gh url x2 +
		// sha + tar path; src COPY block; claude npm version.
		goVer,
		NodeVersion,
		ClaudeCodeVersion,
		GHVersion,
		goVer,
		goSHA256,
		NodeVersion,
		NodeVersion,
		nodeSHA256AMD64,
		GHVersion,
		GHVersion,
		ghSHA256AMD64,
		GHVersion,
		srcCopyBlock,
		ClaudeCodeVersion,
	)
}
