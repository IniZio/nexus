//go:build linux

package builderimage_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/IniZio/nexus3/internal/core/builder/builderimage"
)

// fakeAgentBytes is a minimal placeholder for the nexus3-agent binary in tests.
var fakeAgentBytes = []byte("#!/bin/sh\necho nexus3-agent-fake\n")

// buildMinimalOCIImage creates a v1.Image with a single layer containing
// /usr/bin/buildkitd and /usr/bin/buildctl stub binaries. No network required.
func buildMinimalOCIImage(t *testing.T) v1.Image {
	t.Helper()

	var rawTar bytes.Buffer
	tw := tar.NewWriter(&rawTar)
	entries := []struct {
		name    string
		content []byte
		isDir   bool
	}{
		{"usr/", nil, true},
		{"usr/bin/", nil, true},
		{"usr/bin/buildkitd", []byte("#!/bin/sh\nexec buildkitd-stub\n"), false},
		{"usr/bin/buildctl", []byte("#!/bin/sh\nexec buildctl-stub\n"), false},
	}
	for _, e := range entries {
		if e.isDir {
			if err := tw.WriteHeader(&tar.Header{
				Name:     e.name,
				Typeflag: tar.TypeDir,
				Mode:     0o755,
			}); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := tw.WriteHeader(&tar.Header{
				Name:     e.name,
				Typeflag: tar.TypeReg,
				Mode:     0o755,
				Size:     int64(len(e.content)),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(e.content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	// tarball.LayerFrom expects an Opener returning a gzip-compressed tar stream.
	rawBytes := rawTar.Bytes()
	opener := tarball.Opener(func() (io.ReadCloser, error) {
		var gzbuf bytes.Buffer
		gw := gzip.NewWriter(&gzbuf)
		if _, err := gw.Write(rawBytes); err != nil {
			return nil, err
		}
		if err := gw.Close(); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(gzbuf.Bytes())), nil
	})

	layer, err := tarball.LayerFromOpener(opener)
	if err != nil {
		t.Fatalf("tarball.LayerFromOpener: %v", err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("mutate.AppendLayers: %v", err)
	}
	return img
}

// TestEnsureBuilderImage_DigestCacheHit verifies that:
//  1. EnsureBuilderImage produces a non-empty .ext4 containing buildkitd.
//  2. A second call with the same digest is a digest-cache hit — no re-pull.
func TestEnsureBuilderImage_DigestCacheHit(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not available:", err)
	}

	dataDir := t.TempDir()

	const fakeDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resolveCount := 0
	pullCount := 0

	img := buildMinimalOCIImage(t)

	builderimage.SetResolveDigestForTest(func(_ context.Context, _ string) (string, error) {
		resolveCount++
		return fakeDigest, nil
	})
	builderimage.SetPullRemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCount++
		return img, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	ctx := context.Background()

	// First call: must resolve + pull + build ext4.
	path1, err := builderimage.EnsureBuilderImage(ctx, dataDir, fakeAgentBytes)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	info, err := os.Stat(path1)
	if err != nil {
		t.Fatalf("stat ext4: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("ext4 image is empty")
	}
	if !strings.HasSuffix(path1, ".ext4") {
		t.Errorf("expected .ext4 suffix, got %q", path1)
	}
	if resolveCount != 1 {
		t.Errorf("resolveCount = %d, want 1", resolveCount)
	}
	if pullCount != 1 {
		t.Errorf("pullCount = %d after first call, want 1", pullCount)
	}

	// Confirm buildkitd is present in the ext4.
	verifyExt4ContainsBuildkitd(t, path1)

	// Second call: same digest → cache hit; pull must not be called again.
	path2, err := builderimage.EnsureBuilderImage(ctx, dataDir, fakeAgentBytes)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if path2 != path1 {
		t.Errorf("cache hit returned different path: %q vs %q", path2, path1)
	}
	if pullCount != 1 {
		t.Errorf("pullCount = %d after cache hit, want 1 (no re-pull)", pullCount)
	}
}

// verifyExt4ContainsBuildkitd confirms buildkitd is present in the ext4 image
// via debugfs (preferred) or a raw-byte scan fallback.
func verifyExt4ContainsBuildkitd(t *testing.T, imagePath string) {
	t.Helper()

	if dbgPath, err := exec.LookPath("debugfs"); err == nil {
		out, err := exec.Command(dbgPath, "-R", "ls -l /usr/bin", imagePath).CombinedOutput()
		t.Logf("debugfs ls /usr/bin:\n%s", out)
		if err == nil && strings.Contains(string(out), "buildkitd") {
			return
		}
	}

	// Fallback: mke2fs inlines small file content near inode data.
	data, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read ext4 for fallback check: %v", err)
	}
	if !bytes.Contains(data, []byte("buildkitd")) {
		t.Error("ext4 image does not contain 'buildkitd'")
	} else {
		t.Log("fallback: 'buildkitd' found in raw ext4 bytes")
	}
}

// TestEnsureBuilderImage_EmptyAgentRejected verifies that nil agent bytes are
// rejected without network activity.
func TestEnsureBuilderImage_EmptyAgentRejected(t *testing.T) {
	_, err := builderimage.EnsureBuilderImage(context.Background(), t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected error for nil agentBytes, got nil")
	}
}

// TestEnsureBuilderImage_MissingMke2fs verifies ErrMke2fsUnavailable is
// surfaced when mke2fs is hidden from PATH.
func TestEnsureBuilderImage_MissingMke2fs(t *testing.T) {
	orig := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", orig) })
	os.Setenv("PATH", "/nonexistent")

	const fakeDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	img := buildMinimalOCIImage(t)
	builderimage.SetResolveDigestForTest(func(_ context.Context, _ string) (string, error) {
		return fakeDigest, nil
	})
	builderimage.SetPullRemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		return img, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	_, err := builderimage.EnsureBuilderImage(context.Background(), t.TempDir(), fakeAgentBytes)
	if !errors.Is(err, builderimage.ErrMke2fsUnavailable) {
		t.Errorf("expected ErrMke2fsUnavailable, got %v", err)
	}
}
