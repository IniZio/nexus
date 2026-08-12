//go:build linux

package builderimage

import (
	"context"
	"path/filepath"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// BuildExt4ForTest exposes the unexported buildExt4 function for integration
// tests that need to pack a directory into a raw ext4 disk image.
func BuildExt4ForTest(ctx context.Context, srcDir, dstPath string) error {
	return buildExt4(ctx, srcDir, dstPath)
}

// origResolveDigest and origPullRemoteImage hold the real network
// implementations so ResetTestOverrides can restore them after a test override.
var (
	origResolveDigest    = resolveDigest
	origPullRemoteImage  = pullRemoteImage
)

// SetResolveDigestForTest replaces the resolveDigest function variable for the
// duration of a test. Call ResetTestOverrides in t.Cleanup to restore defaults.
func SetResolveDigestForTest(fn func(ctx context.Context, ociRef string) (string, error)) {
	resolveDigest = fn
}

// SetPullRemoteImageForTest replaces the pullRemoteImage function variable for
// the duration of a test. Call ResetTestOverrides in t.Cleanup to restore defaults.
func SetPullRemoteImageForTest(fn func(ctx context.Context, ociRef string) (v1.Image, error)) {
	pullRemoteImage = fn
}

// ResetTestOverrides restores the real network implementations of resolveDigest
// and pullRemoteImage. Call via t.Cleanup after any Set*ForTest call.
func ResetTestOverrides() {
	resolveDigest = origResolveDigest
	pullRemoteImage = origPullRemoteImage
}

// BuilderImageCachePathForTest returns the full host path EnsureBuilderImage
// would use for the given OCI digest and agent bytes. Tests call this to
// pre-create the cache file (simulating a prior build) without duplicating
// the path formula.
func BuilderImageCachePathForTest(dataDir, ociDigest string, agentBytes []byte) string {
	digestSafe := strings.NewReplacer(":", "-", "/", "-").Replace(ociDigest)
	imagesDir := filepath.Join(dataDir, "images")
	return builderImageCachePath(imagesDir, digestSafe, agentBytes)
}
