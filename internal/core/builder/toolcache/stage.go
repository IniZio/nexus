package toolcache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func validateGuestPath(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("toolcache: path %q must be absolute", p)
	}
	clean := filepath.Clean(p)
	if clean != p {
		return fmt.Errorf("toolcache: path %q must be clean", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return fmt.Errorf("toolcache: path %q contains ..", p)
		}
	}
	return nil
}

func safeMkdirAll(root, guestDir string) error {
	parts := strings.Split(strings.TrimPrefix(guestDir, "/"), "/")
	cur := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err2 := os.Mkdir(next, 0755); err2 != nil {
				return err2
			}
			cur = next
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("toolcache: unsafe path: %q is a symlink", next)
		}
		if !fi.IsDir() {
			return fmt.Errorf("toolcache: unsafe path: %q is not a directory", next)
		}
		cur = next
	}
	return nil
}

func resolvePath(root, guestPath string, hops int) (string, error) {
	if hops > 40 {
		return "", fmt.Errorf("toolcache: symlink loop (>40 hops) resolving %q", guestPath)
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(guestPath), "/"), "/")
	cur := root
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if cur == root {
				continue
			}
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				remaining := filepath.Join(parts[i+1:]...)
				if remaining != "" {
					return "", os.ErrNotExist
				}
				return next, os.ErrNotExist
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(next)
			if err != nil {
				return "", err
			}
			if filepath.IsAbs(target) {
				target = filepath.Join(root, target)
			} else {
				target = filepath.Join(cur, target)
			}
			if !strings.HasPrefix(target+"/", root+"/") {
				target = root
			}
			remaining := "/" + strings.Join(parts[i+1:], "/")
			resolved, err := resolvePath(root, strings.TrimPrefix(target, root)+remaining, hops+1)
			return resolved, err
		}
		cur = next
	}
	return cur, nil
}

func guestPathExists(root, guestPath string) bool {
	resolved, err := resolvePath(root, guestPath, 0)
	if err != nil {
		return false
	}
	_, err = os.Lstat(resolved)
	return err == nil
}

func StageTree(root string, tools []Fetched, skipIfPresent bool) error {
	for _, t := range tools {
		if err := validateGuestPath(t.GuestBinPath); err != nil {
			return err
		}
		if err := validateGuestPath(t.LinkPath); err != nil {
			return err
		}

		if skipIfPresent {
			if guestPathExists(root, t.LinkPath) ||
				guestPathExists(root, "/usr/bin/"+t.Name) ||
				guestPathExists(root, "/bin/"+t.Name) {
				continue
			}
		}

		binDir := filepath.Dir(t.GuestBinPath)
		if err := safeMkdirAll(root, binDir); err != nil {
			return err
		}

		destBin := filepath.Join(root, t.GuestBinPath)
		if fi, err := os.Lstat(destBin); err == nil {
			if fi.IsDir() {
				return fmt.Errorf("toolcache: destination %q is a directory", destBin)
			}
			if err2 := os.Remove(destBin); err2 != nil {
				return err2
			}
		}

		if err := copyFile(t.BinPath, destBin, 0755); err != nil {
			return err
		}

		linkDir := filepath.Dir(t.LinkPath)
		if err := safeMkdirAll(root, linkDir); err != nil {
			return err
		}

		rel, err := filepath.Rel(linkDir, t.GuestBinPath)
		if err != nil {
			return err
		}

		destLink := filepath.Join(root, t.LinkPath)
		if _, err := os.Lstat(destLink); err == nil {
			if err2 := os.Remove(destLink); err2 != nil {
				return err2
			}
		}
		if err := os.Symlink(rel, destLink); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func CopyTree(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("toolcache: CopyTree src %q is not a directory", src)
	}

	if err := os.MkdirAll(dst, 0755); err != nil {
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
			if err := copyFile(srcPath, dstPath, info.Mode()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("toolcache: CopyTree: unsupported file type at %q", srcPath)
		}
	}
	return nil
}
