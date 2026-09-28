//go:build linux

package hostbin

import "embed"

//go:embed embedded/arm64
var embeddedRaw embed.FS

var embeddedFS = mustSub(embeddedRaw, "embedded/arm64")
