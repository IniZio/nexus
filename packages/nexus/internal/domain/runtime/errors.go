package runtime

import "errors"

var (
	ErrUnsupported      = errors.New("operation not supported by driver")
	ErrDriverNotFound   = errors.New("driver not found")
	ErrSnapshotNotFound = errors.New("snapshot not found")
)
