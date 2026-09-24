package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/image"
)

// fakePull is a helper that installs an ociPullAndCacheFn stub returning a
// content-addressed image whose content encodes the ref and agent bytes,
// so different agents produce different digests.
func withFakePull(t *testing.T) *int {
	t.Helper()
	old := ociPullAndCacheFn
	t.Cleanup(func() { ociPullAndCacheFn = old })
	pulls := new(int)
	ociPullAndCacheFn = func(ctx context.Context, r string, cc *image.Cache, agent []byte) (string, error) {
		*pulls++
		content := r + "+" + string(agent)
		sum := sha256.Sum256([]byte(content))
		d := domain.Digest("sha256:" + hex.EncodeToString(sum[:]))
		if err := cc.Put(ctx, domain.Image{
			Digest:    d,
			Ref:       r,
			Kind:      domain.KindBase,
			CreatedAt: time.Now(),
			AgentTag:  image.BuilderAgentTag(agent),
		}, strings.NewReader(content)); err != nil {
			return "", err
		}
		return string(d), nil
	}
	return pulls
}

// TestResolveExt4_ImageRefCacheHit_AgentChanged_Rebakes verifies that:
//   - agent A then agent B → 2 pulls and distinct digests
//   - same agent twice → 1 pull (genuine hit)
func TestResolveExt4_ImageRefCacheHit_AgentChanged_Rebakes(t *testing.T) {
	const ref = "debian:bookworm-slim"

	t.Run("different agents produce two pulls and distinct digests", func(t *testing.T) {
		dir := t.TempDir()
		c, err := image.NewCache(dir)
		if err != nil {
			t.Fatal(err)
		}
		pulls := withFakePull(t)

		_, dA, err := resolveExt4(context.Background(), ImageSpec{Ref: ref}, c, dir, []byte("AGENT-A"))
		if err != nil {
			t.Fatal(err)
		}
		_, dB, err := resolveExt4(context.Background(), ImageSpec{Ref: ref}, c, dir, []byte("AGENT-B"))
		if err != nil {
			t.Fatal(err)
		}
		if *pulls != 2 {
			t.Errorf("pulls = %d, want 2", *pulls)
		}
		if dA == dB {
			t.Errorf("agent changed but same digest returned: %s", dA)
		}
	})

	t.Run("same agent twice produces one pull", func(t *testing.T) {
		dir := t.TempDir()
		c, err := image.NewCache(dir)
		if err != nil {
			t.Fatal(err)
		}
		pulls := withFakePull(t)

		_, d1, err := resolveExt4(context.Background(), ImageSpec{Ref: ref}, c, dir, []byte("AGENT-A"))
		if err != nil {
			t.Fatal(err)
		}
		_, d2, err := resolveExt4(context.Background(), ImageSpec{Ref: ref}, c, dir, []byte("AGENT-A"))
		if err != nil {
			t.Fatal(err)
		}
		if *pulls != 1 {
			t.Errorf("pulls = %d, want 1 on same-agent hit", *pulls)
		}
		if d1 != d2 {
			t.Errorf("same agent should return same digest: %s vs %s", d1, d2)
		}
	})

	t.Run("legacy entry (no agent tag) counts as stale, triggers re-bake", func(t *testing.T) {
		dir := t.TempDir()
		c, err := image.NewCache(dir)
		if err != nil {
			t.Fatal(err)
		}

		// Seed a legacy entry: no AgentTag.
		content := "legacy-content"
		sum := sha256.Sum256([]byte(content))
		legacyDigest := domain.Digest("sha256:" + hex.EncodeToString(sum[:]))
		if err := c.Put(context.Background(), domain.Image{
			Digest:    legacyDigest,
			Ref:       ref,
			Kind:      domain.KindBase,
			CreatedAt: time.Now(),
			AgentTag:  "", // legacy: no tag
		}, strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}

		pulls := withFakePull(t)
		_, d, err := resolveExt4(context.Background(), ImageSpec{Ref: ref}, c, dir, []byte("AGENT-A"))
		if err != nil {
			t.Fatal(err)
		}
		if *pulls != 1 {
			t.Errorf("pulls = %d, want 1 (legacy entry should trigger re-bake)", *pulls)
		}
		if d == string(legacyDigest) {
			t.Error("legacy entry should not be served; expected fresh digest")
		}
	})

	t.Run("nil agentBytes leaves cache behaviour unchanged", func(t *testing.T) {
		dir := t.TempDir()
		c, err := image.NewCache(dir)
		if err != nil {
			t.Fatal(err)
		}

		// Seed a real entry so there is something to hit.
		pulls := withFakePull(t)
		_, _, err = resolveExt4(context.Background(), ImageSpec{Ref: ref}, c, dir, []byte("AGENT-A"))
		if err != nil {
			t.Fatal(err)
		}
		if *pulls != 1 {
			t.Fatalf("setup: expected 1 pull, got %d", *pulls)
		}

		// nil agentBytes: no agent-tag check; existing entry served as-is.
		_, _, err = resolveExt4(context.Background(), ImageSpec{Ref: ref}, c, dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		// No extra pull (nil agentBytes skips cache miss path for ErrAgentBytesRequired).
		if *pulls != 1 {
			t.Errorf("nil agentBytes: pulls = %d, want 1 (unchanged behaviour)", *pulls)
		}
	})
}
