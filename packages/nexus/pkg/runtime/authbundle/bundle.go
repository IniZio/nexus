package authbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxBundledFileBytes = 512 * 1024

func ResolveFromOptions(opts map[string]string) (string, error) {
	if opts == nil {
		return "", nil
	}
	if b := strings.TrimSpace(opts["host_auth_bundle"]); b != "" {
		decoded, err := base64.StdEncoding.DecodeString(b)
		if err != nil {
			return "", fmt.Errorf("host_auth_bundle: %w", err)
		}
		const maxBundleBytes = 4 * 1024 * 1024
		if len(decoded) > maxBundleBytes {
			return "", fmt.Errorf("host_auth_bundle: exceeds maximum size of %d bytes", maxBundleBytes)
		}
		return b, nil
	}
	return "", nil
}

func BuildFromHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", nil
	}

	paths := []string{
		filepath.Join(home, ".config", "opencode"),
		filepath.Join(home, ".local", "share", "opencode"),
		filepath.Join(home, ".config", "codex"),
		filepath.Join(home, ".codex"),
		filepath.Join(home, ".config", "openai"),
		filepath.Join(home, ".config", "github-copilot"),
		filepath.Join(home, ".claude"),
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	var fileCount int
	for _, path := range paths {
		n, err := addFilteredTreeToTar(tw, home, path)
		if err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return "", err
		}
		fileCount += n
	}

	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}

	if fileCount == 0 || buf.Len() == 0 {
		return "", nil
	}

	const maxBundleBytes = 4 * 1024 * 1024
	if buf.Len() > maxBundleBytes {
		return "", nil
	}

	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func includeBundledFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.Contains(rel, "..") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(rel))
	base := filepath.Base(rel)

	switch {
	case strings.HasPrefix(rel, ".config/opencode/"):
		return ext == ".json" || ext == ".yaml" || ext == ".yml"
	case strings.HasPrefix(rel, ".local/share/opencode/"):
		return ext == ".json" || ext == ".yaml" || ext == ".yml"
	case strings.HasPrefix(rel, ".config/codex/"):
		return ext == ".json" || ext == ".yaml" || ext == ".yml"
	case strings.HasPrefix(rel, ".codex/"):
		return ext == ".json" || ext == ".yaml" || ext == ".yml"
	case strings.HasPrefix(rel, ".config/openai/"):
		return ext == ".json" || ext == ".yaml" || ext == ".yml"
	case strings.HasPrefix(rel, ".config/github-copilot/"):
		return ext == ".json" || ext == ".yaml" || ext == ".yml"
	case strings.HasPrefix(rel, ".claude/"):
		if strings.Contains(rel, "/projects/") {
			return false
		}
		if strings.EqualFold(base, "claude.md") {
			return true
		}
		return ext == ".json" || ext == ".yaml" || ext == ".yml"
	default:
		return false
	}
}

func addFilteredTreeToTar(tw *tar.Writer, home, src string) (int, error) {
	_, err := os.Lstat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	var count int
	err = filepath.Walk(src, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fi == nil {
			return nil
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !fi.Mode().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !includeBundledFile(rel) {
			return nil
		}
		if fi.Size() > maxBundledFileBytes {
			return nil
		}

		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = rel

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}
