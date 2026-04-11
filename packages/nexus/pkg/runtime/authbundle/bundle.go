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

func ResolveFromOptions(opts map[string]string) (string, error) {
	if opts == nil {
		return "", nil
	}
	if b := strings.TrimSpace(opts["host_auth_bundle"]); b != "" {
		if _, err := base64.StdEncoding.DecodeString(b); err != nil {
			return "", fmt.Errorf("host_auth_bundle: %w", err)
		}
		return b, nil
	}
	if TruthyOption(opts["use_daemon_host_auth_bundle"]) {
		return BuildFromHome()
	}
	return "", nil
}

func TruthyOption(raw string) bool {
	s := strings.TrimSpace(strings.ToLower(raw))
	return s == "1" || s == "true" || s == "yes" || s == "on"
}

func BuildFromHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", nil
	}

	paths := []string{
		filepath.Join(home, ".config", "opencode"),
		filepath.Join(home, ".config", "codex"),
		filepath.Join(home, ".codex"),
		filepath.Join(home, ".config", "openai"),
		filepath.Join(home, ".claude"),
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	added := 0
	for _, path := range paths {
		if err := addPathToTar(tw, home, path); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return "", err
		}
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			added++
		}
	}

	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}

	if added == 0 || buf.Len() == 0 {
		return "", nil
	}

	const maxBundleBytes = 4 * 1024 * 1024
	if buf.Len() > maxBundleBytes {
		return "", nil
	}

	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func addPathToTar(tw *tar.Writer, rootHome, src string) error {
	_, err := os.Lstat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	return filepath.Walk(src, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if fi == nil {
			return nil
		}

		rel, err := filepath.Rel(rootHome, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = rel

		if fi.Mode()&os.ModeSymlink != 0 {
			linkTarget, lerr := os.Readlink(path)
			if lerr != nil {
				return lerr
			}
			hdr.Linkname = linkTarget
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if !fi.Mode().IsRegular() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(tw, f)
		return err
	})
}
