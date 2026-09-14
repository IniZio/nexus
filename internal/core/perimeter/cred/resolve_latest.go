package cred

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

var registryBaseURL = "https://registry.npmjs.org"

func setRegistryBaseURL(u string) func() {
	old := registryBaseURL
	registryBaseURL = u
	return func() { registryBaseURL = old }
}

// ResolveFloatingVersions returns a copy of recipe where every floating npm
// package version is replaced by the concrete dist-tags.latest from the npm
// registry. Concrete versions pass through unchanged with no network call.
func ResolveFloatingVersions(ctx context.Context, recipe ToolRecipe) (ToolRecipe, error) {
	pkgs := make([]RecipePackage, len(recipe.Packages))
	copy(pkgs, recipe.Packages)

	for i, p := range pkgs {
		if p.Kind != RecipeKindNPM || !p.IsFloating() {
			continue
		}
		version, err := resolveNPMLatest(ctx, p.Name)
		if err != nil {
			return ToolRecipe{}, err
		}
		pkgs[i].Version = version
	}

	out := recipe
	out.Packages = pkgs
	return out, nil
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
