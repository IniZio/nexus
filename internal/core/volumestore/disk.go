package volumestore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/IniZio/nexus/internal/core/hostbin"
)

// ErrMke2fsUnavailable is returned when mke2fs cannot be resolved.
var ErrMke2fsUnavailable = fmt.Errorf("volumestore: mke2fs unavailable")

// preallocateFile creates (or truncates) the file at path to size bytes as a
// sparse file (no disk blocks allocated until data is written).
func preallocateFile(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

// mke2fsArgs returns the mke2fs argv for an empty ext4 image.  prezeroed
// uses assume_storage_prezeroed=1 (e2fsprogs >= 1.47.0): mke2fs skips zeroing
// and the guest kernel skips ext4lazyinit.  It is only valid because
// preallocateFile hands formatExt4 a freshly truncated, all-zero sparse file.
func mke2fsArgs(path string, prezeroed bool) []string {
	opts := "lazy_itable_init=0,lazy_journal_init=0"
	if prezeroed {
		opts = "assume_storage_prezeroed=1"
	}
	return []string{"-t", "ext4", "-F", "-E", opts, path}
}

// runMke2fs is swapped in tests.
var runMke2fs = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).CombinedOutput()
}

// formatExt4 formats the file at path as an empty ext4 filesystem using
// mke2fs.  path MUST be a new all-zero file (see preallocateFile).  Falls back
// to eager init when mke2fs rejects assume_storage_prezeroed (older e2fsprogs).
func formatExt4(ctx context.Context, path string) error {
	mke2fsPath, err := hostbin.Resolve(ctx, hostbin.Mke2fs)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMke2fsUnavailable, err)
	}
	out, err := runMke2fs(ctx, mke2fsPath, mke2fsArgs(path, true)...)
	if err != nil && ctx.Err() == nil {
		out, err = runMke2fs(ctx, mke2fsPath, mke2fsArgs(path, false)...)
	}
	if err != nil {
		return fmt.Errorf("mke2fs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
