// Package daemon provides JSON-RPC handlers for daemon info and settings.
package daemon

import (
	"context"
	"encoding/json"

	rpce "github.com/inizio/nexus/packages/nexus/internal/rpc/errors"
	"github.com/inizio/nexus/packages/nexus/internal/rpc/registry"
)

// NodeInfoProvider supplies node identity and capability data.
type NodeInfoProvider interface {
	NodeName() string
	NodeTags() []string
	Capabilities() []Capability
}

// Capability describes a daemon capability.
type Capability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// Handler exposes daemon.* and node.* RPC methods.
type Handler struct {
	node NodeInfoProvider
}

// New creates a Handler backed by the given NodeInfoProvider.
// node may be nil, in which case node.info returns empty data.
func New(node NodeInfoProvider) *Handler {
	return &Handler{node: node}
}

// Register wires all handled methods into reg.
func (h *Handler) Register(reg registry.Registry) {
	reg.Register("node.info", h.nodeInfo)
}

// ── DTOs ─────────────────────────────────────────────────────────────────────

type nodeIdentity struct {
	Name string   `json:"name,omitempty"`
	Tags []string `json:"tags,omitempty"`
}

type nodeInfoResult struct {
	Node         nodeIdentity `json:"node"`
	Capabilities []Capability `json:"capabilities"`
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (h *Handler) nodeInfo(_ context.Context, raw json.RawMessage) (any, error) {
	res := &nodeInfoResult{
		Capabilities: []Capability{},
	}
	if h.node != nil {
		res.Node = nodeIdentity{
			Name: h.node.NodeName(),
			Tags: h.node.NodeTags(),
		}
		res.Capabilities = h.node.Capabilities()
		if res.Capabilities == nil {
			res.Capabilities = []Capability{}
		}
	}
	return res, nil
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	norm := raw
	if len(norm) == 0 || string(norm) == "null" {
		norm = []byte("{}")
	}
	if err := json.Unmarshal(norm, &v); err != nil {
		return v, rpce.InvalidParams("invalid params: " + err.Error())
	}
	return v, nil
}
