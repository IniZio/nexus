//go:build linux

package builderimage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/IniZio/nexus/internal/core/builder/builderimage"
	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/image"
)

// --- CacheTag tests ---

func TestCacheTag_NilTools_EqualsBuilderAgentTag(t *testing.T) {
	agent := []byte("nexus-agent-v1")
	want := image.BuilderAgentTag(agent)
	got := builderimage.CacheTag(agent, nil)
	if got != want {
		t.Errorf("CacheTag(nil tools) = %q, want %q", got, want)
	}
}

func TestCacheTag_EmptyTools_EqualsBuilderAgentTag(t *testing.T) {
	agent := []byte("nexus-agent-v1")
	want := image.BuilderAgentTag(agent)
	got := builderimage.CacheTag(agent, []toolcache.Fetched{})
	if got != want {
		t.Errorf("CacheTag(empty tools) = %q, want %q", got, want)
	}
}

func TestCacheTag_WithTools_DiffersFromBase(t *testing.T) {
	agent := []byte("nexus-agent-v1")
	tools := []toolcache.Fetched{{Name: "gh", Version: "2.60.0"}}
	base := builderimage.CacheTag(agent, nil)
	withTools := builderimage.CacheTag(agent, tools)
	if withTools == base {
		t.Errorf("CacheTag with tools must differ from base, both = %q", base)
	}
}

func TestCacheTag_WithTools_Stable(t *testing.T) {
	agent := []byte("nexus-agent-v1")
	tools := []toolcache.Fetched{{Name: "gh", Version: "2.60.0", SHA256: "abc123"}}
	a := builderimage.CacheTag(agent, tools)
	b := builderimage.CacheTag(agent, tools)
	if a != b {
		t.Errorf("CacheTag not stable: %q != %q", a, b)
	}
}

func TestCacheTag_DifferentTools_DifferentTag(t *testing.T) {
	agent := []byte("nexus-agent-v1")
	toolsA := []toolcache.Fetched{{Name: "gh", Version: "2.60.0"}}
	toolsB := []toolcache.Fetched{{Name: "gh", Version: "2.61.0"}}
	tagA := builderimage.CacheTag(agent, toolsA)
	tagB := builderimage.CacheTag(agent, toolsB)
	if tagA == tagB {
		t.Errorf("different tool versions must yield different tags, both = %q", tagA)
	}
}

// --- injectSandboxTools / StageTree tests ---

