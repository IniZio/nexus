package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/builder/builderimage"
	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/image"
)

// withFakePullTools installs stubs for both pull function vars and returns
// counters for each. t.Cleanup restores both on test exit.
func withFakePullTools(t *testing.T) (oldPulls, toolPulls *int) {
	t.Helper()
	oldPulls = new(int)
	toolPulls = new(int)

	oldFn := ociPullAndCacheFn
	toolsFn := ociPullAndCacheToolsFn
	t.Cleanup(func() {
		ociPullAndCacheFn = oldFn
		ociPullAndCacheToolsFn = toolsFn
	})

	ociPullAndCacheFn = func(ctx context.Context, r string, cc *image.Cache, agent []byte) (string, error) {
		*oldPulls++
		content := r + "+" + string(agent)
		sum := sha256.Sum256([]byte(content))
		d := domain.Digest("sha256:" + hex.EncodeToString(sum[:]))
		if err := cc.Put(ctx, domain.Image{
			Digest:    d,
			Ref:       r,
			Kind:      domain.KindBase,
			CreatedAt: time.Now(),
			AgentTag:  builderimage.CacheTag(agent, nil),
		}, strings.NewReader(content)); err != nil {
			return "", err
		}
		return string(d), nil
	}

	ociPullAndCacheToolsFn = func(ctx context.Context, r string, cc *image.Cache, agent []byte, tools []toolcache.Fetched) (string, error) {
		*toolPulls++
		// Content encodes ref + agent + first tool name so different tool sets
		// produce distinct digests.
		toolKey := ""
		if len(tools) > 0 {
			toolKey = tools[0].Name
		}
		content := r + "+" + string(agent) + "+" + toolKey
		sum := sha256.Sum256([]byte(content))
		d := domain.Digest("sha256:" + hex.EncodeToString(sum[:]))
		if err := cc.Put(ctx, domain.Image{
			Digest:    d,
			Ref:       r,
			Kind:      domain.KindBase,
			CreatedAt: time.Now(),
			AgentTag:  builderimage.CacheTag(agent, tools),
		}, strings.NewReader(content)); err != nil {
			return "", err
		}
		return string(d), nil
	}

	return oldPulls, toolPulls
}

func makeCache(t *testing.T) (*image.Cache, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := image.NewCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, dir
}

const toolsRef = "debian:bookworm-slim"

var agentA = []byte("AGENT-A")

var ghTool = toolcache.Fetched{Name: "gh", Version: "2.0.0"}

// TestResolveExt4WithTools_NoTools_UsesOldFn verifies that an empty tools
// slice routes through the original ociPullAndCacheFn, not the tools variant.
func TestResolveExt4WithTools_NoTools_UsesOldFn(t *testing.T) {
	c, dir := makeCache(t)
	oldPulls, toolPulls := withFakePullTools(t)

	_, _, err := resolveExt4WithTools(context.Background(), ImageSpec{Ref: toolsRef}, c, dir, agentA, nil)
	if err != nil {
		t.Fatal(err)
	}

	if *oldPulls != 1 {
		t.Errorf("oldPulls = %d, want 1", *oldPulls)
	}
	if *toolPulls != 0 {
		t.Errorf("toolPulls = %d, want 0", *toolPulls)
	}
}

// TestResolveExt4WithTools_WithTools_UsesToolsFn verifies that a non-empty
// tools slice routes through ociPullAndCacheToolsFn and the old fn is not called.
func TestResolveExt4WithTools_WithTools_UsesToolsFn(t *testing.T) {
	c, dir := makeCache(t)
	oldPulls, toolPulls := withFakePullTools(t)

	_, _, err := resolveExt4WithTools(context.Background(), ImageSpec{Ref: toolsRef}, c, dir, agentA, []toolcache.Fetched{ghTool})
	if err != nil {
		t.Fatal(err)
	}

	if *oldPulls != 0 {
		t.Errorf("oldPulls = %d, want 0 (tools path must not call old fn)", *oldPulls)
	}
	if *toolPulls != 1 {
		t.Errorf("toolPulls = %d, want 1", *toolPulls)
	}
}

// TestResolveExt4WithTools_CachedNoTools_RebakesWhenToolsRequested verifies that
// an image whose AgentTag == CacheTag(agent, nil) (i.e. cached without tools) is
// treated as stale when tools are requested, triggering a re-bake via ociPullAndCacheToolsFn.
func TestResolveExt4WithTools_CachedNoTools_RebakesWhenToolsRequested(t *testing.T) {
	c, dir := makeCache(t)

	// Seed an image cached without tools (CacheTag with nil tools).
	noToolsTag := builderimage.CacheTag(agentA, nil)
	content := "no-tools-content"
	sum := sha256.Sum256([]byte(content))
	noToolsDigest := domain.Digest("sha256:" + hex.EncodeToString(sum[:]))
	if err := c.Put(context.Background(), domain.Image{
		Digest:    noToolsDigest,
		Ref:       toolsRef,
		Kind:      domain.KindBase,
		CreatedAt: time.Now(),
		AgentTag:  noToolsTag,
	}, strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}

	oldPulls, toolPulls := withFakePullTools(t)

	_, digest, err := resolveExt4WithTools(context.Background(), ImageSpec{Ref: toolsRef}, c, dir, agentA, []toolcache.Fetched{ghTool})
	if err != nil {
		t.Fatal(err)
	}

	if *toolPulls != 1 {
		t.Errorf("toolPulls = %d, want 1 (image without tools should trigger re-bake)", *toolPulls)
	}
	if *oldPulls != 0 {
		t.Errorf("oldPulls = %d, want 0", *oldPulls)
	}
	if digest == string(noToolsDigest) {
		t.Error("expected fresh digest after tools re-bake, got the old no-tools digest")
	}
}

// TestResolveExt4WithTools_CachedWithTools_HitsWithoutPull verifies that an
// image whose AgentTag == CacheTag(agent, tools) is served from cache without
// any pull call.
func TestResolveExt4WithTools_CachedWithTools_HitsWithoutPull(t *testing.T) {
	c, dir := makeCache(t)

	tools := []toolcache.Fetched{ghTool}

	// Seed an image cached with tools.
	withToolsTag := builderimage.CacheTag(agentA, tools)
	content := "with-tools-content"
	sum := sha256.Sum256([]byte(content))
	withToolsDigest := domain.Digest("sha256:" + hex.EncodeToString(sum[:]))
	if err := c.Put(context.Background(), domain.Image{
		Digest:    withToolsDigest,
		Ref:       toolsRef,
		Kind:      domain.KindBase,
		CreatedAt: time.Now(),
		AgentTag:  withToolsTag,
	}, strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}

	oldPulls, toolPulls := withFakePullTools(t)

	_, digest, err := resolveExt4WithTools(context.Background(), ImageSpec{Ref: toolsRef}, c, dir, agentA, tools)
	if err != nil {
		t.Fatal(err)
	}

	if *oldPulls != 0 || *toolPulls != 0 {
		t.Errorf("unexpected pull: oldPulls=%d toolPulls=%d, want 0+0 (cache hit)", *oldPulls, *toolPulls)
	}
	if digest != string(withToolsDigest) {
		t.Errorf("digest = %q, want %q", digest, string(withToolsDigest))
	}
}
