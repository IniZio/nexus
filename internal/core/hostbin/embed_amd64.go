//go:build linux

package hostbin

import "embed"

//go:embed embedded/amd64
var embeddedRaw embed.FS

var embeddedFS = mustSub(embeddedRaw, "embedded/amd64")
