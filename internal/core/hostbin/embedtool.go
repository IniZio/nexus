package hostbin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ResolveEmbeddedTool extracts the embedded tool binary `name` (e.g.
// "nexus-hub") to DataDir/artifacts/<sha>/<name> and returns its path. Unlike
// ResolveAgent there is no PATH or kernel-dir fallback; the only override is
// the test-only env var EnvVar(name).
func (r *Resolver) ResolveEmbeddedTool(name string) (string, error) {
	if v := r.getenv(EnvVar(name)); v != "" {
		fi, err := os.Stat(v)
		if err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%w: %s=%q (stat: %v)", ErrNotFound, EnvVar(name), v, err)
		}
		return v, nil
	}
	emb := r.embedded()
	if _, err := fs.Stat(emb, name+".zst"); err != nil {
		return "", fmt.Errorf("%w: %s not embedded; run `make artifacts`", ErrNotFound, name)
	}
	shaFile, err := fs.ReadFile(emb, name+".sha256")
	wantSHA := strings.TrimSpace(string(shaFile))
	if err != nil || wantSHA == "" {
		return "", fmt.Errorf("%w: embedded %s.sha256 missing; run `make artifacts`", ErrNotFound, name)
	}
	finalPath := filepath.Join(r.dataDir(), "artifacts", wantSHA, name)
	if fi, serr := os.Stat(finalPath); serr == nil && fi.Mode().IsRegular() {
		return finalPath, nil
	}
	data, err := decompressEmbedded(emb, name, wantSHA)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(finalPath, data); err != nil {
		return "", fmt.Errorf("hostbin: write %s: %w", name, err)
	}
	return finalPath, nil
}

// ResolveEmbeddedTool is a convenience wrapper using a zero-value Resolver.
func ResolveEmbeddedTool(name string) (string, error) {
	return (&Resolver{}).ResolveEmbeddedTool(name)
}
