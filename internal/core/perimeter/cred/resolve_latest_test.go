package cred

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
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

func newOCIRegistry(t *testing.T) (srv *httptest.Server, transport http.RoundTripper) {
	t.Helper()
	srv = httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	return srv, srv.Client().Transport
}

func pushRandomImage(t *testing.T, ref string, transport http.RoundTripper) string {
	t.Helper()
	img, err := random.Image(512, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	r, err := name.ParseReference(ref, name.Insecure)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if err := remote.Write(r, img, remote.WithTransport(transport)); err != nil {
		t.Fatalf("remote.Write: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}
	return digest.String()
}

func TestResolveFloatingVersions_OCILatestResolvedToDigest(t *testing.T) {
	srv, transport := newOCIRegistry(t)
	host := strings.TrimPrefix(srv.URL, "http://")
	imageRef := host + "/tool:nightly"
	wantDigest := pushRandomImage(t, imageRef, transport)

	defer setOCIRemoteOptions(remote.WithTransport(transport))()

	recipe := ToolRecipe{Packages: []RecipePackage{{
		Kind: RecipeKindOCI, Name: "tool", Version: FloatingVersion,
		Image: imageRef, SrcPath: "/bin/tool", InstallDir: "/usr/local/bin",
	}}}
	got, err := ResolveFloatingVersions(context.Background(), recipe)
	if err != nil {
		t.Fatal(err)
	}
	if got.Packages[0].Version != wantDigest {
		t.Fatalf("want %q got %q", wantDigest, got.Packages[0].Version)
	}
	if !strings.HasPrefix(got.Packages[0].Version, "sha256:") {
		t.Fatalf("digest must start with sha256: got %q", got.Packages[0].Version)
	}
}

func TestResolveFloatingVersions_OCIConcretePassThrough(t *testing.T) {
	srv, transport := newOCIRegistry(t)
	host := strings.TrimPrefix(srv.URL, "http://")
	concreteDigest := "sha256:" + strings.Repeat("a", 64)

	defer setOCIRemoteOptions(remote.WithTransport(transport))()

	recipe := ToolRecipe{Packages: []RecipePackage{{
		Kind: RecipeKindOCI, Name: "tool", Version: concreteDigest,
		Image: host + "/tool:nightly", SrcPath: "/bin/tool", InstallDir: "/usr/local/bin",
	}}}
	got, err := ResolveFloatingVersions(context.Background(), recipe)
	if err != nil {
		t.Fatal(err)
	}
	if got.Packages[0].Version != concreteDigest {
		t.Fatalf("want %q got %q", concreteDigest, got.Packages[0].Version)
	}
}

func TestResolveFloatingVersions_OCIUnknownTag(t *testing.T) {
	srv, transport := newOCIRegistry(t)
	host := strings.TrimPrefix(srv.URL, "http://")

	defer setOCIRemoteOptions(remote.WithTransport(transport))()

	recipe := ToolRecipe{Packages: []RecipePackage{{
		Kind: RecipeKindOCI, Name: "tool", Version: FloatingVersion,
		Image: host + "/nonexistent:nightly", SrcPath: "/bin/tool", InstallDir: "/usr/local/bin",
	}}}
	_, err := ResolveFloatingVersions(context.Background(), recipe)
	if err == nil {
		t.Fatal("expected error for unknown tag")
	}
	if !strings.Contains(err.Error(), host+"/nonexistent:nightly") {
		t.Errorf("error must name the image, got: %v", err)
	}
}

func TestResolveFloatingVersions_OCICancelledContext(t *testing.T) {
	srv, transport := newOCIRegistry(t)
	host := strings.TrimPrefix(srv.URL, "http://")

	defer setOCIRemoteOptions(remote.WithTransport(transport))()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	recipe := ToolRecipe{Packages: []RecipePackage{{
		Kind: RecipeKindOCI, Name: "tool", Version: FloatingVersion,
		Image: host + "/tool:nightly", SrcPath: "/bin/tool", InstallDir: "/usr/local/bin",
	}}}
	_, err := ResolveFloatingVersions(ctx, recipe)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestResolveFloatingVersions_OCIMixedWithNPM(t *testing.T) {
	npmSrv := httptest.NewServer(registryHandler("4.5.6"))
	defer npmSrv.Close()
	defer setRegistryBaseURL(npmSrv.URL)()

	ociSrv, transport := newOCIRegistry(t)
	host := strings.TrimPrefix(ociSrv.URL, "http://")
	imageRef := host + "/tool:nightly"
	wantDigest := pushRandomImage(t, imageRef, transport)

	defer setOCIRemoteOptions(remote.WithTransport(transport))()

	recipe := ToolRecipe{Packages: []RecipePackage{
		{Kind: RecipeKindNPM, Name: "my-pkg", Version: FloatingVersion},
		{
			Kind: RecipeKindOCI, Name: "tool", Version: FloatingVersion,
			Image: imageRef, SrcPath: "/bin/tool", InstallDir: "/usr/local/bin",
		},
	}}
	got, err := ResolveFloatingVersions(context.Background(), recipe)
	if err != nil {
		t.Fatal(err)
	}
	if got.Packages[0].Version != "4.5.6" {
		t.Fatalf("npm: want 4.5.6 got %q", got.Packages[0].Version)
	}
	if got.Packages[1].Version != wantDigest {
		t.Fatalf("oci: want %q got %q", wantDigest, got.Packages[1].Version)
	}
}
