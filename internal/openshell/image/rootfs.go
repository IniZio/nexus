package image

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	imageSizeHeadroomFactor = 3
	imageMinSizeBytes       = 256 * 1024 * 1024
)

var errMkfsUnavailable = errors.New("image: mkfs.ext4 / mke2fs not found in PATH")

var defaultExtractor ociExtractor = func(ctx context.Context, ref string, destDir string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parse ref %s: %w", ref, err)
	}
	img, err := remote.Image(parsed, remote.WithContext(ctx), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}))
	if err != nil {
		return "", fmt.Errorf("pull %s: %w", ref, err)
	}
	hash, err := img.Digest()
	if err != nil {
		return "", fmt.Errorf("digest %s: %w", ref, err)
	}
	if err := extractImageLayers(img, destDir); err != nil {
		return "", err
	}
	return "sha256:" + hash.Hex, nil
}

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
		if strings.HasPrefix(base, ".wh.") {
			if base == ".wh..wh..opq" {
				continue
			}
			target := filepath.Join(destDir, filepath.Dir(hdr.Name), strings.TrimPrefix(base, ".wh."))
			_ = os.RemoveAll(target)
			continue
		}

		destPath := filepath.Join(destDir, hdr.Name)
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
				continue
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(destDir, hdr.Linkname)
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("mkdir parent for hardlink %s: %w", destPath, err)
			}
			_ = os.Remove(destPath)
			if err := os.Link(linkTarget, destPath); err != nil {
				continue
			}
		}
	}
	return nil
}

func buildExt4(ctx context.Context, srcDir, dstPath, mkfsPath string, fixedSizeMiB int64) error {
	bin := mkfsPath
	if bin == "" {
		var err error
		bin, err = exec.LookPath("mkfs.ext4")
		if err != nil {
			bin, err = exec.LookPath("mke2fs")
			if err != nil {
				return errMkfsUnavailable
			}
		}
	}

	var sizeBytes int64
	if fixedSizeMiB > 0 {
		sizeBytes = fixedSizeMiB * 1024 * 1024
	} else {
		dataBytes, err := dirSizeBytes(srcDir)
		if err != nil {
			return fmt.Errorf("measure srcDir: %w", err)
		}
		sizeBytes = dataBytes*imageSizeHeadroomFactor + imageMinSizeBytes
		const mib = 1024 * 1024
		sizeBytes = (sizeBytes + mib - 1) &^ (mib - 1)
		if sizeBytes < imageMinSizeBytes {
			sizeBytes = imageMinSizeBytes
		}
	}

	f, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create image file: %w", err)
	}
	f.Close()
	if err := os.Truncate(dstPath, sizeBytes); err != nil {
		return fmt.Errorf("truncate image: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, "-t", "ext4", "-d", srcDir, dstPath)
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mke2fs: %w\n%s", err, out)
	}
	return nil
}

func dirSizeBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
