package builder_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// ── BuildFingerprint ──────────────────────────────────────────────────────────

func TestBuildFingerprintDeterminism(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	cf := []byte("FROM ubuntu:22.04\nRUN echo hello\n")
	base := "ubuntu:22.04"
	agent := []byte("fake-agent-binary-bytes")

	fp1, err := builder.BuildFingerprint(cf, base, agent, dir, cred.ToolRecipe{}, "")
	if err != nil {
		t.Fatalf("BuildFingerprint: %v", err)
	}
	fp2, err := builder.BuildFingerprint(cf, base, agent, dir, cred.ToolRecipe{}, "")
	if err != nil {
		t.Fatalf("BuildFingerprint second call: %v", err)
	}
	if fp1 != fp2 {
		t.Errorf("not deterministic: got %q then %q", fp1, fp2)
	}
	if len(fp1) != 64 {
		t.Errorf("expected 64-char hex string, got len=%d: %q", len(fp1), fp1)
	}
}

func TestBuildFingerprintSensitivity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "src.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	cf := []byte("FROM ubuntu:22.04\nRUN echo hello\n")
	base := "ubuntu:22.04"
	agent := []byte("agent-v1")

	baseline, err := builder.BuildFingerprint(cf, base, agent, dir, cred.ToolRecipe{}, "x64")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("containerfile change", func(t *testing.T) {
		fp, err := builder.BuildFingerprint([]byte("FROM ubuntu:22.04\nRUN echo world\n"), base, agent, dir, cred.ToolRecipe{}, "x64")
		if err != nil {
			t.Fatal(err)
		}
		if fp == baseline {
			t.Error("changing containerfile should change fingerprint")
		}
	})

	t.Run("base ref change", func(t *testing.T) {
		fp, err := builder.BuildFingerprint(cf, "debian:12", agent, dir, cred.ToolRecipe{}, "x64")
		if err != nil {
			t.Fatal(err)
		}
		if fp == baseline {
			t.Error("changing base image ref should change fingerprint")
		}
	})

	t.Run("agent change", func(t *testing.T) {
		fp, err := builder.BuildFingerprint(cf, base, []byte("agent-v2"), dir, cred.ToolRecipe{}, "x64")
		if err != nil {
			t.Fatal(err)
		}
		if fp == baseline {
			t.Error("changing agent bytes should change fingerprint")
		}
	})

	t.Run("context file added", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("new"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(dir, "newfile.txt"))

		fp, err := builder.BuildFingerprint(cf, base, agent, dir, cred.ToolRecipe{}, "x64")
		if err != nil {
			t.Fatal(err)
		}
		if fp == baseline {
			t.Error("adding a context file should change fingerprint")
		}
	})

	t.Run("context file modified (mtime bump)", func(t *testing.T) {
		// Utime the existing file to simulate a touch without content change.
		p := filepath.Join(dir, "src.go")
		now := time.Now().Add(time.Second)
		if err := os.Chtimes(p, now, now); err != nil {
			t.Fatal(err)
		}
		fp, err := builder.BuildFingerprint(cf, base, agent, dir, cred.ToolRecipe{}, "x64")
		if err != nil {
			t.Fatal(err)
		}
		if fp == baseline {
			t.Error("touching (mtime bump) a context file should change fingerprint")
		}
	})

	t.Run("recipe change", func(t *testing.T) {
		r := cred.ToolRecipe{
			BinPath: "/usr/local/bin/sometool",
			Packages: []cred.RecipePackage{
				{Kind: cred.RecipeKindNPM, Name: "some-npm-pkg", Version: "1.0.0"},
			},
		}
		fp, err := builder.BuildFingerprint(cf, base, agent, dir, r, "x64")
		if err != nil {
			t.Fatal(err)
		}
		if fp == baseline {
			t.Error("changing recipe should change fingerprint")
		}
	})

	t.Run("arch change", func(t *testing.T) {
		fp, err := builder.BuildFingerprint(cf, base, agent, dir, cred.ToolRecipe{}, "arm64")
		if err != nil {
			t.Fatal(err)
		}
		if fp == baseline {
			t.Error("changing target arch should change fingerprint")
		}
	})
}

