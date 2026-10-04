//go:build linux

package embedded

import (
	"embed"
	"io/fs"

	"github.com/IniZio/nexus/internal/core/hostbin"
)

//go:embed amd64
var raw embed.FS

func init() {
	sub, err := fs.Sub(raw, "amd64")
	if err != nil {
		panic(err)
	}
	hostbin.RegisterEmbedded(sub)
	hostbin.RegisterEmbeddedAgent("amd64", sub)
}
