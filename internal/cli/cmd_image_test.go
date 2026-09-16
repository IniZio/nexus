package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── test helpers ──────────────────────────────────────────────────────────────

// newTestImageService builds an ImageService backed by a real Cache and a real
// (empty) sandbox FileStore in temp dirs, with an optional fake builder (may
// be nil for list/prune-only tests). The store is always wired so tests
// exercise the same store-wired code path as production newImageService.
func newTestImageService(t *testing.T, b service.ImageBuilder) (*service.ImageService, *image.Cache) {
	svc, c, _ := newTestImageServiceRoot(t, b)
	return svc, c
}

// newTestImageServiceRoot is newTestImageService that also returns the cache root dir.
func newTestImageServiceRoot(t *testing.T, b service.ImageBuilder) (*service.ImageService, *image.Cache, string) {
	t.Helper()
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	fs, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}
	svc := service.NewImageService(c, b)
	svc.WithStore(fs)
	return svc, c, root
}

// fakeDigest computes a sha256:<hex> digest of content, returning a valid
// domain.Digest and the content as a bytes.Reader for cache.Put.
func fakeDigest(content string) (domain.Digest, *bytes.Reader) {
	h := sha256.Sum256([]byte(content))
	d := domain.Digest("sha256:" + hex.EncodeToString(h[:]))
	return d, bytes.NewReader([]byte(content))
}

// seedCache writes one KindBase image record to cache c for testing. Returns the stored image.
// A KindBase entry is retained by prune only when its ref is in
// service.DefaultPinnedBaseRefs or a sandbox references its digest.
func seedCache(t *testing.T, c *image.Cache, content, ref string) domain.Image {
	t.Helper()
	d, r := fakeDigest(content)
	img := domain.Image{
		Digest: d,
		Ref:    ref,
		Kind:   domain.KindBase,
		Size:   int64(len(content)),
	}
	if err := c.Put(context.Background(), img, r); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}
	return img
}

// seedBuilderCache writes one KindBuilder image record to cache c for testing.
// Use this in prune tests where the image should be eligible for GC: any
// unreferenced entry is pruned regardless of kind, and these refs are never pinned.
func seedBuilderCache(t *testing.T, c *image.Cache, content, ref string) domain.Image {
	t.Helper()
	d, r := fakeDigest(content)
	img := domain.Image{
		Digest: d,
		Ref:    ref,
		Kind:   domain.KindBuilder,
		Size:   int64(len(content)),
	}
	if err := c.Put(context.Background(), img, r); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}
	return img
}

// fakeBuilder is an ImageBuilder that returns a pre-canned image.
type fakeBuilder struct {
	img domain.Image
	err error
}

func (f *fakeBuilder) Build(_ context.Context, _ builder.BuildRequest) (domain.Image, error) {
	return f.img, f.err
}

// ── image ls tests ────────────────────────────────────────────────────────────

