package image

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus/internal/openshell"
)

const overlayDefaultSizeMiB int64 = 256

func buildOverlay(ctx context.Context, dir string, bundle openshell.BootstrapBundle, extra map[string][]byte, mkfsPath string, sizeMiB int64) (string, error) {
	staging, err := os.MkdirTemp(dir, "overlay-staging-*")
	if err != nil {
		return "", fmt.Errorf("overlay: create staging: %w", err)
	}
	defer os.RemoveAll(staging)

	for _, sub := range []string{"upper", "work"} {
		if err := os.MkdirAll(filepath.Join(staging, sub), 0o755); err != nil {
			return "", fmt.Errorf("overlay: mkdir %s: %w", sub, err)
		}
	}

	writeFile := func(guestPath string, data []byte, mode os.FileMode) error {
		rel := strings.TrimPrefix(guestPath, "/")
		dst := filepath.Join(staging, "upper", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("overlay: mkdir parent %s: %w", dst, err)
		}
		return os.WriteFile(dst, data, mode)
	}

	for guestPath, data := range bundle.Files() {
		if err := writeFile(guestPath, data, 0o600); err != nil {
			return "", fmt.Errorf("overlay: write bundle file %s: %w", guestPath, err)
		}
	}

	for guestPath, data := range extra {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(guestPath, ".sh") {
			mode = 0o755
		}
		if err := writeFile(guestPath, data, mode); err != nil {
			return "", fmt.Errorf("overlay: write extra file %s: %w", guestPath, err)
		}
	}

	outPath := filepath.Join(dir, "overlay.ext4")
	if err := buildExt4(ctx, staging, outPath, mkfsPath, sizeMiB); err != nil {
		return "", fmt.Errorf("overlay: build ext4: %w", err)
	}
	return outPath, nil
}
