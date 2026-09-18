package builder

import (
	"os"
	"path/filepath"
)

// forceIncludeContainerfile hardlinks (or copies) <src>/.nexus/Containerfile into <staging>/.nexus/; no-op if absent.
func forceIncludeContainerfile(src, staging string) error {
	srcFile := filepath.Join(src, containerfilePath)
	fi, err := os.Lstat(srcFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	dstDir := filepath.Join(staging, ".nexus")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(staging, containerfilePath)
	// dst is usually already a hardlink to srcFile; copying over it would O_TRUNC the shared inode.
	if dfi, err := os.Lstat(dst); err == nil {
		if os.SameFile(fi, dfi) || dfi.Size() > 0 {
			return nil
		}
	}
	if err := os.Link(srcFile, dst); err == nil {
		return nil
	}
	de, err := os.ReadDir(filepath.Join(src, ".nexus"))
	if err != nil {
		return err
	}
	for _, e := range de {
		if e.Name() == "Containerfile" {
			return copyFileWithMode(srcFile, dst, e)
		}
	}
	return nil
}
