//go:build !linux

package volumestore

import (
	"context"
	"fmt"
	"io"
	"os"
)

func allocatedFileBytes(f *os.File) (int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func reflinkFile(dst, src *os.File) error {
	// Darwin clonefile operates on paths, not fds; out of scope here.
	return fmt.Errorf("%w: not implemented on this platform", ErrReflinkUnsupported)
}

func sparseCopyFile(ctx context.Context, dst, src *os.File) error {
	srcInfo, err := src.Stat()
	if err != nil {
		return fmt.Errorf("volumestore: sparseCopy stat: %w", err)
	}
	srcSize := srcInfo.Size()

	buf := make([]byte, 1<<20)
	var off int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		nr, err := src.ReadAt(buf, off)
		if nr > 0 {
			chunk := buf[:nr]
			if !allZero(chunk) {
				if _, werr := dst.WriteAt(chunk, off); werr != nil {
					return fmt.Errorf("volumestore: sparseCopy write: %w", werr)
				}
			}
			off += int64(nr)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("volumestore: sparseCopy read: %w", err)
		}
	}

	return dst.Truncate(srcSize)
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
