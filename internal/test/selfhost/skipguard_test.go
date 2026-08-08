// Package selfhost_test contains unit-level tests for the skip guard behaviour
// of BuildSelfHostBaseImage. These tests do NOT require docker or mke2fs.
package selfhost_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/test/selfhost"
)

// TestBuildSelfHostBaseImageSkipDocker verifies that BuildSelfHostBaseImage
// returns ErrDockerUnavailable when docker is not on PATH. No docker needed.
func TestBuildSelfHostBaseImageSkipDocker(t *testing.T) {
	orig := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", orig) })
	os.Setenv("PATH", "")

	ctx := context.Background()
	cacheDir := t.TempDir()
	cache, err := image.NewCache(cacheDir)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	_, err = selfhost.BuildSelfHostBaseImage(ctx, cache)
	if !errors.Is(err, selfhost.ErrDockerUnavailable) {
		t.Errorf("expected ErrDockerUnavailable, got: %v", err)
	}
}
