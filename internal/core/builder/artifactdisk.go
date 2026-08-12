package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// ArtifactFromDisk reads the raw ext4 image at artifactExt4 (written by the
// builder VM to /dev/vdc), computes its SHA-256 digest, and stores it in
// cache via [image.Cache.Put].
//
// The returned digest string is in "sha256:<hex>" form and matches the key
// under which the image can be retrieved with [image.Cache.Get].
//
// The artifact is stored with [domain.KindBuilder] so callers can distinguish
// it from base images.
func ArtifactFromDisk(ctx context.Context, artifactExt4 string, cache *image.Cache) (string, error) {
	f, err := os.Open(artifactExt4)
	if err != nil {
		return "", fmt.Errorf("artifactdisk: open: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("artifactdisk: stat: %w", err)
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("artifactdisk: hash: %w", err)
	}
	digestHex := hex.EncodeToString(h.Sum(nil))
	d, err := domain.ParseDigest("sha256:" + digestHex)
	if err != nil {
		return "", fmt.Errorf("artifactdisk: parse digest: %w", err)
	}

	img := domain.Image{
		Digest:    d,
		Ref:       "artifact:" + digestHex[:12],
		Kind:      domain.KindBuilder,
		Size:      info.Size(),
		CreatedAt: time.Now().UTC(),
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("artifactdisk: seek: %w", err)
	}
	if err := cache.Put(ctx, img, f); err != nil {
		return "", fmt.Errorf("artifactdisk: cache put: %w", err)
	}
	return d.String(), nil
}
