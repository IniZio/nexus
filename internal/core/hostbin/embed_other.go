//go:build !linux || !(amd64 || arm64)

package hostbin

import (
	"io/fs"
	"testing/fstest"
)

var embeddedFS fs.FS = fstest.MapFS{}
