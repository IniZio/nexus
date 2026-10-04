//go:build !amd64

package embedded

import (
	"embed"
	"io/fs"

	"github.com/IniZio/nexus/internal/core/hostbin"
)

// Non-amd64 hosts also embed the linux/amd64 agent: Fly sprites are amd64 and
// the sprites broker uploads the agent relay into them.
//
//go:embed agent-amd64
var rawAgentAMD64 embed.FS

func init() {
	sub, err := fs.Sub(rawAgentAMD64, "agent-amd64")
	if err != nil {
		panic(err)
	}
	hostbin.RegisterEmbeddedAgent("amd64", sub)
}
