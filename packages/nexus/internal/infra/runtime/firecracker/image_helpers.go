package firecracker

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func createWorkspaceImage(projectRoot, imagePath string) error {
	if strings.TrimSpace(projectRoot) == "" {
		return fmt.Errorf("project root is required for workspace image")
	}

	projectSizeBytes, err := directorySizeBytes(projectRoot)
	if err != nil {
		return fmt.Errorf("compute project size: %w", err)
	}

	fd, err := os.OpenFile(imagePath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create workspace image: %w", err)
	}
	if err := fd.Truncate(workspaceImageSizeBytes(projectSizeBytes)); err != nil {
		_ = fd.Close()
		return fmt.Errorf("truncate workspace image: %w", err)
	}
	if err := fd.Close(); err != nil {
		return fmt.Errorf("close workspace image: %w", err)
	}

	cmd := exec.Command("mkfs.ext4", "-F", "-d", projectRoot, imagePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkfs.ext4 workspace image: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func directorySizeBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func workspaceImageSizeBytes(projectSizeBytes int64) int64 {
	const (
		miB        = int64(1024 * 1024)
		giB        = 1024 * miB
		minSize    = 2 * giB
		overhead   = 2 * giB
		maxInitial = 20 * giB
	)

	target := projectSizeBytes*2 + overhead
	if target < minSize {
		target = minSize
	}
	if target > maxInitial {
		target = maxInitial
	}
	if rem := target % miB; rem != 0 {
		target += miB - rem
	}
	return target
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
