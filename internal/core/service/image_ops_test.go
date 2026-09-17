package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/service"
)

const (
	staleTemplateName = "nexus-builder-abc-agent0123456789abcdef.ext4"
	liveAgentTag      = "fedcba9876543210"
)

// newPruneFixture seeds one unreferenced builder image and one stale builder
// template (mtime well outside BuilderTemplateInFlightGrace) into a fresh cache.
func newPruneFixture(t *testing.T) (*service.ImageService, *image.Cache, domain.Digest, string) {
	t.Helper()
	root := t.TempDir()
	c, err := image.NewCache(root)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	d := putImage(t, c, []byte("orphan builder rootfs"), domain.KindBuilder)

	tpl := filepath.Join(root, staleTemplateName)
	if err := os.WriteFile(tpl, make([]byte, 64*1024), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	old := time.Now().Add(-2 * image.BuilderTemplateInFlightGrace)
	if err := os.Chtimes(tpl, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	svc := service.NewImageService(c, nil)
	svc.WithStore(&fakeSandboxImageLister{})
	return svc, c, d, tpl
}

func TestPlanPrune_ListsCandidatesWithoutRemoving(t *testing.T) {
	ctx := context.Background()
	svc, c, d, tpl := newPruneFixture(t)
	svc.WithBuilderAgentTag(liveAgentTag)

	plan, err := svc.PlanPrune(ctx)
	if err != nil {
		t.Fatalf("PlanPrune: %v", err)
	}
	if len(plan.Images) != 1 || plan.Images[0].Digest != d {
		t.Fatalf("Images: got %+v, want one entry %s", plan.Images, d)
	}
	if plan.ImageBytes != plan.Images[0].Size || plan.ImageBytes <= 0 {
		t.Errorf("ImageBytes: got %d, want %d", plan.ImageBytes, plan.Images[0].Size)
	}
	if !plan.TemplateSweep {
		t.Error("TemplateSweep: got false, want true with an agent tag set")
	}
	if len(plan.Templates) != 1 || filepath.Base(plan.Templates[0].Path) != staleTemplateName {
		t.Fatalf("Templates: got %+v, want %s", plan.Templates, staleTemplateName)
	}
	if plan.TemplateBytes <= 0 {
		t.Errorf("TemplateBytes: got %d, want > 0", plan.TemplateBytes)
	}

	imgs, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(imgs) != 1 {
		t.Errorf("PlanPrune removed cache entries: %d remain, want 1", len(imgs))
	}
	if _, err := os.Stat(tpl); err != nil {
		t.Errorf("PlanPrune removed template: %v", err)
	}
}

func TestPruneImages_RemovesCandidatesAndTemplates(t *testing.T) {
	ctx := context.Background()
	svc, c, _, tpl := newPruneFixture(t)
	svc.WithBuilderAgentTag(liveAgentTag)

	res, err := svc.PruneImages(ctx)
	if err != nil {
		t.Fatalf("PruneImages: %v", err)
	}
	if res.Removed != 1 || res.Templates != 1 {
		t.Errorf("got Removed=%d Templates=%d, want 1/1", res.Removed, res.Templates)
	}
	if res.FreedBytes <= 0 {
		t.Errorf("FreedBytes: got %d, want > 0", res.FreedBytes)
	}
	if !res.TemplateSweep {
		t.Error("TemplateSweep: got false, want true")
	}

	imgs, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("post-prune: %d entries remain, want 0", len(imgs))
	}
	if _, err := os.Stat(tpl); !os.IsNotExist(err) {
		t.Errorf("template still present after prune (stat err=%v)", err)
	}
}

func TestPruneImages_NoAgentTagSkipsTemplateSweep(t *testing.T) {
	ctx := context.Background()
	svc, _, _, tpl := newPruneFixture(t)

	plan, err := svc.PlanPrune(ctx)
	if err != nil {
		t.Fatalf("PlanPrune: %v", err)
	}
	if plan.TemplateSweep || len(plan.Templates) != 0 || plan.TemplateBytes != 0 {
		t.Errorf("plan without tag: got sweep=%v templates=%d bytes=%d, want false/0/0",
			plan.TemplateSweep, len(plan.Templates), plan.TemplateBytes)
	}

	res, err := svc.PruneImages(ctx)
	if err != nil {
		t.Fatalf("PruneImages: %v", err)
	}
	if res.TemplateSweep || res.Templates != 0 {
		t.Errorf("result without tag: got sweep=%v templates=%d, want false/0", res.TemplateSweep, res.Templates)
	}
	if res.Removed != 1 {
		t.Errorf("Removed: got %d, want 1 (image prune must still run)", res.Removed)
	}
	if _, err := os.Stat(tpl); err != nil {
		t.Errorf("template removed despite no agent tag: %v", err)
	}
}
