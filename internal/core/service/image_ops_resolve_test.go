package service_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/IniZio/nexus/internal/core/builder"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
)

type captureBuilder struct {
	received cred.ToolRecipe
	called   bool
}

func (c *captureBuilder) Build(_ context.Context, req builder.BuildRequest) (domain.Image, error) {
	c.received = req.ToolRecipe
	c.called = true
	return domain.Image{}, nil
}

func TestBuildImage_FloatingVersionResolvedBeforeBuilder(t *testing.T) {
	stub := &captureBuilder{}
	svc := service.NewImageService(nil, stub)
	svc.WithVersionResolver(func(_ context.Context, r cred.ToolRecipe) (cred.ToolRecipe, error) {
		out := r
		pkgs := make([]cred.RecipePackage, len(r.Packages))
		copy(pkgs, r.Packages)
		for i, p := range pkgs {
			if p.Version == cred.FloatingVersion {
				pkgs[i].Version = "1.2.3"
			}
		}
		out.Packages = pkgs
		return out, nil
	})
	req := builder.BuildRequest{
		ToolRecipe: cred.ToolRecipe{
			Packages: []cred.RecipePackage{
				{Kind: cred.RecipeKindNPM, Name: "claude", Version: cred.FloatingVersion},
			},
		},
	}
	_, _ = svc.BuildImage(context.Background(), req)
	if !stub.called {
		t.Fatal("builder.Build was not called")
	}
	if len(stub.received.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(stub.received.Packages))
	}
	if stub.received.Packages[0].Version == cred.FloatingVersion {
		t.Fatalf("floating version reached builder; want concrete, got %q", stub.received.Packages[0].Version)
	}
}

func TestBuildFingerprint_DifferentConcretVersionsDifferentKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/.dockerignore", []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	cf := []byte("FROM debian:bookworm-slim\nRUN echo hello\n")
	agent := []byte("fake-agent-binary")

	recipe1 := cred.ToolRecipe{Packages: []cred.RecipePackage{{Kind: cred.RecipeKindNPM, Name: "claude", Version: "1.0.0"}}}
	recipe2 := cred.ToolRecipe{Packages: []cred.RecipePackage{{Kind: cred.RecipeKindNPM, Name: "claude", Version: "2.0.0"}}}

	fp1, err := builder.BuildFingerprint(cf, "debian:bookworm-slim", agent, dir, recipe1, "x64")
	if err != nil {
		t.Fatalf("fingerprint1: %v", err)
	}
	fp2, err := builder.BuildFingerprint(cf, "debian:bookworm-slim", agent, dir, recipe2, "x64")
	if err != nil {
		t.Fatalf("fingerprint2: %v", err)
	}
	if fp1 == fp2 {
		t.Fatalf("different concrete versions produced identical cache key %q; stale-cache regression", fp1)
	}
}

func TestBuildImage_ResolveErrorAborts(t *testing.T) {
	stub := &captureBuilder{}
	svc := service.NewImageService(nil, stub)
	resolveErr := errors.New("registry unreachable")
	svc.WithVersionResolver(func(_ context.Context, r cred.ToolRecipe) (cred.ToolRecipe, error) {
		return cred.ToolRecipe{}, resolveErr
	})
	req := builder.BuildRequest{
		ToolRecipe: cred.ToolRecipe{
			Packages: []cred.RecipePackage{
				{Kind: cred.RecipeKindNPM, Name: "claude", Version: cred.FloatingVersion},
			},
		},
	}
	_, err := svc.BuildImage(context.Background(), req)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("expected resolver error surfaced, got: %v", err)
	}
	if stub.called {
		t.Fatal("builder.Build was called despite resolution failure")
	}
}
