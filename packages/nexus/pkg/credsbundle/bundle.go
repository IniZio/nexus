package credsbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/inizio/nexus/packages/nexus/pkg/agentprofile"
)

const maxBundledFileBytes = 512 * 1024

func Build() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", nil
	}
	return BuildFromHome(home)
}

func BuildFromHome(home string) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	added := 0
	for _, cf := range agentprofile.AllCredFiles() {
		src := filepath.Join(home, cf)
		if fi, statErr := os.Lstat(src); statErr == nil && fi.IsDir() {
			continue
		}
		if err := addToTar(tw, home, src); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return "", err
		}
		if _, statErr := os.Stat(src); statErr == nil {
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

	const maxBundleBytes = 8 * 1024 * 1024
	if buf.Len() > maxBundleBytes {
		return "", nil
	}

	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func addToTar(tw *tar.Writer, rootHome, src string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	rel, err := filepath.Rel(rootHome, src)
	if err != nil {
		return err
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(src)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		targetInfo, err := os.Stat(resolved)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if targetInfo.IsDir() {
			return addDirToTar(tw, resolved, rel)
		}
		return addFileToTar(tw, rel, resolved, targetInfo)
	}

	if fi.IsDir() {
		return addDirToTar(tw, src, rel)
	}

	return addFileToTar(tw, rel, src, fi)
}

func addDirToTar(tw *tar.Writer, src, relBase string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if info.IsDir() && filepath.Base(path) == "node_modules" {
			return filepath.SkipDir
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return nil
			}
			targetInfo, err := os.Stat(resolved)
			if err != nil {
				return nil
			}
			relPath, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			targetRel := filepath.ToSlash(filepath.Join(relBase, relPath))
			if targetInfo.IsDir() {
				return nil
			}
			if targetInfo.Size() > maxBundledFileBytes {
				return nil
			}
			return addFileToTar(tw, targetRel, resolved, targetInfo)
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetRel := filepath.ToSlash(filepath.Join(relBase, relPath))
		if !info.IsDir() && info.Size() > maxBundledFileBytes {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = targetRel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

func addFileToTar(tw *tar.Writer, rel, src string, fi os.FileInfo) error {
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
	f, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	if err != nil {
		return fmt.Errorf("copy %s to tar: %w", rel, err)
	}
	return nil
}
