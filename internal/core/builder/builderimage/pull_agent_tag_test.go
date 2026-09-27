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

func TestPullAndCacheOCI_LegacyUntaggedEntry_Repulls(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const ref = "alpine:3.20"

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

func TestPullAndCacheOCI_AgentTagMismatch_Repulls(t *testing.T) {
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const ref = "alpine:3.20"

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

	// Only confirm pull was invoked; mke2fs may not be present so result is untested.
	_ = newDigest
	_, _ = builderimage.PullAndCacheOCI(context.Background(), ref, c, agentB)
	if pullCount == 0 {
		t.Error("pullAmd64RemoteImage not called despite agent tag mismatch — stale cache hit")
	}
}

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
