//go:build !linux

package builderimage

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/image"
)

const DefaultOCIRef = "docker.io/moby/buildkit:v0.19.0"

var ErrMke2fsUnavailable = errors.New("mke2fs unavailable")

func errUnsupported() error {
	return fmt.Errorf("builderimage: not supported on %s", runtime.GOOS)
}

func EnsureBuilderImage(ctx context.Context, dataDir string, embeddedAgentBytes []byte) (string, error) {
	return "", errUnsupported()
}

func EnsureBuilderImageWithTools(ctx context.Context, dataDir string, embeddedAgentBytes []byte, tools []toolcache.Fetched) (string, error) {
	return "", errUnsupported()
}

func PullAndCacheOCI(ctx context.Context, ociRef string, c *image.Cache, agentBytes []byte) (string, error) {
	return "", errUnsupported()
}

func PullAndCacheOCIWithTools(ctx context.Context, ociRef string, c *image.Cache, agentBytes []byte, tools []toolcache.Fetched) (string, error) {
	return "", errUnsupported()
}

func DiscoverBuildkitdPath(stagingDir string) (string, error) { return "", errUnsupported() }

func DiscoverBuildctlPath(stagingDir string) (string, error) { return "", errUnsupported() }
