package builder

import (
	"context"
	"fmt"
)

// ContextToDisk packs the contents of contextDir into a raw ext4 image at
// outExt4, suitable for attaching to a builder VM as /dev/vdb (the context
// disk). The image is sized with the same headroom factor as the rootfs
// builder so mke2fs always has room for metadata.
//
// The caller is responsible for choosing the output path (e.g. a temporary
// file in a work directory); ContextToDisk creates or overwrites that path.
//
// Returns [ErrMke2fsUnavailable] if mke2fs is not on the host PATH.
func ContextToDisk(ctx context.Context, contextDir string, outExt4 string) error {
	dataBytes, err := dirSizeBytes(contextDir)
	if err != nil {
		return fmt.Errorf("contextdisk: measure context dir: %w", err)
	}

	imageSizeBytes := dataBytes*imageSizeHeadroomFactor + imageMinSizeBytes
	const mib = 1024 * 1024
	imageSizeBytes = (imageSizeBytes + mib - 1) &^ (mib - 1)
	if imageSizeBytes < imageMinSizeBytes {
		imageSizeBytes = imageMinSizeBytes
	}

	if err := runMke2fs(ctx, contextDir, outExt4, imageSizeBytes); err != nil {
		return fmt.Errorf("contextdisk: pack ext4: %w", err)
	}
	return nil
}
