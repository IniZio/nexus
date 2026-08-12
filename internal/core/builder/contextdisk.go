package builder

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// ContextToDisk packs the contents of contextDir into a raw ext4 image at
// outExt4, suitable for attaching to a builder VM as /dev/vdb (the context
// disk). The image is sized with the same headroom factor as the rootfs
// builder so mke2fs always has room for metadata.
//
// If <contextDir>/.dockerignore exists, its patterns are honoured: excluded
// paths are neither counted toward the image size nor copied into the ext4.
// Docker's standard ignore semantics apply (globs, directory prefixes, leading
// "!" negation).
//
// The caller is responsible for choosing the output path (e.g. a temporary
// file in a work directory); ContextToDisk creates or overwrites that path.
//
// Returns [ErrMke2fsUnavailable] if mke2fs is not on the host PATH.
func ContextToDisk(ctx context.Context, contextDir string, outExt4 string) error {
	pm, err := loadDockerIgnore(contextDir)
	if err != nil {
		return fmt.Errorf("contextdisk: load .dockerignore: %w", err)
	}

	// When a .dockerignore is present, build a filtered view of the context
	// directory so that both the size measurement and the mke2fs invocation
	// see only the included files. Files are hardlinked (not copied) so the
	// intermediate tree is cheap even for large repositories.
	packDir := contextDir
	if pm != nil {
		filtered, cleanup, err := filteredContextDir(contextDir, pm)
		if err != nil {
			return fmt.Errorf("contextdisk: filter context: %w", err)
		}
		defer cleanup()
		packDir = filtered
	}

	dataBytes, err := dirSizeBytes(packDir)
	if err != nil {
		return fmt.Errorf("contextdisk: measure context dir: %w", err)
	}

	imageSizeBytes := dataBytes*imageSizeHeadroomFactor + imageMinSizeBytes
	const mib = 1024 * 1024
	imageSizeBytes = (imageSizeBytes + mib - 1) &^ (mib - 1)
	if imageSizeBytes < imageMinSizeBytes {
		imageSizeBytes = imageMinSizeBytes
	}

	if err := runMke2fs(ctx, packDir, outExt4, imageSizeBytes); err != nil {
		return fmt.Errorf("contextdisk: pack ext4: %w", err)
	}
	return nil
}

// loadDockerIgnore reads <contextDir>/.dockerignore and returns a compiled
// PatternMatcher. Returns nil, nil if the file does not exist or contains no
// patterns.
func loadDockerIgnore(contextDir string) (*patternmatcher.PatternMatcher, error) {
	f, err := os.Open(filepath.Join(contextDir, ".dockerignore"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	patterns, err := ignorefile.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return nil, nil
	}
	return patternmatcher.New(patterns)
}

// filteredContextDir creates a temporary directory that contains only the
// files from src that are not excluded by pm. Regular files are hardlinked
// (not copied) so the intermediate tree is cheap even for large repos.
// The caller must invoke the returned cleanup func when done.
func filteredContextDir(src string, pm *patternmatcher.PatternMatcher) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "nexus3-ctx-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// MatchesOrParentMatches returns true when the entry itself or any
		// ancestor directory is excluded. For directories this means we can
		// skip the entire subtree with filepath.SkipDir.
		excluded, err := pm.MatchesOrParentMatches(rel)
		if err != nil {
			return fmt.Errorf("patternmatcher: %w", err)
		}
		if excluded {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		dst := filepath.Join(tmpDir, rel)
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		// Hardlink regular files; fall back to copy on cross-device error.
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.Link(path, dst); err != nil {
			if err := copyFileWithMode(path, dst, d); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return tmpDir, cleanup, nil
}

// copyFileWithMode copies src to dst preserving the file mode; used as a
// cross-device fallback when os.Link fails in filteredContextDir.
func copyFileWithMode(src, dst string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