func TestImageList_JSON_EmptyCache(t *testing.T) {
	svc, _ := newTestImageService(t, nil)
	out, stdout, _ := capture(true)

	if err := runImageWithService(context.Background(), []string{"ls"}, out, svc); err != nil {
		t.Fatalf("image ls (empty): %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["schema_version"] != float64(1) {
		t.Errorf("schema_version: got %v, want 1", env["schema_version"])
	}
	if env["kind"] != "image.list" {
		t.Errorf("kind: got %v, want image.list", env["kind"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data: expected object, got %T", env["data"])
	}
	imgs, ok := data["images"].([]any)
	if !ok {
		t.Fatalf("data.images: expected array, got %T", data["images"])
	}
	if len(imgs) != 0 {
		t.Errorf("expected 0 images in empty cache, got %d", len(imgs))
	}
}

func TestImageList_JSON_PopulatedCache(t *testing.T) {
	svc, c := newTestImageService(t, nil)
	stored := seedCache(t, c, "rootfs content alpha", "test:alpha")

	out, stdout, _ := capture(true)
	if err := runImageWithService(context.Background(), []string{"ls"}, out, svc); err != nil {
		t.Fatalf("image ls: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "image.list" {
		t.Errorf("kind: got %v, want image.list", env["kind"])
	}
	data := env["data"].(map[string]any)
	imgs := data["images"].([]any)
	if len(imgs) != 1 {
		t.Fatalf("expected 1 image, got %d", len(imgs))
	}
	imgMap := imgs[0].(map[string]any)
	if imgMap["digest"] != stored.Digest.String() {
		t.Errorf("digest: got %v, want %s", imgMap["digest"], stored.Digest)
	}
	if imgMap["ref"] != "test:alpha" {
		t.Errorf("ref: got %v, want test:alpha", imgMap["ref"])
	}
	if imgMap["kind"] != "base" {
		t.Errorf("kind: got %v, want base", imgMap["kind"])
	}
}

func TestImageList_Human_PopulatedCache(t *testing.T) {
	svc, c := newTestImageService(t, nil)
	seedCache(t, c, "rootfs content beta", "test:beta")

	out, stdout, _ := capture(false)
	if err := runImageWithService(context.Background(), []string{"ls"}, out, svc); err != nil {
		t.Fatalf("image ls (human): %v", err)
	}

	if !strings.Contains(stdout.String(), "1 image(s)") {
		t.Errorf("human output: got %q, want to contain '1 image(s)'", stdout.String())
	}
}

// ── image prune tests ─────────────────────────────────────────────────────────

func TestImagePrune_JSON_EmptyCache(t *testing.T) {
	svc, _ := newTestImageService(t, nil)
	out, stdout, _ := capture(true)

	if err := runImageWithService(context.Background(), []string{"prune"}, out, svc); err != nil {
		t.Fatalf("image prune (empty): %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "image.pruned" {
		t.Errorf("kind: got %v, want image.pruned", env["kind"])
	}
	data := env["data"].(map[string]any)
	if data["removed"] != float64(0) {
		t.Errorf("removed: got %v, want 0", data["removed"])
	}
}

func TestImagePrune_JSON_RemovesUnreferenced(t *testing.T) {
	svc, c := newTestImageService(t, nil)
	// Orphan builder artifacts accumulate from repeated --file builds; only
	// pinned (DefaultPinnedBaseRefs) or sandbox-referenced entries survive.
	seedBuilderCache(t, c, "rootfs content one", "test:one")
	seedBuilderCache(t, c, "rootfs content two", "test:two")

	// Verify both images are present before prune.
	imgs, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("pre-prune List: %v", err)
	}
	if len(imgs) != 2 {
		t.Fatalf("pre-prune: expected 2 images, got %d", len(imgs))
	}

	out, stdout, _ := capture(true)
	if err := runImageWithService(context.Background(), []string{"prune"}, out, svc); err != nil {
		t.Fatalf("image prune: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "image.pruned" {
		t.Errorf("kind: got %v, want image.pruned", env["kind"])
	}
	data := env["data"].(map[string]any)
	if data["removed"] != float64(2) {
		t.Errorf("removed: got %v, want 2", data["removed"])
	}

	// Verify cache is empty after prune.
	remaining, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("post-prune List: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("post-prune: expected 0 images, got %d", len(remaining))
	}
}

func TestImagePrune_Human_ReportsCount(t *testing.T) {
	svc, c := newTestImageService(t, nil)
	// KindBuilder orphan — unreferenced and unpinned, so eligible for GC.
	seedBuilderCache(t, c, "rootfs content gamma", "test:gamma")

	out, stdout, _ := capture(false)
	if err := runImageWithService(context.Background(), []string{"prune"}, out, svc); err != nil {
		t.Fatalf("image prune (human): %v", err)
	}

	if !strings.Contains(stdout.String(), "Pruned 1 image(s)") {
		t.Errorf("human output: got %q, want to contain 'Pruned 1 image(s)'", stdout.String())
	}
}

func TestImagePrune_JSON_ReportsTemplatesAndFreedBytes(t *testing.T) {
	svc, c, root := newTestImageServiceRoot(t, nil)
	seedBuilderCache(t, c, "rootfs content delta", "test:delta")
	seedStaleTemplate(t, root)
	svc.WithBuilderAgentTag("fedcba9876543210")

	out, stdout, _ := capture(true)
	if err := runImageWithService(context.Background(), []string{"prune"}, out, svc); err != nil {
		t.Fatalf("image prune: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	data := env["data"].(map[string]any)
	if data["removed"] != float64(1) {
		t.Errorf("removed: got %v, want 1", data["removed"])
	}
	if data["templates"] != float64(1) {
		t.Errorf("templates: got %v, want 1", data["templates"])
	}
	if fb, _ := data["freed_bytes"].(float64); fb <= 0 {
		t.Errorf("freed_bytes: got %v, want > 0", data["freed_bytes"])
	}
	if data["template_sweep"] != true {
		t.Errorf("template_sweep: got %v, want true", data["template_sweep"])
	}
	if data["dry_run"] != false {
		t.Errorf("dry_run: got %v, want false", data["dry_run"])
	}
}

func TestImagePrune_DryRun_JSON_ListsWithoutRemoving(t *testing.T) {
	svc, c, root := newTestImageServiceRoot(t, nil)
	stored := seedBuilderCache(t, c, "rootfs content epsilon", "test:epsilon")
	tpl := seedStaleTemplate(t, root)
	svc.WithBuilderAgentTag("fedcba9876543210")

	out, stdout, _ := capture(true)
	if err := runImageWithService(context.Background(), []string{"prune", "--dry-run"}, out, svc); err != nil {
		t.Fatalf("image prune --dry-run: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	data := env["data"].(map[string]any)
	if data["dry_run"] != true {
		t.Errorf("dry_run: got %v, want true", data["dry_run"])
	}
	imgs, _ := data["images"].([]any)
	if len(imgs) != 1 || imgs[0].(map[string]any)["digest"] != stored.Digest.String() {
		t.Errorf("images: got %v, want one entry with digest %s", data["images"], stored.Digest)
	}
	tpls, _ := data["builder_templates"].([]any)
	if len(tpls) != 1 || tpls[0].(map[string]any)["agent_tag"] != "0123456789abcdef" {
		t.Errorf("builder_templates: got %v, want one entry with agent_tag 0123456789abcdef", data["builder_templates"])
	}

	remaining, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("post-dry-run List: %v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("dry-run removed cache entries: %d remain, want 1", len(remaining))
	}
	if _, err := os.Stat(tpl); err != nil {
		t.Errorf("dry-run removed template: %v", err)
	}
}

func TestImagePrune_DryRun_Human_WouldFree(t *testing.T) {
	svc, c := newTestImageService(t, nil)
	seedBuilderCache(t, c, "rootfs content zeta", "test:zeta")

	out, stdout, _ := capture(false)
	if err := runImageWithService(context.Background(), []string{"prune", "--dry-run"}, out, svc); err != nil {
		t.Fatalf("image prune --dry-run (human): %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "Would free") {
		t.Errorf("human output: got %q, want to contain 'Would free'", got)
	}
	if !strings.Contains(got, "test:zeta") {
		t.Errorf("human output: got %q, want candidate table row for test:zeta", got)
	}
	if !strings.Contains(got, "builder-template sweep skipped") {
		t.Errorf("human output: got %q, want sweep-skipped notice when no agent tag is set", got)
	}
}

// seedStaleTemplate writes a nexus-builder-*.ext4 template with a foreign agent
// tag and an mtime outside BuilderTemplateInFlightGrace into the cache root.
func seedStaleTemplate(t *testing.T, root string) string {
	t.Helper()
	tpl := filepath.Join(root, "nexus-builder-abc-agent0123456789abcdef.ext4")
	if err := os.WriteFile(tpl, make([]byte, 64*1024), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	old := time.Now().Add(-2 * image.BuilderTemplateInFlightGrace)
	if err := os.Chtimes(tpl, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return tpl
}

// TestImagePrune_SandboxReferencedBuilderSurvives is the mutation-proven guard
// for the newImageService WithStore wiring (G3-manual-prune-safe).
//
// It seeds the image cache with two KindBuilder images, creates a sandbox
// record in a real FileStore whose ImageDigest points at one of them, wires
// the store into the ImageService, and runs the manual prune verb. Only the
// unreferenced orphan must be removed; the sandbox-referenced builder image
// must survive.
//
// Mutation proof: reverting svc.WithStore(fs) (or passing nil instead of fs)
// causes ReferencedDigests to keep only pinned base refs, so both builder
// images are deleted and the final assertion ("referenced image survived")
// fails.
func TestImagePrune_SandboxReferencedBuilderSurvives(t *testing.T) {
	ctx := context.Background()

	// Real sandbox store with one record referencing a builder image.
	fs, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	// Real image cache seeded with two KindBuilder images.
	c, err := image.NewCache(t.TempDir())
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	referencedImg := seedBuilderCache(t, c, "builder image referenced by sandbox", "builder:referenced")
	_ = seedBuilderCache(t, c, "builder image orphan", "builder:orphan")

	// Sandbox record whose ImageDigest points at the referenced builder image.
	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Project: "test",
		Name:    "prune-guard",
		State:   domain.Stopped,
		Envelope: domain.Envelope{
			ImageDigest: referencedImg.Digest.String(),
		},
	}
	if err := fs.Create(ctx, sb); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	// Wire the store — this is the path under test; mirrors newImageService.
	svc := service.NewImageService(c, nil)
	svc.WithStore(fs)

	out, stdout, _ := capture(true)
	if err := runImageWithService(ctx, []string{"prune"}, out, svc); err != nil {
		t.Fatalf("image prune: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	data := env["data"].(map[string]any)
	// Only the orphan image must be removed (removed == 1).
	// If removed == 2, the store was not wired and the referenced image was deleted.
	if data["removed"] != float64(1) {
		t.Errorf("removed: got %v, want 1 (only orphan; sandbox-referenced image must survive — WithStore not wired?)", data["removed"])
	}

	// Confirm the referenced builder image is still in the cache.
	remaining, listErr := c.List(ctx)
	if listErr != nil {
		t.Fatalf("post-prune List: %v", listErr)
	}
	for _, img := range remaining {
		if img.Digest == referencedImg.Digest {
			return // PASS
		}
	}
	t.Errorf("sandbox-referenced builder image %s was deleted by prune; check that svc.WithStore is called in newImageService", referencedImg.Digest)
}

// ── image build tests ─────────────────────────────────────────────────────────

func TestImageBuild_JSON_FakeBuilder(t *testing.T) {
	d, _ := fakeDigest("built rootfs content")
	fakeImg := domain.Image{
		Digest: d,
		Ref:    "nexus3-base:test",
		Kind:   domain.KindBase,
		Size:   42,
	}
	svc, _ := newTestImageService(t, &fakeBuilder{img: fakeImg})

	out, stdout, _ := capture(true)
	if err := runImageWithService(context.Background(),
		[]string{"build", "--base", "debian:bookworm-slim", "--workspace", t.TempDir(), "--ref", "nexus3-base:test"},
		out, svc); err != nil {
		t.Fatalf("image build: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["schema_version"] != float64(1) {
		t.Errorf("schema_version: got %v, want 1", env["schema_version"])
	}
	if env["kind"] != "image.built" {
		t.Errorf("kind: got %v, want image.built", env["kind"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data: expected object, got %T", env["data"])
	}
	if data["digest"] != d.String() {
		t.Errorf("digest: got %v, want %s", data["digest"], d)
	}
	if data["ref"] != "nexus3-base:test" {
		t.Errorf("ref: got %v, want nexus3-base:test", data["ref"])
	}
	if data["size"] != float64(42) {
		t.Errorf("size: got %v, want 42", data["size"])
	}
}

func TestImageBuild_JSON_NoBuilder(t *testing.T) {
	// Builder is nil: should emit an error, not panic.
	svc, _ := newTestImageService(t, nil)
	out, stdout, stderr := capture(true)

	if err := runImageWithService(context.Background(),
		[]string{"build", "--workspace", t.TempDir()},
		out, svc); err != nil {
		t.Fatalf("image build (no builder): unexpected non-nil return: %v", err)
	}

	// Should have written an error envelope to stdout (JSON mode).
	if stdout.Len() == 0 && stderr.Len() == 0 {
		t.Fatal("expected error output, got nothing")
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	if env["kind"] != "error" {
		t.Errorf("kind: got %v, want error", env["kind"])
	}
}

func TestImageBuild_Human_FakeBuilder(t *testing.T) {
	d, _ := fakeDigest("human build content")
	fakeImg := domain.Image{Digest: d, Size: 100, Kind: domain.KindBase}
	svc, _ := newTestImageService(t, &fakeBuilder{img: fakeImg})

	out, stdout, _ := capture(false)
	if err := runImageWithService(context.Background(),
		[]string{"build", "--workspace", t.TempDir()},
		out, svc); err != nil {
		t.Fatalf("image build (human): %v", err)
	}

	if !strings.Contains(stdout.String(), "built image") {
		t.Errorf("human output: got %q, want to contain 'built image'", stdout.String())
	}
}

// ── usage error tests ─────────────────────────────────────────────────────────

func TestImage_UsageError_MissingSubcommand(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"image"})
	if code != 2 {
		t.Errorf("missing subcommand: exit code = %d, want 2", code)
	}
}

func TestImage_UsageError_UnknownSubcommand(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"image", "frobnicate"})
	if code != 2 {
		t.Errorf("unknown subcommand: exit code = %d, want 2", code)
	}
}

// ── JSON schema shape ─────────────────────────────────────────────────────────

// TestImageList_JSON_SchemaShape verifies the exact JSON envelope fields.
func TestImageList_JSON_SchemaShape(t *testing.T) {
	svc, _ := newTestImageService(t, nil)
	out, stdout, _ := capture(true)

	if err := runImageWithService(context.Background(), []string{"ls"}, out, svc); err != nil {
		t.Fatalf("image ls: %v", err)
	}

	// Round-trip through the concrete struct to ensure every field is present
	// and correctly tagged.
	var env struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Data          struct {
			Images []json.RawMessage `json:"images"`
		} `json:"data"`
	}
	if err := json.NewDecoder(stdout).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: got %d, want 1", env.SchemaVersion)
	}
	if env.Kind != "image.list" {
		t.Errorf("kind: got %q, want image.list", env.Kind)
	}
}
