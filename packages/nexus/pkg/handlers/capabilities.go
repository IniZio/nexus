package handlers

import (
	"context"
	"encoding/json"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

type CapabilitiesListResult struct {
	Capabilities []runtime.Capability `json:"capabilities"`
}

func HandleCapabilitiesList(_ context.Context, _ json.RawMessage, factory *runtime.Factory) (*CapabilitiesListResult, *rpckit.RPCError) {
	if factory == nil {
		return &CapabilitiesListResult{Capabilities: []runtime.Capability{}}, nil
	}
	return &CapabilitiesListResult{Capabilities: factory.Capabilities()}, nil
}
