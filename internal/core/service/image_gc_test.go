package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/service"
)

// ── helpers ────────────────────────────────────────────────────────────────────

func digestOf(content []byte) domain.Digest {
	h := sha256.Sum256(content)
	return domain.MustDigest("sha256:" + hex.EncodeToString(h[:]))
}

func newCache(t *testing.T) *image.Cache {
	t.Helper()
	c, err := image.NewCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return c
}

func putImage(t *testing.T, c *image.Cache, content []byte, kind domain.ImageKind) domain.Digest {
	t.Helper()
	d := digestOf(content)
	img := domain.Image{
		Digest:    d,
		Kind:      kind,
		Size:      int64(len(content)),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := c.Put(context.Background(), img, bytes.NewReader(content)); err != nil {
		t.Fatalf("Put(%s): %v", d, err)
	}
	return d
}

type fakeSandboxImageLister struct {
	sandboxes []domain.Sandbox
}

func (f *fakeSandboxImageLister) List(_ context.Context) ([]domain.Sandbox, error) {
	return f.sandboxes, nil
}

func sandboxWith(digest string) domain.Sandbox {
	return domain.Sandbox{Envelope: domain.Envelope{ImageDigest: digest}}
}

// ── ReferencedDigests tests ────────────────────────────────────────────────────

// TestReferencedDigests_BaseImageAlwaysKept: KindBase is always in the referenced
// set even with no sandbox references and no extra digests.
// Mutation proof: removing the KindBase loop makes this test fail.
func TestReferencedDigests_BaseImageAlwaysKept(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)
	baseDigest := putImage(t, c, []byte("base-content"), domain.KindBase)

	ref, err := service.ReferencedDigests(ctx, c, nil)
	if err != nil {
		t.Fatalf("ReferencedDigests: %v", err)
	}
	for _, d := range ref {
		if d == baseDigest {
			return
		}
	}
	t.Errorf("KindBase digest %s not in referenced set %v", baseDigest, ref)
}

// TestReferencedDigests_SandboxRefKept: a KindBuilder image referenced by a
// sandbox record is in the referenced set.
// Mutation proof: removing the sandbox-ref loop makes this test fail.
func TestReferencedDigests_SandboxRefKept(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)
	builderDigest := putImage(t, c, []byte("builder-content"), domain.KindBuilder)

	store := &fakeSandboxImageLister{
		sandboxes: []domain.Sandbox{sandboxWith(builderDigest.String())},
	}
	ref, err := service.ReferencedDigests(ctx, c, store)
	if err != nil {
		t.Fatalf("ReferencedDigests: %v", err)
	}
	for _, d := range ref {
		if d == builderDigest {
			return
		}
	}
	t.Errorf("sandbox-referenced digest %s not in referenced set %v", builderDigest, ref)
}

// TestReferencedDigests_UnreferencedBuilderNotKept: a KindBuilder image with no
// sandbox references and not in extra is NOT in the referenced set.
func TestReferencedDigests_UnreferencedBuilderNotKept(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)
	orphanDigest := putImage(t, c, []byte("orphan-builder"), domain.KindBuilder)

	store := &fakeSandboxImageLister{sandboxes: nil}
	ref, err := service.ReferencedDigests(ctx, c, store)
	if err != nil {
		t.Fatalf("ReferencedDigests: %v", err)
	}
	for _, d := range ref {
		if d == orphanDigest {
			t.Errorf("unreferenced builder digest %s should NOT be in referenced set", orphanDigest)
		}
	}
}

// TestReferencedDigests_ExtraDigestKept: a digest passed in extra is always in
// the referenced set regardless of kind or sandbox references.
// Mutation proof: removing the extra-digest loop makes this test fail.
func TestReferencedDigests_ExtraDigestKept(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)
	extraDigest := putImage(t, c, []byte("extra-content"), domain.KindBuilder)

	ref, err := service.ReferencedDigests(ctx, c, nil, extraDigest)
	if err != nil {
		t.Fatalf("ReferencedDigests: %v", err)
	}
	for _, d := range ref {
		if d == extraDigest {
			return
		}
	}
	t.Errorf("extra digest %s not in referenced set %v", extraDigest, ref)
}

// ── BuildPreflight tests ───────────────────────────────────────────────────────

