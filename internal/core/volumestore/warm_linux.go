//go:build linux

package volumestore

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func reflinkFile(dst, src *os.File) error {
	err := unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
	if err == nil {
		return nil
	}
	switch err {
	case unix.EOPNOTSUPP, unix.EXDEV, unix.EINVAL, unix.ENOTTY, unix.ENOSYS:
		return fmt.Errorf("%w: %v", ErrReflinkUnsupported, err)
	}
	return fmt.Errorf("volumestore: IoctlFileClone: %w", err)
}

func sparseCopyFile(dst, src *os.File) error {
	srcInfo, err := src.Stat()
	if err != nil {
		return fmt.Errorf("volumestore: sparseCopy stat: %w", err)
	}
	srcSize := srcInfo.Size()

	srcFd := int(src.Fd())
	dstFd := int(dst.Fd())

	var off int64
	seekDataSupported := true

	for {
		// Find next data region.
		dataStart, err := unix.Seek(srcFd, off, unix.SEEK_DATA)
		if err != nil {
			if err == unix.ENXIO {
				break // no more data extents
			}
			if err == unix.EINVAL {
				// SEEK_DATA not supported; treat whole file as one extent.
				seekDataSupported = false
				dataStart = 0
			} else {
				return fmt.Errorf("volumestore: SEEK_DATA: %w", err)
			}
		}

		var holeStart int64
		if seekDataSupported {
			holeStart, err = unix.Seek(srcFd, dataStart, unix.SEEK_HOLE)
			if err != nil {
				if err == unix.ENXIO {
					holeStart = srcSize
				} else {
					return fmt.Errorf("volumestore: SEEK_HOLE: %w", err)
				}
			}
		} else {
			holeStart = srcSize
		}

		extentLen := holeStart - dataStart
		if extentLen <= 0 {
			if !seekDataSupported {
				break
			}
			off = holeStart
			continue
		}

		if err := copyExtent(dst, src, dstFd, srcFd, dataStart, extentLen); err != nil {
			return err
		}

		if !seekDataSupported {
			break
		}
		off = holeStart
	}

	return dst.Truncate(srcSize)
}

const copyBufSize = 1 << 20 // 1 MiB

func copyExtent(dst, src *os.File, dstFd, srcFd int, start, length int64) error {
	roff := start
	woff := start
	rem := length

	for rem > 0 {
		n := rem
		if n > copyBufSize {
			n = copyBufSize
		}
		copied, err := unix.CopyFileRange(srcFd, &roff, dstFd, &woff, int(n), 0)
		if err != nil {
			switch err {
			case unix.EXDEV, unix.ENOSYS, unix.EOPNOTSUPP, unix.EINVAL:
				return copyExtentPreadPwrite(dst, src, woff, rem)
			}
			return fmt.Errorf("volumestore: CopyFileRange: %w", err)
		}
		if copied == 0 {
			return fmt.Errorf("volumestore: CopyFileRange returned 0 bytes")
		}
		rem -= int64(copied)
	}
	return nil
}

func copyExtentPreadPwrite(dst, src *os.File, start, length int64) error {
	buf := make([]byte, copyBufSize)
	off := start
	rem := length
	for rem > 0 {
		n := int64(len(buf))
		if n > rem {
			n = rem
		}
		nr, err := src.ReadAt(buf[:n], off)
		if err != nil && err != io.EOF {
			return fmt.Errorf("volumestore: pread: %w", err)
		}
		if nr == 0 {
			break
		}
		if _, err := dst.WriteAt(buf[:nr], off); err != nil {
			return fmt.Errorf("volumestore: pwrite: %w", err)
		}
		off += int64(nr)
		rem -= int64(nr)
	}
	return nil
}
