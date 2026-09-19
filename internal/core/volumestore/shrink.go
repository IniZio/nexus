package volumestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrE2fsckUnavailable = fmt.Errorf("volumestore: e2fsck not found on PATH (install e2fsprogs)")
var ErrResize2fsUnavailable = fmt.Errorf("volumestore: resize2fs not found on PATH (install e2fsprogs)")

// reclaimTimeout bounds reclaimExt4, decoupled from a caller's short ctx.
const reclaimTimeout = 5 * time.Minute

func ShrinkToolsAvailable() bool {
	_, e2fsckErr := exec.LookPath("e2fsck")
	_, resize2fsErr := exec.LookPath("resize2fs")
	return e2fsckErr == nil && resize2fsErr == nil
}

func Mke2fsAvailable() bool {
	_, err := exec.LookPath("mke2fs")
	return err == nil
}

var resize2fsBlockLenRE = regexp.MustCompile(`(\d+) \((\d+)k\) blocks long`)

// shrinkExt4ToMinimum runs e2fsck -fy then resize2fs -M against the raw ext4
// image at path, then truncates the file to the resulting minimum size.
// Idempotent; safe to call back to back. Returns the new file size in bytes.
func shrinkExt4ToMinimum(ctx context.Context, path string) (int64, error) {
	e2fsckPath, err := exec.LookPath("e2fsck")
	if err != nil {
		return 0, ErrE2fsckUnavailable
	}
	resize2fsPath, err := exec.LookPath("resize2fs")
	if err != nil {
		return 0, ErrResize2fsUnavailable
	}

	if err := runE2fsckAt(ctx, e2fsckPath, path); err != nil {
		return 0, err
	}

	resizeCmd := exec.CommandContext(ctx, resize2fsPath, "-M", path)
	out, err := resizeCmd.CombinedOutput()
	if err != nil {
		_ = runE2fsckAt(ctx, e2fsckPath, path)
		return 0, fmt.Errorf("resize2fs -M %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}

	newSize, err := parseResize2fsMinSize(string(out))
	if err != nil {
		_ = runE2fsckAt(ctx, e2fsckPath, path)
		return 0, fmt.Errorf("resize2fs -M %s: parse output: %w (output: %s)", path, err, strings.TrimSpace(string(out)))
	}

	if err := os.Truncate(path, newSize); err != nil {
		return 0, fmt.Errorf("truncate %s to %d bytes: %w", path, newSize, err)
	}
	return newSize, nil
}

// reclaimExt4 frees host disk blocks while preserving declaredSizeBytes as
// logical capacity: compact via shrinkExt4ToMinimum, truncate back up
// (sparse, zero host cost), grow to fill; repairs via e2fsck on grow failure.
func reclaimExt4(ctx context.Context, path string, declaredSizeBytes int64) error {
	if _, err := shrinkExt4ToMinimum(ctx, path); err != nil {
		return err
	}
	if err := os.Truncate(path, declaredSizeBytes); err != nil {
		return fmt.Errorf("truncate %s to %d bytes: %w", path, declaredSizeBytes, err)
	}
	if err := runResize2fs(ctx, path); err != nil {
		_ = runE2fsckClean(ctx, path)
		return fmt.Errorf("grow %s back to declared size: %w", path, err)
	}
	return nil
}

func runResize2fs(ctx context.Context, path string) error {
	resize2fsPath, err := exec.LookPath("resize2fs")
	if err != nil {
		return ErrResize2fsUnavailable
	}
	cmd := exec.CommandContext(ctx, resize2fsPath, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resize2fs %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runE2fsckClean(ctx context.Context, path string) error {
	e2fsckPath, err := exec.LookPath("e2fsck")
	if err != nil {
		return ErrE2fsckUnavailable
	}
	return runE2fsckAt(ctx, e2fsckPath, path)
}

// runE2fsckAt runs e2fsck -fy at an already-resolved path; exit code 1 ("errors corrected") is success.
func runE2fsckAt(ctx context.Context, e2fsckPath, path string) error {
	cmd := exec.CommandContext(ctx, e2fsckPath, "-fy", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() > 1 {
			return fmt.Errorf("e2fsck -fy %s: %w: %s", path, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func parseResize2fsMinSize(out string) (int64, error) {
	matches := resize2fsBlockLenRE.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return 0, fmt.Errorf("no block-count line found")
	}
	last := matches[len(matches)-1]
	blocks, err := strconv.ParseInt(last[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse block count %q: %w", last[1], err)
	}
	blockKiB, err := strconv.ParseInt(last[2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse block size %q: %w", last[2], err)
	}
	return blocks * blockKiB * 1024, nil
}