// TestBuildFingerprintRecipeSensitivity proves that two inputs differing ONLY by
// agent profile (ToolRecipe) produce different fingerprints, and that two
// identical inputs produce the same fingerprint on repeated calls. This is the
// primary regression test for the cache-collision bug where a claude-code
// sandbox and a cursor-agent sandbox built from the same workspace produced
// identical fingerprints because the recipe was absent from the hash.
func TestBuildFingerprintRecipeSensitivity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	cf := []byte("FROM debian:bookworm-slim\nRUN echo hi\n")
	base := "debian:bookworm-slim"
	agent := []byte("agent-binary-bytes")

	claudeRecipe := cred.ToolRecipe{
		BinPath: "/usr/local/bin/claude",
		Packages: []cred.RecipePackage{
			{
				Kind:        cred.RecipeKindTarball,
				Name:        "node",
				Version:     "22.23.2",
				URLTemplate: "https://nodejs.org/dist/v{VERSION}/node-v{VERSION}-linux-{ARCH}.tar.gz",
				SHA256ByArch: map[string]string{
					"x64": "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a",
				},
				InstallDir: "/usr/local",
			},
			{Kind: cred.RecipeKindNPM, Name: "@anthropic-ai/claude-code", Version: "2.1.226"},
		},
	}
	cursorRecipe := cred.ToolRecipe{
		BinPath: "/usr/local/bin/cursor-agent",
		Packages: []cred.RecipePackage{
			{
				Kind:        cred.RecipeKindTarball,
				Name:        "cursor-agent",
				Version:     "2026.08.25-3e8eec8",
				URLTemplate: "https://downloads.cursor.com/linux/{ARCH}/cursor-agent-{VERSION}.tar.gz",
				SHA256ByArch: map[string]string{
					"x64": "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
				},
				InstallDir: "/usr/local/share/cursor-agent/versions/{VERSION}",
				Symlinks: []cred.RecipeSymlink{
					{LinkPath: "/usr/local/bin/cursor-agent", TargetPath: "/usr/local/share/cursor-agent/versions/{VERSION}/agent-cli"},
				},
			},
		},
	}

	fpClaude, err := builder.BuildFingerprint(cf, base, agent, dir, claudeRecipe, "x64")
	if err != nil {
		t.Fatalf("BuildFingerprint (claude): %v", err)
	}
	fpCursor, err := builder.BuildFingerprint(cf, base, agent, dir, cursorRecipe, "x64")
	if err != nil {
		t.Fatalf("BuildFingerprint (cursor): %v", err)
	}

	// AC-1: two inputs differing ONLY by agent profile produce different fingerprints.
	if fpClaude == fpCursor {
		t.Errorf("claude and cursor recipes produced the SAME fingerprint %q — cache collision bug is present", fpClaude)
	}

	// AC-2: repeated calls with the same recipe produce the same fingerprint.
	// This proves map-key iteration order does not affect the result.
	fpClaude2, err := builder.BuildFingerprint(cf, base, agent, dir, claudeRecipe, "x64")
	if err != nil {
		t.Fatalf("BuildFingerprint (claude repeat): %v", err)
	}
	fpCursor2, err := builder.BuildFingerprint(cf, base, agent, dir, cursorRecipe, "x64")
	if err != nil {
		t.Fatalf("BuildFingerprint (cursor repeat): %v", err)
	}
	if fpClaude != fpClaude2 {
		t.Errorf("claude recipe is not deterministic: got %q then %q", fpClaude, fpClaude2)
	}
	if fpCursor != fpCursor2 {
		t.Errorf("cursor recipe is not deterministic: got %q then %q", fpCursor, fpCursor2)
	}
}

// ── ExtractFromRef ────────────────────────────────────────────────────────────

func TestExtractFromRef(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "FROM ubuntu:22.04\n", "ubuntu:22.04"},
		{"platform flag", "FROM --platform=linux/amd64 ubuntu:22.04\n", "ubuntu:22.04"},
		{"as alias", "FROM ubuntu:22.04 AS builder\n", "ubuntu:22.04"},
		{"digest ref", "FROM ubuntu@sha256:abc123\n", "ubuntu@sha256:abc123"},
		{"skip arg then from", "ARG VERSION=1\nFROM debian:stable\n", "debian:stable"},
		{"skip comment then from", "# comment\nFROM alpine:3\n", "alpine:3"},
		{"scratch", "FROM scratch\n", "scratch"},
		{"empty file", "", "scratch"},
		{"platform and alias", "FROM --platform=$BUILDPLATFORM golang:1.21 AS build\n", "golang:1.21"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := builder.ExtractFromRef([]byte(tc.input))
			if got != tc.want {
				t.Errorf("ExtractFromRef: got %q, want %q", got, tc.want)
			}
		})
	}
}

