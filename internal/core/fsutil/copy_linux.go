package fsutil

import (
	"os"

	"golang.org/x/sys/unix"
)

func ficlone(dst, src *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}
