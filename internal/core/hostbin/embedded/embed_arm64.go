//go:build linux

package embedded

import (
	"embed"
	"io/fs"

	"github.com/IniZio/nexus/internal/core/hostbin"
)

//go:embed arm64
var raw embed.FS

func init() {
	sub, err := fs.Sub(raw, "arm64")
	if err != nil {
		panic(err)
	}
	hostbin.RegisterEmbedded(sub)
	hostbin.RegisterEmbeddedAgent("arm64", sub)
}
