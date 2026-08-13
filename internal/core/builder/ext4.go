package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// deterministicUUID is a fixed ext4 filesystem UUID used on every build so
// that identical inputs produce identical bytes. A random UUID (the mke2fs
// default) would cause the digest of the same logical content to vary across
// builds, defeating the content-addressability contract.
const deterministicUUID = "00000000-0000-0000-0000-000000000000"

// deterministicHashSeed is passed to mke2fs -E hash_seed to fix the HTree
// directory seed, removing another source of non-determinism.
const deterministicHashSeed = "00000000-0000-0000-0000-000000000000"

// imageSizeHeadroomFactor is the multiplier applied to the source-directory
// size when computing the raw image size. The factor accounts for ext4
// metadata overhead (inode table, journal, block group descriptors) and
// alignment padding. A 2× multiplier is conservative for typical rootfs trees
// (tens of thousands of small files plus a Go toolchain or buildkitd binary).
const imageSizeHeadroomFactor = 2

// imageMinSizeBytes is the minimum raw image size. mke2fs requires enough
// room for at least the superblock, backup descriptors, and an inode table.
// 64 MiB is generous but ensures mke2fs always succeeds even on near-empty
// trees (such as those produced by unit-test fakes).
const imageMinSizeBytes = 64 * 1024 * 1024

// ErrMke2fsUnavailable is returned by [exportAndCache] when mke2fs is not
// found on the host PATH. Test suites should check [Mke2fsAvailable] and
// skip rather than fail when they see this error.
var ErrMke2fsUnavailable = errors.New("mke2fs not found in PATH (install e2fsprogs)")

// Mke2fsAvailable reports whether mke2fs is available on the host PATH.
func Mke2fsAvailable() bool {
	_, err := exec.LookPath("mke2fs")
	return err == nil
}

