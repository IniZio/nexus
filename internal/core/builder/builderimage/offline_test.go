//go:build linux

// Package builderimage_test — G9 offline / cache-hit guard tests.
//
// Two scenarios are verified:
//
//  1. Cached digest hit (no-pull path):
//     EnsureBuilderImage returns the cached path when the ext4 image for the
//     resolved digest already exists on disk. The pullRemoteImage hook is never
//     called.
//
//  2. Network fully unreachable (resolveDigest fails):
//     EnsureBuilderImage returns a clear, actionable error — not a panic and not
//     an opaque failure.
//
// Both tests override only SetResolveDigestForTest (which returns a plain
// string) and do NOT import github.com/google/go-containerregistry. This
// keeps the builderimage test binary small enough to link in the 8 GiB
// in-guest KVM environment without OOM (see testmain_test.go for context).
//
// For tests that exercise the full pull path (SetPullRemoteImageForTest with a
// real v1.Image mock), see image_test.go in this package.
package builderimage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder/builderimage"
)

// fakeAgentBytesOffline is a minimal non-empty agent binary placeholder.
var fakeAgentBytesOffline = []byte("#!/bin/sh\necho nexus3-agent-fake\n")

// TestEnsureBuilderImage_CachedOffline verifies that when the ext4 image for a
// given digest is already on disk, EnsureBuilderImage returns immediately
// without proceeding to pullRemoteImage.
//
// This is the "offline cache hit" guard: a cached builder VM image must be
// reusable without any network access beyond the lightweight resolveDigest
// call (here overridden to a no-op).
//
// Setup:
//   - resolveDigest (overridden) returns a known fake digest — no registry hit.
//   - The ext4 cache file for that digest is pre-created with non-zero content.
//   - pullRemoteImage is NOT overridden — the code path that calls it is never
//     reached because EnsureBuilderImage returns at the cache-hit check.
//
// Expected: EnsureBuilderImage returns the cached path and no error.
func TestEnsureBuilderImage_CachedOffline(t *testing.T) {
	t.Cleanup(builderimage.ResetTestOverrides)

	const fakeDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

	// Override resolveDigest so it returns our known digest without network.
	builderimage.SetResolveDigestForTest(func(_ context.Context, _ string) (string, error) {
		return fakeDigest, nil
	})

	dataDir := t.TempDir()

	// Pre-populate the cache using the same path formula EnsureBuilderImage uses
	// (digest + agent hash). BuilderImageCachePathForTest delegates to the same
	// unexported helper, so the test stays in sync with the production code.
	cachePath := builderimage.BuilderImageCachePathForTest(dataDir, fakeDigest, fakeAgentBytesOffline)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatalf("pre-create images dir: %v", err)
	}
	// Write non-zero content so the size > 0 check passes.
	if err := os.WriteFile(cachePath, []byte("fake-ext4-content"), 0o644); err != nil {
		t.Fatalf("pre-create cache file: %v", err)
	}

	got, err := builderimage.EnsureBuilderImage(context.Background(), dataDir, fakeAgentBytesOffline)
	if err != nil {
		t.Fatalf("EnsureBuilderImage with cached image: unexpected error: %v", err)
	}
	if got != cachePath {
		t.Errorf("got path %q, want %q", got, cachePath)
	}
}

// TestEnsureBuilderImage_ResolveDigestFails verifies that when resolveDigest
// fails (simulating a fully offline environment — no DNS, no registry ping),
// EnsureBuilderImage returns a clear, actionable error.
//
// This is the "uncached + network unreachable" guard. The first network call
// (resolveDigest) fails immediately, so EnsureBuilderImage must surface the
// failure without panicking or returning a misleading message.
func TestEnsureBuilderImage_ResolveDigestFails(t *testing.T) {
	t.Cleanup(builderimage.ResetTestOverrides)

	const networkErrMsg = "G9: simulated network unreachable (no DNS)"

	builderimage.SetResolveDigestForTest(func(_ context.Context, _ string) (string, error) {
		return "", errors.New(networkErrMsg)
	})

	dataDir := t.TempDir() // no cache file created

	result, err := builderimage.EnsureBuilderImage(context.Background(), dataDir, fakeAgentBytesOffline)

	if err == nil {
		t.Fatalf("EnsureBuilderImage with offline resolveDigest: want error, got nil (path=%q)", result)
	}
	if !strings.Contains(err.Error(), networkErrMsg) {
		t.Errorf("error %q does not contain network error message %q — not actionable", err.Error(), networkErrMsg)
	}
	if result != "" {
		t.Errorf("want empty path on error, got %q", result)
	}
}