// TestBuildPreflight_EnoughSpace: when free space >= floor, BuildPreflight
// returns nil without calling Prune.
func TestBuildPreflight_EnoughSpace(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)
	// Put an orphan that would be pruned if Prune were called.
	putImage(t, c, []byte("would-be-pruned"), domain.KindBuilder)

	restore := service.SetFreeSpaceFuncForTest(func(string) (uint64, error) {
		return 20 << 30, nil // 20 GiB — above 15 GiB floor
	})
	defer restore()

	if err := service.BuildPreflight(ctx, t.TempDir(), 15<<30, c, nil); err != nil {
		t.Errorf("BuildPreflight with enough space: want nil, got %v", err)
	}

	// Verify the orphan is still there (Prune was not called).
	imgs, _ := c.List(ctx)
	if len(imgs) != 1 {
		t.Errorf("expected 1 image after skipped prune, got %d", len(imgs))
	}
}

// TestBuildPreflight_LowSpacePrunesAndSucceeds: when free space < floor but
// rises above floor after prune, BuildPreflight returns nil.
func TestBuildPreflight_LowSpacePrunesAndSucceeds(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)
	// Orphan builder — will be pruned.
	putImage(t, c, []byte("orphan"), domain.KindBuilder)

	calls := 0
	restore := service.SetFreeSpaceFuncForTest(func(string) (uint64, error) {
		calls++
		if calls == 1 {
			return 5 << 30, nil // 5 GiB — below floor
		}
		return 20 << 30, nil // 20 GiB — above floor after prune
	})
	defer restore()

	if err := service.BuildPreflight(ctx, t.TempDir(), 15<<30, c, nil); err != nil {
		t.Errorf("BuildPreflight after prune: want nil, got %v", err)
	}
	// Orphan should be gone.
	imgs, _ := c.List(ctx)
	if len(imgs) != 0 {
		t.Errorf("expected 0 images after prune, got %d", len(imgs))
	}
}

// TestBuildPreflight_StillShortAfterPrune: when free space remains below the
// floor after prune, BuildPreflight returns a non-nil error containing "still short".
func TestBuildPreflight_StillShortAfterPrune(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	restore := service.SetFreeSpaceFuncForTest(func(string) (uint64, error) {
		return 1 << 30, nil // always 1 GiB — always below floor
	})
	defer restore()

	err := service.BuildPreflight(ctx, t.TempDir(), 15<<30, c, nil)
	if err == nil {
		t.Fatal("BuildPreflight: expected error when still short after prune, got nil")
	}
	if !strings.Contains(err.Error(), "still short") {
		t.Errorf("error message %q does not contain 'still short'", err.Error())
	}
}

// ── AutoPruneAfterBuild tests ─────────────────────────────────────────────────

// TestAutoPruneAfterBuild_ExtraProtected: the extra digest (the just-built
// image) is never pruned even when it is the only KindBuilder image.
// Mutation proof: removing the extra-digest loop in ReferencedDigests makes this fail.
func TestAutoPruneAfterBuild_ExtraProtected(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)
	builtDigest := putImage(t, c, []byte("just-built"), domain.KindBuilder)

	n, err := service.AutoPruneAfterBuild(ctx, c, nil, 0, builtDigest)
	if err != nil {
		t.Fatalf("AutoPruneAfterBuild: %v", err)
	}
	if n != 0 {
		t.Errorf("pruned %d entries, want 0 (extra image must be protected)", n)
	}
	imgs, _ := c.List(ctx)
	if len(imgs) != 1 {
		t.Errorf("expected 1 image, got %d", len(imgs))
	}
}

// TestAutoPruneAfterBuild_ReferencedSandboxImageNotPruned: a KindBuilder image
// referenced by a sandbox is not pruned by AutoPruneAfterBuild.
// THIS IS THE KEY SAFETY TEST — a referenced image must never be removed.
func TestAutoPruneAfterBuild_ReferencedSandboxImageNotPruned(t *testing.T) {
	ctx := context.Background()
	c := newCache(t)

	// Image referenced by a running sandbox.
	sandboxDigest := putImage(t, c, []byte("sandbox-image"), domain.KindBuilder)
	// Orphan that should be pruned.
	putImage(t, c, []byte("orphan-image"), domain.KindBuilder)

	store := &fakeSandboxImageLister{
		sandboxes: []domain.Sandbox{sandboxWith(sandboxDigest.String())},
	}

	n, err := service.AutoPruneAfterBuild(ctx, c, store, 0)
	if err != nil {
		t.Fatalf("AutoPruneAfterBuild: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d entries, want 1 (only the orphan)", n)
	}

	// Sandbox-referenced image must still be present.
	imgs, _ := c.List(ctx)
	for _, img := range imgs {
		if img.Digest == sandboxDigest {
			return
		}
	}
	t.Errorf("sandbox-referenced image %s was pruned — safety violation", sandboxDigest)
}
