//go:build linux

// Package builderimage bootstraps a bootable builder-VM rootfs ext4 image
// from the public moby/buildkit OCI image using pure-Go layer extraction.
// No docker, podman, or buildkitd is required on the host; only mke2fs
// (from e2fsprogs) is exec'd to format the final ext4 image.
//
// # Cache layout
//
//	<dataDir>/images/nexus-builder-<digest>-agent<agenthash>.ext4
//
// The digest is the OCI manifest digest of the pulled image; agenthash is
// the first 16 hex characters of SHA-256(agentBytes). Both are required
// so that a changed or grown nexus3-agent binary forces a rebuild instead
// of reusing a stale image sized for a smaller agent.
package builderimage

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// DefaultOCIRef is the public moby/buildkit image reference used as the
// builder VM base. Pinned to the same tag as old-nexus for reproducibility.
const DefaultOCIRef = "docker.io/moby/buildkit:v0.19.0"

// ErrMke2fsUnavailable is returned when mke2fs is not found on the host PATH.
// Tests should skip (not fail) when they see this error.
var ErrMke2fsUnavailable = errors.New("mke2fs not found in PATH (install e2fsprogs)")

// imageSizeHeadroomFactor is applied to the staging dir size to compute the
// ext4 image size, covering metadata overhead and alignment padding.
const imageSizeHeadroomFactor = 2

// imageMinSizeBytes is the minimum ext4 image size (64 MiB). mke2fs requires
// enough space for at least the superblock, inode table, and journal.
const imageMinSizeBytes = 64 * 1024 * 1024

// deterministicUUID is a fixed ext4 filesystem UUID. Same value as
// builder/ext4.go — ensures identical content → identical bytes → stable
// digest on the final image file.
const deterministicUUID = "00000000-0000-0000-0000-000000000000"

// deterministicHashSeed pins the HTree directory seed to remove another
// source of non-determinism across builds.
const deterministicHashSeed = "00000000-0000-0000-0000-000000000000"

// resolveDigest is a var so tests can override it without hitting the network.
var resolveDigest = func(ctx context.Context, ociRef string) (string, error) {
	ref, err := name.ParseReference(ociRef)
	if err != nil {
		return "", fmt.Errorf("parse OCI ref %q: %w", ociRef, err)
	}
	desc, err := remote.Get(ref, remote.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("resolve OCI ref %q: %w", ociRef, err)
	}
	return desc.Digest.String(), nil
}

// pullRemoteImage is a var so tests can override it without hitting the network.
var pullRemoteImage = func(ctx context.Context, ociRef string) (v1.Image, error) {
	ref, err := name.ParseReference(ociRef)
	if err != nil {
		return nil, fmt.Errorf("parse OCI ref %q: %w", ociRef, err)
	}
	img, err := remote.Image(ref, remote.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("pull OCI image %q: %w", ociRef, err)
	}
	return img, nil
}

// builderImageCachePath returns the host path for a cached builder ext4 image.
//
// The filename encodes both the OCI digest (digestSafe, already sanitised for
// filesystem use) and a short hash of the agent binary. Including the agent
// hash ensures that a grown or rebuilt nexus3-agent produces a distinct key
// and triggers a fresh image build rather than reusing a stale, too-small
// ext4.
func builderImageCachePath(imagesDir, digestSafe string, agentBytes []byte) string {
	agentSum := sha256.Sum256(agentBytes)
	agentTag := fmt.Sprintf("%x", agentSum[:8]) // 16 hex chars — sufficient for version skew
	return filepath.Join(imagesDir, fmt.Sprintf("nexus-builder-%s-agent%s.ext4", digestSafe, agentTag))
}

