// Package fsutil holds in-process file helpers so core paths need no host
// coreutils.
package fsutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const chunkSize = 1 << 20

var zeroChunk = make([]byte, chunkSize)

// cloneFunc clones src into dst by extent sharing.
type cloneFunc func(dst, src *os.File) error

func ficlone(dst, src *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}

// CopyFileReflink copies src to a newly created dst. It tries a FICLONE
// extent clone first and falls back to a sparse copy that keeps source holes
// and skips all-zero blocks. dst must not exist. The source mode is kept.
func CopyFileReflink(src, dst string) error {
	return copyFile(src, dst, ficlone)
}

func copyFile(src, dst string, clone cloneFunc) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("copy %s: not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()

	cerr := clone(out, in)
	if cerr != nil {
		if !errors.Is(cerr, unix.EOPNOTSUPP) && !errors.Is(cerr, unix.EXDEV) &&
			!errors.Is(cerr, unix.EINVAL) && !errors.Is(cerr, unix.ENOTTY) {
			return fmt.Errorf("clone %s: %w", src, cerr)
		}
		if err := sparseCopy(out, in, st.Size()); err != nil {
			return err
		}
	}
	if err := out.Chmod(st.Mode().Perm()); err != nil {
		return err
	}
	return out.Sync()
}

func sparseCopy(out, in *os.File, size int64) error {
	if err := out.Truncate(size); err != nil {
		return err
	}
	buf := make([]byte, chunkSize)
	var pos int64
	for pos < size {
		data, err := in.Seek(pos, unix.SEEK_DATA)
		if err != nil {
			if errors.Is(err, unix.ENXIO) {
				break
			}
			return err
		}
		hole, err := in.Seek(data, unix.SEEK_HOLE)
		if err != nil {
			return err
		}
		if hole > size {
			hole = size
		}
		for off := data; off < hole; {
			n := int64(len(buf))
			if hole-off < n {
				n = hole - off
			}
			m, rerr := in.ReadAt(buf[:n], off)
			if m > 0 && !bytes.Equal(buf[:m], zeroChunk[:m]) {
				if _, werr := out.WriteAt(buf[:m], off); werr != nil {
					return werr
				}
			}
			off += int64(m)
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					break
				}
				return rerr
			}
		}
		pos = hole
	}
	return nil
}
