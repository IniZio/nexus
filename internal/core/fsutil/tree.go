package fsutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CopyFile copies the regular file src to dst, creating dst's parent
// directories. An existing dst is truncated in place. The source permission
// bits are applied at create time (subject to umask). No reflink, no fsync.
func CopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if special := info.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky); special != 0 {
		return os.Chmod(dst, info.Mode().Perm()|special)
	}
	return nil
}

// CopyTree recursively copies the directory src into dst. Symlinks are
// recreated verbatim, never followed. Sockets, devices and other special
// files are an error.
func CopyTree(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("fsutil: CopyTree src %q is not a directory", src)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		info, err := e.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
		case info.IsDir():
			if err := CopyTree(srcPath, dstPath); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := CopyFile(srcPath, dstPath); err != nil {
				return err
			}
		default:
			return fmt.Errorf("fsutil: CopyTree: unsupported file type at %q", srcPath)
		}
	}
	return nil
}