// EnsureBuilderImage returns the host path to a bootable raw ext4 image built
// from the moby/buildkit OCI image. On first call it pulls the image, extracts
// its layers, adds VM-boot infrastructure, and converts to ext4. Subsequent
// calls for the same OCI digest skip all network and conversion work.
//
// embeddedAgentBytes is the raw bytes of the nexus3-agent binary to inject
// into the rootfs as PID-1 for the builder VM. It must not be nil or empty.
//
// dataDir is the nexus3 data directory; images are written under
// <dataDir>/images/.
func EnsureBuilderImage(ctx context.Context, dataDir string, embeddedAgentBytes []byte) (string, error) {
	if len(embeddedAgentBytes) == 0 {
		return "", fmt.Errorf("builderimage: embeddedAgentBytes must not be empty")
	}

	slog.Info("builderimage: resolving OCI digest", "ref", DefaultOCIRef)
	digest, err := resolveDigest(ctx, DefaultOCIRef)
	if err != nil {
		return "", fmt.Errorf("builderimage: %w", err)
	}

	// Sanitise the digest for use as a filename component.
	// "sha256:abc123" → "sha256-abc123"
	digestSafe := strings.NewReplacer(":", "-", "/", "-").Replace(digest)
	imagesDir := filepath.Join(dataDir, "images")
	cachePath := builderImageCachePath(imagesDir, digestSafe, embeddedAgentBytes)

	if info, err := os.Stat(cachePath); err == nil && info.Size() > 0 {
		slog.Info("builderimage: cache hit", "path", cachePath, "digest", digest)
		return cachePath, nil
	}

	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return "", fmt.Errorf("builderimage: mkdir images: %w", err)
	}

	slog.Info("builderimage: pulling OCI layers (may take several minutes)", "ref", DefaultOCIRef)
	img, err := pullRemoteImage(ctx, DefaultOCIRef)
	if err != nil {
		return "", fmt.Errorf("builderimage: pull: %w", err)
	}

	stagingDir, err := os.MkdirTemp("", "nexus3-builder-rootfs-*")
	if err != nil {
		return "", fmt.Errorf("builderimage: staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	slog.Info("builderimage: extracting OCI layers")
	if err := extractImageLayers(img, stagingDir); err != nil {
		return "", fmt.Errorf("builderimage: extract layers: %w", err)
	}

	slog.Info("builderimage: adding boot layers")
	if err := addBootLayers(stagingDir, embeddedAgentBytes); err != nil {
		return "", fmt.Errorf("builderimage: boot layers: %w", err)
	}

	slog.Info("builderimage: adding toolchain layers")
	if err := addToolchainLayers(ctx, stagingDir); err != nil {
		return "", fmt.Errorf("builderimage: toolchain layers: %w", err)
	}

	slog.Info("builderimage: building ext4 image", "dest", cachePath)
	if err := buildExt4(ctx, stagingDir, cachePath); err != nil {
		// Remove any partial output so the next call retries cleanly.
		_ = os.Remove(cachePath)
		return "", fmt.Errorf("builderimage: ext4 build: %w", err)
	}

	slog.Info("builderimage: done", "path", cachePath, "digest", digest)
	return cachePath, nil
}

// extractImageLayers extracts all layers of img onto destDir, applying OCI
// whiteout semantics.
func extractImageLayers(img v1.Image, destDir string) error {
	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("list layers: %w", err)
	}
	for i, layer := range layers {
		rc, err := layer.Uncompressed()
		if err != nil {
			return fmt.Errorf("layer %d uncompress: %w", i, err)
		}
		if err := extractTarStream(rc, destDir); err != nil {
			_ = rc.Close()
			return fmt.Errorf("layer %d extract: %w", i, err)
		}
		_ = rc.Close()
	}
	return nil
}

// extractTarStream applies a tar stream onto destDir, handling directories,
// regular files, symlinks, hardlinks, and OCI whiteout entries.
func extractTarStream(r io.Reader, destDir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		base := filepath.Base(hdr.Name)
		// OCI whiteout: .wh.<name> means delete <name>; .wh..wh..opq is opaque.
		if strings.HasPrefix(base, ".wh.") {
			if base == ".wh..wh..opq" {
				continue // opaque whiteout — layers below are hidden; not relevant here
			}
			target := filepath.Join(destDir, filepath.Dir(hdr.Name), strings.TrimPrefix(base, ".wh."))
			_ = os.RemoveAll(target)
			continue
		}

		destPath := filepath.Join(destDir, hdr.Name)
		// Guard against path traversal.
		cleanDest := filepath.Clean(destPath) + string(filepath.Separator)
		cleanRoot := filepath.Clean(destDir) + string(filepath.Separator)
		if cleanDest != cleanRoot && !strings.HasPrefix(cleanDest, cleanRoot) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destPath, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("mkdir %s: %w", destPath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("mkdir parent %s: %w", destPath, err)
			}
			f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create %s: %w", destPath, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return fmt.Errorf("write %s: %w", destPath, err)
			}
			_ = f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("mkdir parent for symlink %s: %w", destPath, err)
			}
			_ = os.Remove(destPath)
			if err := os.Symlink(hdr.Linkname, destPath); err != nil {
				continue // non-fatal: best-effort symlink creation
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(destDir, hdr.Linkname)
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("mkdir parent for hardlink %s: %w", destPath, err)
			}
			_ = os.Remove(destPath)
			if err := os.Link(linkTarget, destPath); err != nil {
				continue // non-fatal
			}
		}
	}
	return nil
}

// buildExt4 converts the filesystem tree in srcDir to a raw ext4 image at
// dstPath using mke2fs. The image is built with deterministic parameters
// (fixed UUID, fixed hash seed, SOURCE_DATE_EPOCH=0) so identical content
// always produces identical bytes.
func buildExt4(ctx context.Context, srcDir, dstPath string) error {
	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		return ErrMke2fsUnavailable
	}

	dataBytes, err := dirSizeBytes(srcDir)
	if err != nil {
		return fmt.Errorf("measure srcDir: %w", err)
	}
	sizeBytes := dataBytes*imageSizeHeadroomFactor + imageMinSizeBytes
	const mib = 1024 * 1024
	sizeBytes = (sizeBytes + mib - 1) &^ (mib - 1)
	if sizeBytes < imageMinSizeBytes {
		sizeBytes = imageMinSizeBytes
	}

	// Pre-allocate a sparse file; mke2fs reads its size automatically.
	f, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create image file: %w", err)
	}
	f.Close()
	if err := os.Truncate(dstPath, sizeBytes); err != nil {
		return fmt.Errorf("truncate image: %w", err)
	}

	cmd := exec.CommandContext(ctx, mke2fsPath,
		"-t", "ext4",
		"-d", srcDir,
		"-U", deterministicUUID,
		"-E", "hash_seed="+deterministicHashSeed,
		dstPath,
	)
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mke2fs: %w\n%s", err, out)
	}
	return nil
}

// dirSizeBytes returns the total byte count of all regular files under dir.
func dirSizeBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
