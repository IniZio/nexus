//go:build linux && (amd64 || arm64)

package hostbin

import "io/fs"

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
