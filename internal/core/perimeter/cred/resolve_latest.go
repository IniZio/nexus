package cred

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

var registryBaseURL = "https://registry.npmjs.org"

func setRegistryBaseURL(u string) func() {
	old := registryBaseURL
	registryBaseURL = u
	return func() { registryBaseURL = old }
}

var ociRemoteOptions []remote.Option

func setOCIRemoteOptions(opts ...remote.Option) func() {
	old := ociRemoteOptions
	ociRemoteOptions = opts
	return func() { ociRemoteOptions = old }
}

// ResolveFloatingVersions returns a copy of recipe where every floating npm
// package version is replaced by the concrete dist-tags.latest from the npm
// registry, and every floating OCI package version is replaced by the image's
// manifest/index digest. Concrete versions pass through unchanged with no
// network call.
func ResolveFloatingVersions(ctx context.Context, recipe ToolRecipe) (ToolRecipe, error) {
	pkgs := make([]RecipePackage, len(recipe.Packages))
	copy(pkgs, recipe.Packages)

	for i, p := range pkgs {
		if !p.IsFloating() {
			continue
		}
		switch p.Kind {
		case RecipeKindNPM:
			version, err := resolveNPMLatest(ctx, p.Name)
			if err != nil {
				return ToolRecipe{}, err
			}
			pkgs[i].Version = version
		case RecipeKindOCI:
			digest, err := resolveOCIDigest(ctx, p.Image)
			if err != nil {
				return ToolRecipe{}, err
			}
			pkgs[i].Version = digest
		}
	}

	out := recipe
	out.Packages = pkgs
	return out, nil
}

func resolveOCIDigest(ctx context.Context, image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", fmt.Errorf("resolve oci digest for %s: %w", image, err)
	}
	opts := append([]remote.Option{remote.WithContext(ctx)}, ociRemoteOptions...)
	desc, err := remote.Head(ref, opts...)
	if err != nil {
		return "", fmt.Errorf("resolve oci digest for %s: %w", image, err)
	}
	return desc.Digest.String(), nil
}

func resolveNPMLatest(ctx context.Context, name string) (string, error) {
	reqURL := registryBaseURL + "/" + url.PathEscape(name)

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("cred: resolveNPMLatest(%q): build request: %w", name, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cred: resolveNPMLatest(%q): request: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cred: resolveNPMLatest(%q): registry returned HTTP %d", name, resp.StatusCode)
	}

	var body struct {
		DistTags struct {
			Latest string `json:"latest"`
		} `json:"dist-tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("cred: resolveNPMLatest(%q): decode response: %w", name, err)
	}

	if body.DistTags.Latest == "" {
		return "", fmt.Errorf("cred: resolveNPMLatest(%q): dist-tags.latest is empty or absent", name)
	}

	return body.DistTags.Latest, nil
}
