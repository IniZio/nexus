package volumestore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrMke2fsUnavailable is returned when mke2fs is not on PATH.
var ErrMke2fsUnavailable = fmt.Errorf("volumestore: mke2fs not found on PATH (install e2fsprogs)")

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

// formatExt4 formats the file at path as an empty ext4 filesystem using
// mke2fs.  Mirrors the pattern used in internal/cli/shadowdisk.go.
func formatExt4(ctx context.Context, path string) error {
	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		return ErrMke2fsUnavailable
	}
	cmd := exec.CommandContext(ctx, mke2fsPath,
		"-t", "ext4",
		"-F",                                            // force (no interactive confirmation)
		"-E", "lazy_itable_init=0,lazy_journal_init=0",  // fully initialise inode table
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mke2fs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
