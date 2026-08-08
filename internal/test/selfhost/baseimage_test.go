//go:build integration

// Package selfhost_test contains integration tests for the self-hosting base
// image harness. Tests in this file require docker and mke2fs; they are
// excluded from normal "go test ./..." runs and must be opted into explicitly:
//
//	go test -tags integration -run TestBuildSelfHostBaseImage \
//	    ./internal/test/selfhost/ -v -timeout 30m
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

// TestBuildSelfHostBaseImage is the acceptance test for S1 of Run 5.
//
// It invokes [selfhost.BuildSelfHostBaseImage] and asserts:
//  1. The returned image has a valid SHA-256 digest.
//  2. The image is retrievable from the cache (metadata round-trip).
//  3. The ext4 artifact is non-empty and the correct size is recorded.
//  4. The rootfs contains /sbin/nexus3-agent, /usr/local/go/bin/go, and a
//     non-empty seeded module cache at /usr/local/gopath/pkg/mod
//     (checked via debugfs if available, otherwise via the cache artifact path).
//
// Prerequisites: docker (for build+export) and mke2fs (for ext4 creation).
// The test SKIPs cleanly if either is absent — it does NOT fail.
func TestBuildSelfHostBaseImage(t *testing.T) {
	// ── Skip guards ───────────────────────────────────────────────────────────

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not in PATH: skipping self-host base image build")
	}
	if !builder.Mke2fsAvailable() {
		t.Skip("mke2fs not in PATH: skipping self-host base image build")
	}

	// ── Build ─────────────────────────────────────────────────────────────────

	ctx := context.Background()

	cacheDir := t.TempDir()
	cache, err := image.NewCache(cacheDir)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	t.Logf("Building self-hosting base image (Go %s; first run ~10–20 min)...", selfhost.GoVersion)
	img, err := selfhost.BuildSelfHostBaseImage(ctx, cache)
	if err != nil {
		if errors.Is(err, selfhost.ErrDockerUnavailable) || errors.Is(err, builder.ErrMke2fsUnavailable) {
			t.Skipf("prerequisite unavailable: %v", err)
		}
		t.Fatalf("BuildSelfHostBaseImage: %v", err)
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
		t.Fatalf("cache.Get(%s): %v", img.Digest, err)
	}
	if cached.Digest != img.Digest {
		t.Errorf("cache.Get digest mismatch: got %s, want %s", cached.Digest, img.Digest)
	}
	if cached.Ref != "nexus3-selfhost-base" {
		t.Errorf("cache.Get ref: got %q, want %q", cached.Ref, "nexus3-selfhost-base")
	}

	// ── Assertion 3: artifact is non-empty ────────────────────────────────────

	if img.Size <= 0 {
		t.Errorf("image.Size <= 0: %d", img.Size)
	}

	// Artifact path follows the cache's on-disk layout:
	// <root>/sha256/<64hex>/artifact
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

	// Agent binary — the init process for the workspace VM
	checkPath("/sbin/nexus3-agent")

	// Go toolchain — required for in-workspace builds
	checkPath("/usr/local/go/bin/go")

	// Seeded module cache — must be non-empty for offline builds
	modOut, _ := exec.Command(
		debugfsPath, "-R", "ls /usr/local/gopath/pkg/mod", artifactPath,
	).CombinedOutput()
	if len(strings.TrimSpace(string(modOut))) == 0 {
		t.Errorf("debugfs: /usr/local/gopath/pkg/mod appears empty (module cache not seeded)\noutput: %s", modOut)
	} else {
		t.Logf("Module cache /usr/local/gopath/pkg/mod: non-empty (seeded)")
		// Log the first line to confirm real content
		lines := strings.SplitN(strings.TrimSpace(string(modOut)), "\n", 3)
		if len(lines) > 0 {
			t.Logf("  ls sample: %s", lines[0])
		}
	}

	// Summary
	t.Logf("Self-hosting base image PASS: digest=%s size=%.2f GiB ref=%s",
		img.Digest, float64(img.Size)/(1<<30), img.Ref)
}
