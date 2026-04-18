package workspace

import (
	"context"
	"encoding/json"

	appsvc "github.com/inizio/nexus/packages/nexus/internal/app/workspace"
	rpce "github.com/inizio/nexus/packages/nexus/internal/rpc/errors"
	"github.com/inizio/nexus/packages/nexus/internal/rpc/registry"
)

// Handler holds the workspace app service and registers all workspace RPC methods.
type Handler struct {
	svc *appsvc.Service
}

// NewHandler constructs a Handler backed by the given Service.
func NewHandler(svc *appsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// Register wires all workspace.* methods into reg.
func (h *Handler) Register(reg registry.Registry) {
	reg.Register("workspace.info", h.wrap(h.handleInfo))
	reg.Register("workspace.create", h.wrap(h.handleCreate))
	reg.Register("workspace.list", h.wrap(h.handleList))
	reg.Register("workspace.relations.list", h.wrap(h.handleRelations))
	reg.Register("workspace.remove", h.wrap(h.handleRemove))
	reg.Register("workspace.stop", h.wrap(h.handleStop))
	reg.Register("workspace.start", h.wrap(h.handleStart))
	reg.Register("workspace.restore", h.wrap(h.handleRestore))
	reg.Register("workspace.fork", h.wrap(h.handleFork))
	reg.Register("workspace.checkout", h.wrap(h.handleCheckout))
	reg.Register("workspace.setLocalWorktree", h.wrap(h.handleSetLocalWorktree))
	reg.Register("workspace.ready", h.wrap(h.handleReady))
	reg.Register("workspace.ports.list", h.wrap(h.handlePortsList))
	reg.Register("workspace.ports.add", h.wrap(h.handlePortsAdd))
	reg.Register("workspace.ports.remove", h.wrap(h.handlePortsRemove))
	reg.Register("workspace.tunnels.start", h.wrap(h.handleTunnelsStart))
	reg.Register("workspace.tunnels.stop", h.wrap(h.handleTunnelsStop))
}

func (h *Handler) wrap(fn func(ctx context.Context, raw json.RawMessage) (any, error)) registry.HandlerFunc {
	return fn
}

func decode[Req any](raw json.RawMessage) (Req, error) {
	var req Req
	norm := raw
	if len(norm) == 0 || string(norm) == "null" {
		norm = []byte("{}")
	}
	if err := json.Unmarshal(norm, &req); err != nil {
		return req, rpce.InvalidParams("invalid params: " + err.Error())
	}
	return req, nil
}