// TestInjectSandboxTools_PlacesBinaryAndSymlink verifies that when no gh
// exists in the staging dir, injectSandboxTools places the binary at
// GuestBinPath and creates a symlink at LinkPath.
func TestInjectSandboxTools_PlacesBinaryAndSymlink(t *testing.T) {
	stagingDir := t.TempDir()

	// Create a fake gh binary on host.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho gh-stub\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}

	tools := []toolcache.Fetched{{
		Name:         "gh",
		Version:      "2.101.0",
		BinPath:      binPath,
		GuestBinPath: "/usr/local/share/nexus-tools/gh/2.101.0/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}}

	if err := builderimage.InjectSandboxToolsForTest(stagingDir, tools); err != nil {
		t.Fatalf("InjectSandboxToolsForTest: %v", err)
	}

	// Binary must be placed at GuestBinPath.
	binGuestPath := filepath.Join(stagingDir, "usr/local/share/nexus-tools/gh/2.101.0/bin/gh")
	if _, err := os.Lstat(binGuestPath); err != nil {
		t.Fatalf("binary not found at GuestBinPath: %v", err)
	}

	// Symlink must be created at LinkPath.
	linkGuestPath := filepath.Join(stagingDir, "usr/local/bin/gh")
	info, err := os.Lstat(linkGuestPath)
	if err != nil {
		t.Fatalf("symlink not found at LinkPath: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("LinkPath entry is not a symlink: mode=%v", info.Mode())
	}
}

// TestInjectSandboxTools_SkipIfPresent verifies that if /usr/bin/gh already
// exists in the staging dir (image ships its own gh), injectSandboxTools does
// not place the nexus-managed binary or symlink.  This guards against
// pull.go:injectSandboxTools inadvertently passing skipIfPresent=false to
// toolcache.StageTree, which would clobber an image-supplied gh.
func TestInjectSandboxTools_SkipIfPresent(t *testing.T) {
	stagingDir := t.TempDir()

	// Pre-place /usr/bin/gh to simulate the --image sandbox already ships gh.
	existingDir := filepath.Join(stagingDir, "usr", "bin")
	if err := os.MkdirAll(existingDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existingGh := filepath.Join(existingDir, "gh")
	originalContent := []byte("original-gh-from-image")
	if err := os.WriteFile(existingGh, originalContent, 0o755); err != nil {
		t.Fatalf("write existing gh: %v", err)
	}

	// Nexus-managed binary that should NOT be injected.
	binDir := t.TempDir()
	newBin := filepath.Join(binDir, "gh")
	if err := os.WriteFile(newBin, []byte("new-gh-from-nexus"), 0o755); err != nil {
		t.Fatalf("write new gh: %v", err)
	}

	tools := []toolcache.Fetched{{
		Name:         "gh",
		Version:      "2.101.0",
		BinPath:      newBin,
		GuestBinPath: "/usr/local/share/nexus-tools/gh/2.101.0/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}}

	if err := builderimage.InjectSandboxToolsForTest(stagingDir, tools); err != nil {
		t.Fatalf("InjectSandboxToolsForTest: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(stagingDir, "usr/local/bin/gh")); !os.IsNotExist(err) {
		t.Errorf("usr/local/bin/gh must not be created when image already ships gh: lstat err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(stagingDir, "usr/local/share/nexus-tools")); !os.IsNotExist(err) {
		t.Errorf("usr/local/share/nexus-tools must not be created when image already ships gh: lstat err=%v", err)
	}
	got, err := os.ReadFile(existingGh)
	if err != nil {
		t.Fatalf("read pre-placed gh: %v", err)
	}
	if !bytes.Equal(got, originalContent) {
		t.Errorf("image gh was modified: got %q, want %q", got, originalContent)
	}
}

// --- PullAndCacheOCIWithTools end-to-end (offline seam) ---

// TestPullAndCacheOCIWithTools_CacheHitSameTools checks that a second call
// with the same tools returns a cache hit (pull not called again).
func TestPullAndCacheOCIWithTools_CacheHitSameTools(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const ref = "alpine:3.20"
	agent := []byte("nexus-agent-v1")
	tools := []toolcache.Fetched{{Name: "gh", Version: "2.60.0"}}
	tag := builderimage.CacheTag(agent, tools)

	// Pre-seed cache with a matching entry.
	content := []byte("fake-ext4-with-tools")
	h := sha256.Sum256(content)
	existingDigest := domain.Digest("sha256:" + hex.EncodeToString(h[:]))
	seeded := domain.Image{
		Digest:    existingDigest,
		Ref:       ref,
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC(),
		AgentTag:  tag,
	}
	if err := c.Put(context.Background(), seeded, bytes.NewReader(content)); err != nil {
		t.Fatalf("pre-seed Put: %v", err)
	}

	pullCalled := false
	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCalled = true
		return nil, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	got, err := builderimage.PullAndCacheOCIWithTools(context.Background(), ref, c, agent, tools)
	if err != nil {
		t.Fatalf("PullAndCacheOCIWithTools: %v", err)
	}
	if pullCalled {
		t.Error("pull invoked despite matching cache tag")
	}
	if got != string(existingDigest) {
		t.Errorf("digest = %q, want %q", got, existingDigest)
	}
}

// TestPullAndCacheOCIWithTools_RebakeWhenToolsChange verifies that changing
// the tool set forces a re-bake (pull is invoked).
func TestPullAndCacheOCIWithTools_RebakeWhenToolsChange(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const ref = "alpine:3.20"
	agent := []byte("nexus-agent-v1")
	toolsV1 := []toolcache.Fetched{{Name: "gh", Version: "2.60.0"}}
	tagV1 := builderimage.CacheTag(agent, toolsV1)

	// Pre-seed cache with tools-v1 tag.
	content := []byte("fake-ext4-tools-v1")
	h := sha256.Sum256(content)
	existingDigest := domain.Digest("sha256:" + hex.EncodeToString(h[:]))
	seeded := domain.Image{
		Digest:    existingDigest,
		Ref:       ref,
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC(),
		AgentTag:  tagV1,
	}
	if err := c.Put(context.Background(), seeded, bytes.NewReader(content)); err != nil {
		t.Fatalf("pre-seed Put: %v", err)
	}

	pullCalled := 0
	toolsV2 := []toolcache.Fetched{{Name: "gh", Version: "2.61.0"}}
	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCalled++
		return buildMinimalOCIImage(t), nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	_, _ = builderimage.PullAndCacheOCIWithTools(context.Background(), ref, c, agent, toolsV2)
	if pullCalled == 0 {
		t.Error("pull not invoked despite tool version change")
	}
}

// TestPullAndCacheOCIWithTools_NilToolsDelegatesToPullAndCacheOCI checks that
// nil tools yields the same cache tag as PullAndCacheOCI (backward compat).
func TestPullAndCacheOCIWithTools_NilToolsDelegatesToPullAndCacheOCI(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const ref = "alpine:3.20"
	agent := []byte("nexus-agent-v1")

	// Pre-seed with the plain BuilderAgentTag (as PullAndCacheOCI would store it).
	content := []byte("fake-ext4-no-tools")
	h := sha256.Sum256(content)
	existingDigest := domain.Digest("sha256:" + hex.EncodeToString(h[:]))
	seeded := domain.Image{
		Digest:    existingDigest,
		Ref:       ref,
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC(),
		AgentTag:  image.BuilderAgentTag(agent),
	}
	if err := c.Put(context.Background(), seeded, bytes.NewReader(content)); err != nil {
		t.Fatalf("pre-seed Put: %v", err)
	}

	pullCalled := false
	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCalled = true
		return nil, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	got, err := builderimage.PullAndCacheOCIWithTools(context.Background(), ref, c, agent, nil)
	if err != nil {
		t.Fatalf("PullAndCacheOCIWithTools(nil tools): %v", err)
	}
	if pullCalled {
		t.Error("pull invoked — nil-tools cache tag must match plain BuilderAgentTag")
	}
	if got != string(existingDigest) {
		t.Errorf("digest = %q, want %q", got, existingDigest)
	}
}