// exportAndCache converts the filesystem tree in srcDir to a raw ext4 image,
// computes its SHA-256 digest, and stores it in the cache via [image.Cache.Put].
//
// The ext4 image is built with deterministic parameters (fixed UUID, fixed
// hash seed, SOURCE_DATE_EPOCH=0) so that two builds of identical content
// produce identical bytes and therefore the same digest — satisfying the
// content-addressability contract in domain/image.go.
//
// If mke2fs is not available on the host PATH, exportAndCache returns an
// error wrapping [ErrMke2fsUnavailable]. The caller can test with
// [errors.Is](err, ErrMke2fsUnavailable) and skip gracefully.
func exportAndCache(ctx context.Context, srcDir, ref string, kind domain.ImageKind, cache *image.Cache) (domain.Image, error) {
	// Compute source-tree size to right-size the image.
	dataBytes, err := dirSizeBytes(srcDir)
	if err != nil {
		return domain.Image{}, fmt.Errorf("ext4: measure srcDir: %w", err)
	}
	imageSizeBytes := dataBytes*imageSizeHeadroomFactor + imageMinSizeBytes
	// Round up to a 1 MiB boundary.
	const mib = 1024 * 1024
	imageSizeBytes = (imageSizeBytes + mib - 1) &^ (mib - 1)
	if imageSizeBytes < imageMinSizeBytes {
		imageSizeBytes = imageMinSizeBytes
	}

	// Allocate the image file as a sparse file; mke2fs reads the file size
	// and formats it without a separate blocks-count argument.
	tmpDir, err := os.MkdirTemp("", "nexus3-ext4-*")
	if err != nil {
		return domain.Image{}, fmt.Errorf("ext4: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	ext4Path := filepath.Join(tmpDir, "rootfs.img")

	if err := runMke2fs(ctx, srcDir, ext4Path, imageSizeBytes); err != nil {
		return domain.Image{}, fmt.Errorf("ext4: %w", err)
	}

	// Open the image file for hashing and streaming into the cache.
	f, err := os.Open(ext4Path)
	if err != nil {
		return domain.Image{}, fmt.Errorf("ext4: open image: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return domain.Image{}, fmt.Errorf("ext4: stat image: %w", err)
	}

	// Hash the image content to form the cache key.
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return domain.Image{}, fmt.Errorf("ext4: hash image: %w", err)
	}
	digest, err := domain.ParseDigest("sha256:" + hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return domain.Image{}, fmt.Errorf("ext4: parse digest: %w", err)
	}

	img := domain.Image{
		Digest:    digest,
		Ref:       ref,
		Kind:      kind,
		Size:      info.Size(),
		CreatedAt: time.Now().UTC(),
	}

	// Seek back to start; cache.Put streams from the reader while re-hashing
	// to verify integrity. The file is already on a local tmpfs so the
	// double read is fast.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return domain.Image{}, fmt.Errorf("ext4: seek: %w", err)
	}
	if err := cache.Put(ctx, img, f); err != nil {
		return domain.Image{}, fmt.Errorf("ext4: cache put: %w", err)
	}
	return img, nil
}

// runMke2fs creates a raw ext4 image at dstPath from the contents of srcDir.
//
// The image is pre-allocated as a sparse file of sizeBytes, then formatted
// with deterministic parameters:
//   - -U deterministicUUID: fixed filesystem UUID (no random bytes).
//   - -E hash_seed=…,nodiscard,lazy_itable_init=1,lazy_journal_init=1: fixed
//     HTree seed plus sparse-preserving flags (inode table and journal are
//     written lazily so their blocks remain holes in the host file).
//   - SOURCE_DATE_EPOCH=0: forces all timestamps to the Unix epoch.
//
// These constraints together ensure that identical srcDir content always
// produces identical image bytes AND that the on-disk size reflects only the
// actual content, not the full image size.
//
// Host dependency: mke2fs from e2fsprogs. Install with:
//
//	apt-get install e2fsprogs    # Debian/Ubuntu
//	dnf install e2fsprogs        # Fedora/RHEL
func runMke2fs(ctx context.Context, srcDir, dstPath string, sizeBytes int64) error {
	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		return ErrMke2fsUnavailable
	}

	// Pre-allocate a sparse file; mke2fs reads its size automatically.
	if err := os.Truncate(dstPath, sizeBytes); err != nil {
		// File does not exist yet on first call; create it.
		f, cerr := os.Create(dstPath)
		if cerr != nil {
			return fmt.Errorf("create image file: %w", cerr)
		}
		f.Close()
		if err := os.Truncate(dstPath, sizeBytes); err != nil {
			return fmt.Errorf("truncate image file: %w", err)
		}
	}

	// The -E options together ensure the image stays sparse:
	//   nodiscard         — skip FITRIM (a no-op on plain files, but prevents
	//                        mke2fs from writing zeros across the device when it
	//                        issues discard I/Os to simulate trim).
	//   lazy_itable_init=1 — defer inode-table zero-fill to a background pass;
	//                        unwritten table blocks remain as holes in the file.
	//   lazy_journal_init=1 — defer journal zero-fill likewise.
	// Without these flags mke2fs eagerly writes the inode table and journal
	// across the full image, destroying the sparse holes and inflating on-disk
	// usage to nearly the full image size (e.g. 17 MiB vs 364 KiB for a 256 MiB
	// image with ~100 KiB of content — a 47× waste).
	extOpts := "hash_seed=" + deterministicHashSeed + ",nodiscard,lazy_itable_init=1,lazy_journal_init=1"
	cmd := exec.CommandContext(ctx, mke2fsPath,
		"-t", "ext4",
		"-d", srcDir,
		"-U", deterministicUUID,
		"-E", extOpts,
		dstPath,
	)
	// SOURCE_DATE_EPOCH=0 instructs mke2fs to use the Unix epoch for all
	// timestamps embedded in the superblock, making the output reproducible.
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mke2fs failed: %w\n%s", err, out)
	}
	return nil
}

// dirSizeBytes returns the total byte count of all regular files under dir.
// Symlinks, devices, and other special files are not counted.
func dirSizeBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
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
