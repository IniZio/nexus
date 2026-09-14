package service

import (
	"context"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

type ImageBuilder interface {
	Build(ctx context.Context, req builder.BuildRequest) (domain.Image, error)
}

type ImageService struct {
	cache   *image.Cache
	builder ImageBuilder
	store   SandboxImageLister
	// versionResolver resolves floating tool versions; nil means use cred.ResolveFloatingVersions.
	versionResolver func(context.Context, cred.ToolRecipe) (cred.ToolRecipe, error)
}

func NewImageService(c *image.Cache, b ImageBuilder) *ImageService {
	return &ImageService{cache: c, builder: b}
}

func (s *ImageService) WithStore(sl SandboxImageLister) {
	s.store = sl
}

func (s *ImageService) WithVersionResolver(r func(context.Context, cred.ToolRecipe) (cred.ToolRecipe, error)) {
	s.versionResolver = r
}

// BuildImage resolves floating tool versions before Build so the image-cache key carries concrete versions, not symbolic tags.
func (s *ImageService) BuildImage(ctx context.Context, req builder.BuildRequest) (domain.Image, error) {
	if s.builder == nil {
		return domain.Image{}, fmt.Errorf("image: build: %w", ErrNoBuilder)
	}
	resolve := s.versionResolver
	if resolve == nil {
		resolve = cred.ResolveFloatingVersions
	}
	resolved, err := resolve(ctx, req.ToolRecipe)
	if err != nil {
		return domain.Image{}, fmt.Errorf("image: build: resolve tool recipe versions: %w", err)
	}
	req.ToolRecipe = resolved
	img, err := s.builder.Build(ctx, req)
	if err != nil {
		return domain.Image{}, fmt.Errorf("image: build: %w", err)
	}
	return img, nil
}

func (s *ImageService) ListImages(ctx context.Context) ([]domain.Image, error) {
	imgs, err := s.cache.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("image: list: %w", err)
	}
	return imgs, nil
}

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

var ErrNoBuilder = fmt.Errorf("no builder configured (builder VM integration not yet wired)")
