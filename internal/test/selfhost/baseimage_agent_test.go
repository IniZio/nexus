//go:build integration

// Package selfhost_test — integration tests for the agent base image harness.
// Requires docker and mke2fs; excluded from normal "go test ./..." runs:
//
//	go test -tags integration -run TestBuildAgentBaseImage \
//	    ./internal/test/selfhost/ -v -timeout 45m
package selfhost_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/test/selfhost"
)

// TestBuildAgentBaseImage is the acceptance test for S-IMG.
//
// It invokes [selfhost.BuildAgentBaseImage] and asserts:
//  1. The returned image has a valid SHA-256 digest.
//  2. The image is retrievable from the cache (metadata round-trip).
//  3. The ext4 artifact is non-empty and the correct size is recorded.
//  4. The rootfs contains /sbin/nexus3-agent, /usr/local/go/bin/go,
//     /usr/bin/node (symlink → /usr/local/bin/node), and
//     /usr/bin/claude (symlink → /usr/local/bin/claude).
//
// Prerequisites: docker (for build+export) and mke2fs (for ext4 creation).
// The test SKIPs cleanly if either is absent — it does NOT fail.
func TestBuildAgentBaseImage(t *testing.T) {
	// ── Skip guards ───────────────────────────────────────────────────────────

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not in PATH: skipping agent base image build")
	}
	if !builder.Mke2fsAvailable() {
		t.Skip("mke2fs not in PATH: skipping agent base image build")
	}

	// ── Build ─────────────────────────────────────────────────────────────────

	ctx := context.Background()

	cacheDir := t.TempDir()
	cache, err := image.NewCache(cacheDir)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	t.Logf("Building agent base image (Go %s + Node.js %s + claude-code %s + gh %s; first run ~15–30 min)...",
		selfhost.GoVersion, selfhost.NodeVersion, selfhost.ClaudeCodeVersion, selfhost.GHVersion)
	img, err := selfhost.BuildAgentBaseImage(ctx, cache)
	if err != nil {
		if errors.Is(err, selfhost.ErrDockerUnavailable) || errors.Is(err, builder.ErrMke2fsUnavailable) {
			t.Skipf("prerequisite unavailable: %v", err)
		}
		t.Fatalf("BuildAgentBaseImage: %v", err)
	}

	// ── Assertion 1: valid digest ─────────────────────────────────────────────

	if !img.Digest.Valid() {
		t.Errorf("image.Digest is invalid: %q", img.Digest)
	}
	t.Logf("Image digest: %s", img.Digest)
	t.Logf("Image size:   %.2f GiB", float64(img.Size)/(1<<30))

	// ── Assertion 2: retrievable from cache ───────────────────────────────────

	cached, err := cache.Get(ctx, img.Digest)
	if err != nil {
		t.Fatalf("cache.Get: %v", err)
	}
	if cached.Digest != img.Digest {
		t.Errorf("cached digest mismatch: got %s, want %s", cached.Digest, img.Digest)
	}
	if cached.Ref != "nexus3-agent-base" {
		t.Errorf("cached Ref = %q, want %q", cached.Ref, "nexus3-agent-base")
	}

	// ── Assertion 3: non-empty artifact ───────────────────────────────────────

	// Cache stores at <cacheDir>/<algo>/<hex>/artifact (matching image.Cache layout).
	artifactPath := filepath.Join(cacheDir, img.Digest.Algo(), img.Digest.Hex(), "artifact")
	fi, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("stat artifact %s: %v", artifactPath, err)
	}
	if fi.Size() == 0 {
		t.Errorf("artifact file is empty: %s", artifactPath)
	}

	// ── Assertion 4: rootfs structure check (via debugfs) ─────────────────────

	debugfsPath, err := exec.LookPath("debugfs")
	if err != nil {
		t.Log("debugfs not in PATH: skipping rootfs structure check (install e2fsprogs for full verification)")
		return
	}

	checkPath := func(fsPath string) {
		t.Helper()
		out, err := exec.Command(debugfsPath, "-R", "stat "+fsPath, artifactPath).CombinedOutput()
		outStr := string(out)
		if err != nil || strings.Contains(outStr, "File not found") || strings.Contains(outStr, "Inode not found") {
			t.Errorf("debugfs: %q not found in ext4 rootfs\noutput: %s", fsPath, outStr)
			return
		}
		t.Logf("debugfs stat %s: OK", fsPath)
	}

	// Boot contract: nexus3-agent as init
	checkPath("/sbin/nexus3-agent")

	// Go toolchain: required for in-workspace builds
	checkPath("/usr/local/go/bin/go")

	// Node.js: symlinked into /usr/bin for standard guest PATH
	checkPath("/usr/bin/node")

	// Claude Code CLI: symlinked into /usr/bin for standard guest PATH
	checkPath("/usr/bin/claude")

	// gh CLI: symlinked into /usr/bin for standard guest PATH
	checkPath("/usr/bin/gh")

	// The actual binaries behind the /usr/bin symlinks
	checkPath("/usr/local/bin/node")
	checkPath("/usr/local/bin/claude")
	checkPath("/usr/local/bin/gh")

	// The real claude payload: a native ELF installed by npm as an optionalDependency.
	// /usr/local/bin/claude is a wrapper; this is the actual binary it resolves to.
	checkPath("/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe")
}
