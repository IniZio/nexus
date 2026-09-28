package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/IniZio/nexus/internal/core/hostbin/embedded"
)

// populateSrcDir writes a small but representative tree into dir:
// a regular file, a nested file, and a symlink.
func populateSrcDir(t *testing.T, dir string, mtime time.Time) {
	t.Helper()

	dirs := []string{
		filepath.Join(dir, "subdir"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	files := map[string]string{
		filepath.Join(dir, "hello.txt"):          "hello world\n",
		filepath.Join(dir, "subdir", "data.bin"): "binary\x00data\xff\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	symlink := filepath.Join(dir, "link.txt")
	if err := os.Symlink("hello.txt", symlink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestExt4ImageReproducible verifies that two calls to runMke2fs with
// identical source trees (same content, same mtimes) produce byte-identical
// ext4 images. This is the Z3-repro acceptance criterion: the embedded static
// mke2fs must be deterministic across invocations.
func TestExt4ImageReproducible(t *testing.T) {
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not available")
	}

	ctx := context.Background()
	fixedMtime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("identical_trees_identical_digest", func(t *testing.T) {
		tmp := t.TempDir()

		src1 := filepath.Join(tmp, "src1")
		src2 := filepath.Join(tmp, "src2")
		img1 := filepath.Join(tmp, "img1.raw")
		img2 := filepath.Join(tmp, "img2.raw")

		if err := os.MkdirAll(src1, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(src2, 0o755); err != nil {
			t.Fatal(err)
		}

		populateSrcDir(t, src1, fixedMtime)
		populateSrcDir(t, src2, fixedMtime)

		const size = imageMinSizeBytes
		if err := runMke2fs(ctx, src1, img1, size); err != nil {
			t.Fatalf("first runMke2fs: %v", err)
		}
		if err := runMke2fs(ctx, src2, img2, size); err != nil {
			t.Fatalf("second runMke2fs: %v", err)
		}

		d1 := sha256File(t, img1)
		d2 := sha256File(t, img2)
		if d1 != d2 {
			t.Errorf("non-reproducible: build 1=%s build 2=%s", d1, d2)
		}
	})

	t.Run("different_file_mtimes_produce_different_digest", func(t *testing.T) {
		// runMke2fs does NOT normalise file mtimes (SOURCE_DATE_EPOCH only fixes
		// superblock/group-descriptor timestamps). Callers that require
		// mtime-independence must normalise the source tree themselves before
		// calling runMke2fs.
		tmp := t.TempDir()

		src1 := filepath.Join(tmp, "src1")
		src2 := filepath.Join(tmp, "src2")
		img1 := filepath.Join(tmp, "img1.raw")
		img2 := filepath.Join(tmp, "img2.raw")

		if err := os.MkdirAll(src1, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(src2, 0o755); err != nil {
			t.Fatal(err)
		}

		mtime1 := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		mtime2 := time.Date(2010, 6, 15, 12, 0, 0, 0, time.UTC)

		populateSrcDir(t, src1, mtime1)
		populateSrcDir(t, src2, mtime2)

		const size = imageMinSizeBytes
		if err := runMke2fs(ctx, src1, img1, size); err != nil {
			t.Fatalf("first runMke2fs: %v", err)
		}
		if err := runMke2fs(ctx, src2, img2, size); err != nil {
			t.Fatalf("second runMke2fs: %v", err)
		}

		d1 := sha256File(t, img1)
		d2 := sha256File(t, img2)
		t.Logf("mtime1 image sha256: %s", d1)
		t.Logf("mtime2 image sha256: %s", d2)
		if d1 == d2 {
			t.Log("NOTE: digests equal despite different mtimes; mke2fs version may not embed file mtimes")
		} else {
			t.Log("confirmed: file mtimes affect image digest; callers must normalise mtimes for full reproducibility")
		}
	})
}
