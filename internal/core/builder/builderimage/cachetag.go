package builderimage

import (
	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/image"
)

// CacheTag returns the image-cache AgentTag for an OCI image baked with
// agentBytes and tools. When len(tools)==0 the result is identical to
// image.BuilderAgentTag(agentBytes) so that existing cached images remain
// valid without a re-bake. When tools are supplied the tag is extended with
// "+tools-" + toolcache.Digest(tools) so that a change in the tool set
// forces a re-bake.
//
// AgentTag is stored as a plain JSON string in image metadata and is compared
// verbatim; there are no format constraints on OCI-image records (the 16-hex
// constraint only applies to builder-template filenames). The "+tools-" suffix
// is therefore safe.
func CacheTag(agentBytes []byte, tools []toolcache.Fetched) string {
	base := image.BuilderAgentTag(agentBytes)
	if len(tools) == 0 {
		return base
	}
	return base + "+tools-" + toolcache.Digest(tools)
}
