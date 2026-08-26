package service

import (
	"context"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// ImageBuilder is the minimal interface the image service requires from the
// builder layer. Defined as an interface so that tests can inject a fake
// without needing buildkitd, mke2fs, or any other external tooling.
type ImageBuilder interface {
	Build(ctx context.Context, req builder.BuildRequest) (domain.Image, error)
}

// ImageService is the image-ops coordination layer. It sits between the CLI
// surface and the builder/cache packages, mirroring the structure of the
// sandbox Service but scoped to content-addressed rootfs images.
//
// The zero value is not usable; construct one with NewImageService.
type ImageService struct {
	cache   *image.Cache
	builder ImageBuilder
	store   SandboxImageLister
}

// NewImageService returns an ImageService backed by the given cache and builder.
// b may be nil when only listing or pruning images (build is unavailable then).
func NewImageService(c *image.Cache, b ImageBuilder) *ImageService {
	return &ImageService{cache: c, builder: b}
}

// WithStore wires in a SandboxImageLister so PruneImages can retain images
// referenced by live sandbox records. Without this, only KindBase images are
// kept during pruning.
func (s *ImageService) WithStore(sl SandboxImageLister) {
	s.store = sl
}

// BuildImage drives the builder to produce a bootable rootfs from req, stores
// it in the cache, and returns the resulting domain.Image. Returns an error
// wrapping ErrNoBuilder if no builder was configured.
func (s *ImageService) BuildImage(ctx context.Context, req builder.BuildRequest) (domain.Image, error) {
	if s.builder == nil {
		return domain.Image{}, fmt.Errorf("image: build: %w", ErrNoBuilder)
	}
	img, err := s.builder.Build(ctx, req)
	if err != nil {
		return domain.Image{}, fmt.Errorf("image: build: %w", err)
	}
	return img, nil
}

// ListImages returns metadata for all images currently in the cache.
// An empty slice (not an error) is returned when the cache is empty.
func (s *ImageService) ListImages(ctx context.Context) ([]domain.Image, error) {
	imgs, err := s.cache.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("image: list: %w", err)
	}
	return imgs, nil
}

// PruneImages removes cache entries not referenced by any sandbox or base image.
//
// With a store wired in via WithStore, only true orphans are removed.
// Without a store, only KindBase images are preserved and all others are removed.
//
// Returns the number of entries removed.
func (s *ImageService) PruneImages(ctx context.Context) (int, error) {
	ref, err := ReferencedDigests(ctx, s.cache, s.store)
	if err != nil {
		return 0, fmt.Errorf("image: prune: compute refs: %w", err)
	}
	n, err := s.cache.Prune(ctx, ref)
	if err != nil {
		return 0, fmt.Errorf("image: prune: %w", err)
	}
	return n, nil
}

// ErrNoBuilder is returned by BuildImage when no builder was wired into the
// ImageService. This happens when the CLI constructs a list/prune-only service
// (e.g. before the builder VM integration is complete).
var ErrNoBuilder = fmt.Errorf("no builder configured (builder VM integration not yet wired)")