// ── LookupBuildCache / StoreBuildCache ────────────────────────────────────────

func makeFakeImageCache(t *testing.T, storeRoot string) (*image.Cache, string) {
	t.Helper()
	cacheRoot := filepath.Join(storeRoot, "images")
	imgCache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	// Put a fake image so LookupBuildCache can verify it exists.
	content := []byte("fake-ext4-rootfs-bytes-for-test")
	h := sha256.Sum256(content)
	digestStr := "sha256:" + hex.EncodeToString(h[:])
	d, err := domain.ParseDigest(digestStr)
	if err != nil {
		t.Fatalf("ParseDigest: %v", err)
	}
	img := domain.Image{
		Digest:    d,
		Ref:       "test:fingerprint-cache",
		Kind:      domain.KindBuilder,
		Size:      int64(len(content)),
		CreatedAt: time.Now().UTC(),
	}
	if err := imgCache.Put(context.Background(), img, bytes.NewReader(content)); err != nil {
		t.Fatalf("imgCache.Put: %v", err)
	}
	return imgCache, digestStr
}

func TestLookupBuildCacheMiss(t *testing.T) {
	storeRoot := t.TempDir()
	imgCache, _ := makeFakeImageCache(t, storeRoot)

	_, hit, err := builder.LookupBuildCache(context.Background(), storeRoot, "nonexistentfp000", imgCache)
	if err != nil {
		t.Fatalf("unexpected error on miss: %v", err)
	}
	if hit {
		t.Error("expected cache miss for unknown fingerprint")
	}
}

func TestStoreThenLookupHit(t *testing.T) {
	storeRoot := t.TempDir()
	imgCache, digestStr := makeFakeImageCache(t, storeRoot)

	fp := "aabbccddeeff0011"

	// Miss before storing.
	_, hit, err := builder.LookupBuildCache(context.Background(), storeRoot, fp, imgCache)
	if err != nil {
		t.Fatalf("pre-store lookup error: %v", err)
	}
	if hit {
		t.Error("expected miss before store")
	}

	// Store.
	if err := builder.StoreBuildCache(storeRoot, fp, digestStr); err != nil {
		t.Fatalf("StoreBuildCache: %v", err)
	}

	// Hit after storing.
	got, hit, err := builder.LookupBuildCache(context.Background(), storeRoot, fp, imgCache)
	if err != nil {
		t.Fatalf("post-store lookup error: %v", err)
	}
	if !hit {
		t.Error("expected cache hit after store")
	}
	if got != digestStr {
		t.Errorf("got digest %q, want %q", got, digestStr)
	}
}

func TestLookupBuildCacheEvictedImage(t *testing.T) {
	// If the image is gone from imgCache (pruned), LookupBuildCache should
	// return a miss rather than returning a stale digest path.
	storeRoot := t.TempDir()

	// Create an image cache that has NO images in it.
	emptyCache, err := image.NewCache(filepath.Join(storeRoot, "images"))
	if err != nil {
		t.Fatal(err)
	}

	fp := "deadbeefdeadbeef"
	digest := "sha256:" + hex.EncodeToString(sha256.New().Sum(nil)) // real hex, image absent

	// Manually write the digest file without putting the image in the cache.
	dir := filepath.Join(storeRoot, "build-cache", fp)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "digest"), []byte(digest+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	_, hit, err := builder.LookupBuildCache(context.Background(), storeRoot, fp, emptyCache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hit {
		t.Error("should be a miss when image is evicted from cache")
	}
}

func TestStoreBuildCacheIdempotent(t *testing.T) {
	storeRoot := t.TempDir()
	imgCache, digestStr := makeFakeImageCache(t, storeRoot)
	fp := "ffffffffffffffff"

	if err := builder.StoreBuildCache(storeRoot, fp, digestStr); err != nil {
		t.Fatal(err)
	}
	// Store again — should not error (atomic rename is idempotent).
	if err := builder.StoreBuildCache(storeRoot, fp, digestStr); err != nil {
		t.Fatalf("second StoreBuildCache: %v", err)
	}

	got, hit, err := builder.LookupBuildCache(context.Background(), storeRoot, fp, imgCache)
	if err != nil || !hit || got != digestStr {
		t.Errorf("after idempotent store: hit=%v err=%v got=%q", hit, err, got)
	}
}
