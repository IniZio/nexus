package credsbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"

	"github.com/inizio/nexus/packages/nexus/pkg/agentprofile"
)

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

	if fi.IsDir() {
		return filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			rel2, err2 := filepath.Rel(rootHome, path)
			if err2 != nil {
				return err2
			}
			hdr, err2 := tar.FileInfoHeader(info, "")
			if err2 != nil {
				return err2
			}
			hdr.Name = rel2
			if err2 := tw.WriteHeader(hdr); err2 != nil {
				return err2
			}
			if info.IsDir() {
				return nil
			}
			f, err2 := os.Open(path)
			if err2 != nil {
				if os.IsNotExist(err2) {
					return nil
				}
				return err2
			}
			defer f.Close()
			_, err2 = io.Copy(tw, f)
			return err2
		})
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
	return err
}
