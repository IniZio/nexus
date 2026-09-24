//go:build linux

package builderimage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/IniZio/nexus/internal/core/builder/builderimage"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/image"
)

// TestPullAndCacheOCI_LegacyUntaggedEntry_Repulls verifies that a cache entry
// with no AgentTag (written before tag tracking) triggers a re-pull when
// agentBytes is non-empty — empty tag must not be treated as a hit.
func TestPullAndCacheOCI_LegacyUntaggedEntry_Repulls(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const ref = "alpine:3.20"

	// Seed a legacy entry: no AgentTag (pre-tag-tracking).
	content := []byte("legacy-ext4-content")
	h := sha256.Sum256(content)
	legacyDigest := domain.Digest("sha256:" + hex.EncodeToString(h[:]))
	legacyImg := domain.Image{
		Digest:    legacyDigest,
		Ref:       ref,
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC(),
		AgentTag:  "", // legacy: no tag
	}
	if err := c.Put(context.Background(), legacyImg, bytes.NewReader(content)); err != nil {
		t.Fatalf("pre-seed Put: %v", err)
	}

	// pullAmd64RemoteImage must be called; a minimal fake image is returned.
	pullCount := 0
	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCount++
		return buildMinimalOCIImage(t), nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	agent := []byte("nexus-agent-current")
	got, _ := builderimage.PullAndCacheOCI(context.Background(), ref, c, agent)

	if pullCount == 0 {
		t.Error("pullAmd64RemoteImage not called for legacy untagged entry — stale hit")
	}
	// New entry must carry the current agent tag.
	if got != "" {
		imgs, _ := c.List(context.Background())
		for _, img := range imgs {
			if string(img.Digest) == got {
				wantTag := image.BuilderAgentTag(agent)
				if img.AgentTag != wantTag {
					t.Errorf("new entry AgentTag = %q, want %q", img.AgentTag, wantTag)
				}
				break
			}
		}
	}
}

// TestPullAndCacheOCI_AgentTagMismatch_Repulls verifies that a cache entry
// carrying a different (non-empty) AgentTag causes a re-pull rather than
// returning the stale bake.
func TestPullAndCacheOCI_AgentTagMismatch_Repulls(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const ref = "alpine:3.20"

	// Seed a cache entry baked with agentA.
	agentA := []byte("nexus-agent-v1")
	content := []byte("fake-ext4-content-agent-a")
	h := sha256.Sum256(content)
	existingDigest := domain.Digest("sha256:" + hex.EncodeToString(h[:]))
	existingImg := domain.Image{
		Digest:    existingDigest,
		Ref:       ref,
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC(),
		AgentTag:  image.BuilderAgentTag(agentA),
	}
	if err := c.Put(context.Background(), existingImg, bytes.NewReader(content)); err != nil {
		t.Fatalf("pre-seed Put: %v", err)
	}

	// Provide agentB; pull stub returns a distinguishable image.
	agentB := []byte("nexus-agent-v2")
	pullCount := 0
	newContent := []byte("fake-ext4-content-agent-b")
	hNew := sha256.Sum256(newContent)
	newDigest := "sha256:" + hex.EncodeToString(hNew[:])

	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCount++
		return buildMinimalOCIImage(t), nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	// We can't easily intercept the ext4 build, so we test the decision boundary:
	// that pullAmd64RemoteImage IS called when agent tag mismatches.
	// (The actual bake requires mke2fs; skip the result check, just confirm pull.)
	_ = newDigest
	_, _ = builderimage.PullAndCacheOCI(context.Background(), ref, c, agentB)
	// Pull must have been invoked (mke2fs may or may not be present; error is fine).
	if pullCount == 0 {
		t.Error("pullAmd64RemoteImage not called despite agent tag mismatch — stale cache hit")
	}
}

// TestPullAndCacheOCI_AgentTagMatch_NoRepull verifies that a cache entry whose
// AgentTag matches the current agent is served without a re-pull.
func TestPullAndCacheOCI_AgentTagMatch_NoRepull(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const ref = "alpine:3.20"
	agent := []byte("nexus-agent-v1")

	content := []byte("fake-ext4-content")
	h := sha256.Sum256(content)
	existingDigest := domain.Digest("sha256:" + hex.EncodeToString(h[:]))
	existingImg := domain.Image{
		Digest:    existingDigest,
		Ref:       ref,
		Kind:      domain.KindBase,
		CreatedAt: time.Now().UTC(),
		AgentTag:  image.BuilderAgentTag(agent),
	}
	if err := c.Put(context.Background(), existingImg, bytes.NewReader(content)); err != nil {
		t.Fatalf("pre-seed Put: %v", err)
	}

	pullCalled := false
	builderimage.SetPullAmd64RemoteImageForTest(func(_ context.Context, _ string) (v1.Image, error) {
		pullCalled = true
		t.Error("pullAmd64RemoteImage called despite matching agent tag")
		return nil, nil
	})
	t.Cleanup(builderimage.ResetTestOverrides)

	got, err := builderimage.PullAndCacheOCI(context.Background(), ref, c, agent)
	if err != nil {
		t.Fatalf("PullAndCacheOCI: %v", err)
	}
	if pullCalled {
		t.Error("pull invoked despite matching agent tag")
	}
	if got != string(existingDigest) {
		t.Errorf("digest = %q, want %q", got, existingDigest)
	}
}
