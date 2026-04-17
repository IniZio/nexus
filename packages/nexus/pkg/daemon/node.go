package daemon

import (
	"context"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

type NodeInfoResult struct {
	Node          config.NodeIdentity      `json:"node"`
	Capabilities  []runtime.Capability     `json:"capabilities"`
	Compatibility config.NodeCompatibility `json:"compatibility"`
}

func HandleNodeInfo(_ context.Context, nodeCfg *config.NodeConfig, factory *runtime.Factory) (*NodeInfoResult, *rpckit.RPCError) {
	result := &NodeInfoResult{
		Capabilities: []runtime.Capability{},
	}

	if nodeCfg != nil {
		result.Node = nodeCfg.Node
		result.Compatibility = nodeCfg.Compatibility
	}

	if factory != nil {
		result.Capabilities = factory.Capabilities()
	}

	return result, nil
}
