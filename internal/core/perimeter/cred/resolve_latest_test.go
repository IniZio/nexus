package cred

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func registryHandler(version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"dist-tags": map[string]string{"latest": version},
		})
	})
}

func floatingRecipe(name string) ToolRecipe {
	return ToolRecipe{Packages: []RecipePackage{{Kind: RecipeKindNPM, Name: name, Version: FloatingVersion}}}
}

func TestResolveFloatingVersions_LatestResolved(t *testing.T) {
	srv := httptest.NewServer(registryHandler("1.2.3"))
	defer srv.Close()
	defer setRegistryBaseURL(srv.URL)()

	got, err := ResolveFloatingVersions(context.Background(), floatingRecipe("my-pkg"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Packages[0].Version != "1.2.3" {
		t.Fatalf("want 1.2.3 got %q", got.Packages[0].Version)
	}
}

func TestResolveFloatingVersions_ConcretePassThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("registry must not be called for a concrete version")
	}))
	defer srv.Close()
	defer setRegistryBaseURL(srv.URL)()

	recipe := ToolRecipe{Packages: []RecipePackage{{Kind: RecipeKindNPM, Name: "my-pkg", Version: "2.0.0"}}}
	got, err := ResolveFloatingVersions(context.Background(), recipe)
	if err != nil {
		t.Fatal(err)
	}
	if got.Packages[0].Version != "2.0.0" {
		t.Fatalf("want 2.0.0 got %q", got.Packages[0].Version)
	}
}

func TestResolveFloatingVersions_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	defer setRegistryBaseURL(srv.URL)()

	_, err := ResolveFloatingVersions(context.Background(), floatingRecipe("bad-pkg"))
	if err == nil {
		t.Fatal("expected error for non-200")
	}
	if !strings.Contains(err.Error(), "bad-pkg") {
		t.Errorf("error must mention package name: %v", err)
	}
}

func TestResolveFloatingVersions_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{not valid json"))
	}))
	defer srv.Close()
	defer setRegistryBaseURL(srv.URL)()

	_, err := ResolveFloatingVersions(context.Background(), floatingRecipe("bad-json-pkg"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "bad-json-pkg") {
		t.Errorf("error must mention package name: %v", err)
	}
}

func TestResolveFloatingVersions_EmptyDistTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"dist-tags": map[string]string{}})
	}))
	defer srv.Close()
	defer setRegistryBaseURL(srv.URL)()

	_, err := ResolveFloatingVersions(context.Background(), floatingRecipe("no-latest-pkg"))
	if err == nil {
		t.Fatal("expected error for empty dist-tags.latest")
	}
	if !strings.Contains(err.Error(), "no-latest-pkg") {
		t.Errorf("error must mention package name: %v", err)
	}
}

func TestResolveFloatingVersions_ScopedPackagePath(t *testing.T) {
	var observedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedPath = r.URL.RawPath
		if observedPath == "" {
			observedPath = r.URL.Path
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"dist-tags": map[string]string{"latest": "3.0.0"},
		})
	}))
	defer srv.Close()
	defer setRegistryBaseURL(srv.URL)()

	_, err := ResolveFloatingVersions(context.Background(), floatingRecipe("@anthropic-ai/claude-code"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(observedPath, "%2F") {
		t.Errorf("expected '/' to be percent-encoded in path, got %q", observedPath)
	}
}

func TestResolveFloatingVersions_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	defer setRegistryBaseURL(srv.URL)()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ResolveFloatingVersions(ctx, floatingRecipe("some-pkg"))
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
