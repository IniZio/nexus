//go:build !linux

package fsutil

import (
	"os"

	"golang.org/x/sys/unix"
)

// ficlone reports EOPNOTSUPP so copyFile uses the sparse-copy fallback.
func ficlone(dst, src *os.File) error {
	return unix.EOPNOTSUPP
}
