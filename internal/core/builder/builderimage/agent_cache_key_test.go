//go:build linux

// Package builderimage_test — agent-hash cache-key correctness tests.
//
// These tests verify the fix for the latent "no space left on device" bug:
// the builder ext4 cache must be keyed on BOTH the OCI digest AND the
// nexus3-agent binary, so that a grown or rebuilt agent produces a fresh
// image rather than reusing a stale one sized for a smaller agent.
package builderimage_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/IniZio/nexus3/internal/core/builder/builderimage"
)

// TestAgentCacheKey_DifferentAgentsDifferentPaths verifies that two calls with
// the SAME OCI digest but DIFFERENT agent bytes produce DIFFERENT cache paths
// and each triggers a fresh image build. This is the direct regression test
// for the stale-cache bug: if the cache key ignores the agent, the second call
// returns the first (too-small) ext4 instead of rebuilding.
func TestAgentCacheKey_DifferentAgentsDifferentPaths(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available:", err)
	}

	const fakeDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	agentV1 := []byte("#!/bin/sh\necho nexus3-agent-v1\n")
	agentV2 := []byte("#!/bin/sh\necho nexus3-agent-v2-has-grown-considerably\n")

	img := buildMinimalOCIImage(t)

	pullCount := 0
	builderimage.SetResolveDigestForTest(func(_ context.Context, _ string) (string, error) {
		return fakeDigest, nil
	})
	builderimage.SetPullRemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCount++
		return img, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	dataDir := t.TempDir()
	ctx := context.Background()

	// First call with agentV1 — must pull + build.
	path1, err := builderimage.EnsureBuilderImage(ctx, dataDir, agentV1)
	if err != nil {
		t.Fatalf("call with agentV1: %v", err)
	}
	if pullCount != 1 {
		t.Errorf("pullCount = %d after agentV1 call, want 1", pullCount)
	}

	// Second call with agentV2 and the SAME OCI digest — must NOT hit the V1
	// cache. It must produce a different path and rebuild (pull again).
	path2, err := builderimage.EnsureBuilderImage(ctx, dataDir, agentV2)
	if err != nil {
		t.Fatalf("call with agentV2: %v", err)
	}
	if path1 == path2 {
		t.Errorf("same cache path returned for different agents: %q — stale-cache bug not fixed", path1)
	}
	if pullCount != 2 {
		t.Errorf("pullCount = %d after agentV2 call, want 2 (V2 must trigger rebuild)", pullCount)
	}

	// Both ext4 images must exist and be non-empty.
	for _, p := range []string{path1, path2} {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("stat %q: %v", p, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("ext4 image %q is empty", p)
		}
	}

	// Verify the path naming: each must include the agent hash, not just the
	// OCI digest, so that a human operator can identify stale files.
	base1 := filepath.Base(path1)
	base2 := filepath.Base(path2)
	if base1 == base2 {
		t.Errorf("filenames are identical: %q — agent hash missing from cache key", base1)
	}
}

// TestAgentCacheKey_SameSameAgentCacheHit verifies that calling EnsureBuilderImage
// twice with the SAME agent bytes (same OCI digest) is still a cache hit —
// the agent-hash inclusion must not break the basic deduplication invariant.
func TestAgentCacheKey_SameAgentCacheHit(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available:", err)
	}

	const fakeDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

	agent := []byte("#!/bin/sh\necho nexus3-agent-stable\n")
	img := buildMinimalOCIImage(t)

	pullCount := 0
	builderimage.SetResolveDigestForTest(func(_ context.Context, _ string) (string, error) {
		return fakeDigest, nil
	})
	builderimage.SetPullRemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCount++
		return img, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	dataDir := t.TempDir()
	ctx := context.Background()

	path1, err := builderimage.EnsureBuilderImage(ctx, dataDir, agent)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	path2, err := builderimage.EnsureBuilderImage(ctx, dataDir, agent)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if path1 != path2 {
		t.Errorf("same agent produced different paths: %q vs %q", path1, path2)
	}
	if pullCount != 1 {
		t.Errorf("pullCount = %d, want 1 (second call should be a cache hit)", pullCount)
	}
}

// TestAgentCacheKey_ExtSizeAccommodatesAgent verifies that the builder ext4 is
// sized to accommodate the agent binary with headroom. The ext4 image file
// must be larger than the agent size plus the 64 MiB minimum, ensuring the
// agent baked into the rootfs (two copies: /usr/local/bin and /sbin) never
// causes a "no space left on device" write failure.
func TestAgentCacheKey_ExtSizeAccommodatesAgent(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available:", err)
	}

	const fakeDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	// Use a 512 KiB agent to make the size assertion meaningful.
	largeAgent := make([]byte, 512*1024)
	for i := range largeAgent {
		largeAgent[i] = byte(i & 0xff)
	}

	img := buildMinimalOCIImage(t)
	builderimage.SetResolveDigestForTest(func(_ context.Context, _ string) (string, error) {
		return fakeDigest, nil
	})
	builderimage.SetPullRemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		return img, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	path, err := builderimage.EnsureBuilderImage(context.Background(), t.TempDir(), largeAgent)
	if err != nil {
		t.Fatalf("EnsureBuilderImage: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat ext4: %v", err)
	}

	const imageMinSizeBytes = 64 * 1024 * 1024
	agentLen := int64(len(largeAgent))

	// The ext4 must hold at least two copies of the agent (baked into rootfs)
	// plus the 64 MiB structural minimum. The actual formula is:
	//   sizeBytes = dirSizeBytes * 2 + 64 MiB
	// where dirSizeBytes >= 2 * agentLen, giving sizeBytes >= 4 * agentLen + 64 MiB.
	wantMin := agentLen + imageMinSizeBytes
	if info.Size() < wantMin {
		t.Errorf("ext4 size %d < agent+min (%d+%d = %d): image too small to hold agent",
			info.Size(), agentLen, imageMinSizeBytes, wantMin)
	}
}
