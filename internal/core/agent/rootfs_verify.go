package agent

import (
	"fmt"
	"io/fs"
	"path/filepath"
)

// rootfsVerifyMinFiles is the minimum number of regular files a rootfs must
// contain before the zero-length ratio check applies. Substantial base images
// (debian/ubuntu + apt installs) always carry thousands of regular files, so a
// low floor here catches every realistic corruption while leaving genuinely
// tiny bases (scratch/busybox micro-images) unguarded rather than risking a
// false positive on them.
const rootfsVerifyMinFiles = 200

// rootfsVerifyMaxZeroPct is the maximum tolerated percentage of zero-length
// regular files. A healthy Linux rootfs has some legitimately empty files
// (lock placeholders, empty configs, package markers) but they are a small
// minority. The observed corruption signature is ~99.96% zero-length
// (5021 / 5023). 90% sits far above any legitimate rootfs yet far below the
// corruption signature, giving a wide, false-positive-free margin.
const rootfsVerifyMaxZeroPct = 90

// ErrRootfsHollow reports that an exported rootfs tree is almost entirely
// zero-length regular files — the signature of a build export that produced
// directory structure and inodes but lost file DATA. Booting such an image
// fails at runtime with "exec format error" because every binary is empty.
//
// This is a fail-closed integrity guard: nexus3 previously cached and booted
// such images because nothing verified file contents between the buildkit
// export and the ext4 conversion. Converting the silent corruption into a hard,
// retryable build error is the confirmed-defect fix; the intermittent
// export-side trigger inside buildkit is a separate, still-open investigation.
type ErrRootfsHollow struct {
	TotalFiles int
	ZeroFiles  int
}

func (e *ErrRootfsHollow) Error() string {
	pct := 0
	if e.TotalFiles > 0 {
		pct = e.ZeroFiles * 100 / e.TotalFiles
	}
	return fmt.Sprintf(
		"rootfs export is hollow: %d/%d regular files (%d%%) are zero-length — "+
			"the build lost file contents (booting this image would fail with "+
			"'exec format error'); retry the build",
		e.ZeroFiles, e.TotalFiles, pct,
	)
}

// verifyRootfsPopulated walks the exported rootfs at dir and fails when the
// tree is hollow — the overwhelming majority of regular files are zero-length.
//
// It exists because the in-guest build path (buildInGuestImageLinux) fed the
// buildkit export directory straight into mke2fs with no content check: an
// export that produced correct directory structure but empty regular-file
// contents (observed 5021/5023 files zero-length) was silently converted to an
// ext4 image, cached, and booted, only failing later at exec time. This guard
// makes that corruption a hard build error at the point it is detectable and
// cheap to retry.
//
// The check is deliberately conservative (see the threshold constants) so it
// only ever fires on unambiguous corruption, never on a legitimately small or
// sparse image.
func verifyRootfsPopulated(dir string) error {
	var total, zero int
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total++
		if info.Size() == 0 {
			zero++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("verify rootfs: walk %s: %w", dir, err)
	}

	// Too few regular files to judge — leave tiny/minimal images unguarded
	// rather than risk a false positive.
	if total < rootfsVerifyMinFiles {
		return nil
	}
	if zero*100 >= rootfsVerifyMaxZeroPct*total {
		return &ErrRootfsHollow{TotalFiles: total, ZeroFiles: zero}
	}
	return nil
}
