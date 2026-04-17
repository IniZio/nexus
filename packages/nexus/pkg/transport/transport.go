package transport

import (
	"context"
	"encoding/json"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
)

// Handler is the raw RPC handler signature.
type Handler func(ctx context.Context, msgID string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError)

// Registry maps RPC method names to handler functions.
type Registry interface {
	Register(method string, h Handler)
	Dispatch(ctx context.Context, method, msgID string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError)
	RegisteredMethods() []string
}

// Transport is the interface for a communication transport.
type Transport interface {
	Name() string
	Serve(reg Registry, deps *Deps) error
	Close() error
}

// Deps is the dependency container passed to transports.
// Concrete types are kept as interface{} where the packages have unresolved
// build issues (cycles, missing handlers), to keep this package buildable.
type Deps struct {
	WorkspaceMgr   interface{} // *workspacemgr.Manager
	ProjectMgr     interface{} // *project.Manager
	RuntimeFactory interface{} // *runtime.Factory
	SpotlightMgr   interface{} // *spotlight.Manager
	ServiceMgr     interface{} // *services.Manager
	AuthRelay      interface{} // *authrelay.Broker
	NodeCfg        interface{} // *config.NodeConfig
}
