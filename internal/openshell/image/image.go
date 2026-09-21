package image

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus/internal/openshell"
)

type ociExtractor func(ctx context.Context, ref string, destDir string) (digest string, err error)

type Option func(*Builder)

func WithMkfsPath(p string) Option {
	return func(b *Builder) { b.mkfsPath = p }
}

func withExtractor(e ociExtractor) Option {
	return func(b *Builder) { b.extractor = e }
}

func WithOverlaySizeMiB(n int64) Option {
	return func(b *Builder) { b.overlaySizeMiB = n }
}

type Builder struct {
	cacheDir       string
	mkfsPath       string
	extractor      ociExtractor
	overlaySizeMiB int64
}

var _ openshell.ImageBuilder = (*Builder)(nil)

func New(cacheDir string, opts ...Option) *Builder {
	b := &Builder{
		cacheDir:       cacheDir,
		extractor:      defaultExtractor,
		overlaySizeMiB: overlayDefaultSizeMiB,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

func (b *Builder) refMarkerPath(imageRef string) string {
	safe := strings.NewReplacer(":", "-", "/", "-").Replace(imageRef)
	return filepath.Join(b.cacheDir, "rootfs", "refs", safe)
}

func (b *Builder) PrepareRootfs(ctx context.Context, imageRef string) (rootfsExt4 string, digest string, err error) {
	markerPath := b.refMarkerPath(imageRef)
	if raw, readErr := os.ReadFile(markerPath); readErr == nil {
		hexPart := strings.TrimSpace(string(raw))
		cachedExt4 := filepath.Join(b.cacheDir, "rootfs", hexPart, "rootfs.ext4")
		if _, statErr := os.Stat(cachedExt4); statErr == nil {
			return cachedExt4, "sha256:" + hexPart, nil
		}
	}

	staging, err := os.MkdirTemp(b.cacheDir, "rootfs-staging-*")
	if err != nil {
		return "", "", fmt.Errorf("image: create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	layersDir := filepath.Join(staging, "layers")
	if err := os.MkdirAll(layersDir, 0o755); err != nil {
		return "", "", fmt.Errorf("image: create layers dir: %w", err)
	}

	fullDigest, err := b.extractor(ctx, imageRef, layersDir)
	if err != nil {
		return "", "", fmt.Errorf("image: extract %s: %w", imageRef, err)
	}

	hexPart := strings.TrimPrefix(fullDigest, "sha256:")
	cacheEntry := filepath.Join(b.cacheDir, "rootfs", hexPart)
	cachedExt4 := filepath.Join(cacheEntry, "rootfs.ext4")

	if _, statErr := os.Stat(cachedExt4); statErr != nil {
		if err := os.MkdirAll(cacheEntry, 0o755); err != nil {
			return "", "", fmt.Errorf("image: create cache entry: %w", err)
		}
		tmpExt4 := cachedExt4 + ".tmp"
		if err := buildExt4(ctx, layersDir, tmpExt4, b.mkfsPath, 0); err != nil {
			return "", "", fmt.Errorf("image: build rootfs ext4: %w", err)
		}
		if err := os.Rename(tmpExt4, cachedExt4); err != nil {
			return "", "", fmt.Errorf("image: install rootfs ext4: %w", err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err == nil {
		_ = os.WriteFile(markerPath, []byte(hexPart), 0o644)
	}
	return cachedExt4, fullDigest, nil
}

func (b *Builder) BuildOverlay(ctx context.Context, dir string, bundle openshell.BootstrapBundle, extra map[string][]byte) (overlayExt4 string, err error) {
	return buildOverlay(ctx, dir, bundle, extra, b.mkfsPath, b.overlaySizeMiB)
}
