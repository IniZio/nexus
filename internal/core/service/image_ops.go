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
	// builderAgentTag: image.BuilderAgentTag of the host's nexus3-agent; "" skips the template sweep.
	builderAgentTag string
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

// WithBuilderAgentTag sets the live agent tag; templates with any other tag are stale. "" skips the sweep.
func (s *ImageService) WithBuilderAgentTag(tag string) *ImageService {
	s.builderAgentTag = tag
	return s
}

// PrunePlan is what PruneImages would remove, computed without unlinking.
type PrunePlan struct {
	Images        []domain.Image          // unreferenced cache entries (candidates)
	Templates     []image.BuilderTemplate // stale builder templates that would be removed
	ImageBytes    int64                   // sum of Images[i].Size
	TemplateBytes int64                   // allocated bytes of Templates
	TemplateSweep bool                    // false when no agent tag is known (sweep skipped)
}

type PruneResult struct {
	Removed       int   // cache entries actually removed
	Templates     int   // builder templates actually removed
	FreedBytes    int64 // artifact sizes + template allocated bytes
	TemplateSweep bool  // false when no agent tag is known (sweep skipped)
}

// PlanPrune reports what PruneImages would remove without unlinking anything.
func (s *ImageService) PlanPrune(ctx context.Context) (PrunePlan, error) {
	ref, err := ReferencedDigests(ctx, s.cache, s.store)
	if err != nil {
		return PrunePlan{}, fmt.Errorf("image: prune: compute refs: %w", err)
	}
	imgs, err := s.cache.PruneCandidates(ctx, ref)
	if err != nil {
		return PrunePlan{}, fmt.Errorf("image: prune: candidates: %w", err)
	}
	plan := PrunePlan{Images: imgs, TemplateSweep: s.builderAgentTag != ""}
	for _, img := range imgs {
		plan.ImageBytes += img.Size
	}
	tpls, freed, err := s.cache.PruneBuilderTemplates(ctx, s.builderAgentTag, true)
	if err != nil {
		return PrunePlan{}, fmt.Errorf("image: prune: builder templates: %w", err)
	}
	plan.Templates = tpls
	plan.TemplateBytes = freed
	return plan, nil
}

// PruneImages removes unreferenced cache entries and, when an agent tag is known, stale builder templates.
func (s *ImageService) PruneImages(ctx context.Context) (PruneResult, error) {
	ref, err := ReferencedDigests(ctx, s.cache, s.store)
	if err != nil {
		return PruneResult{}, fmt.Errorf("image: prune: compute refs: %w", err)
	}
	candidates, err := s.cache.PruneCandidates(ctx, ref)
	if err != nil {
		return PruneResult{}, fmt.Errorf("image: prune: candidates: %w", err)
	}
	n, err := s.cache.Prune(ctx, ref)
	if err != nil {
		return PruneResult{}, fmt.Errorf("image: prune: %w", err)
	}
	res := PruneResult{Removed: n, TemplateSweep: s.builderAgentTag != ""}
	for _, img := range candidates {
		res.FreedBytes += img.Size
	}
	tpls, freed, err := s.cache.PruneBuilderTemplates(ctx, s.builderAgentTag, false)
	if err != nil {
		return res, fmt.Errorf("image: prune: builder templates: %w", err)
	}
	res.Templates = len(tpls)
	res.FreedBytes += freed
	return res, nil
}

var ErrNoBuilder = fmt.Errorf("no builder configured (builder VM integration not yet wired)")
