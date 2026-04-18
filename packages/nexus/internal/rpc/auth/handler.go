// Package auth provides JSON-RPC handlers for auth relay operations.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/inizio/nexus/packages/nexus/internal/creds/relay"
	"github.com/inizio/nexus/packages/nexus/internal/domain/workspace"
	rpce "github.com/inizio/nexus/packages/nexus/internal/rpc/errors"
	"github.com/inizio/nexus/packages/nexus/internal/rpc/registry"
)

// WorkspaceGetter fetches workspace entities by ID.
type WorkspaceGetter interface {
	Get(ctx context.Context, id string) (*workspace.Workspace, error)
}

// Handler exposes authrelay.* RPC methods.
type Handler struct {
	wsRepo WorkspaceGetter
	broker *relay.Broker
}

// New creates a Handler backed by the given workspace getter and relay broker.
func New(wsRepo WorkspaceGetter, broker *relay.Broker) *Handler {
	return &Handler{wsRepo: wsRepo, broker: broker}
}

// Register wires all authrelay.* methods into reg.
func (h *Handler) Register(reg registry.Registry) {
	reg.Register("authrelay.mint", h.mint)
	reg.Register("authrelay.revoke", h.revoke)
}

// ── DTOs ─────────────────────────────────────────────────────────────────────

type mintReq struct {
	WorkspaceID string `json:"workspaceId"`
	Binding     string `json:"binding"`
	TTLSeconds  int    `json:"ttlSeconds,omitempty"`
}

type mintRes struct {
	Token string `json:"token"`
}

type revokeReq struct {
	Token string `json:"token"`
}

type revokeRes struct {
	Revoked bool `json:"revoked"`
}

// ── helpers ──────────────────────────────────────────────────────────────────

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

// ── handlers ─────────────────────────────────────────────────────────────────

func (h *Handler) mint(ctx context.Context, raw json.RawMessage) (any, error) {
	req, err := decode[mintReq](raw)
	if err != nil {
		return nil, err
	}
	if req.WorkspaceID == "" {
		return nil, rpce.InvalidParams("workspaceId is required")
	}
	if req.Binding == "" {
		return nil, rpce.InvalidParams("binding is required")
	}
	ws, err := h.wsRepo.Get(ctx, req.WorkspaceID)
	if err != nil {
		return nil, rpce.NotFound("workspace not found")
	}
	bindingValue, ok := ws.AuthBinding[req.Binding]
	if !ok || bindingValue == "" {
		return nil, rpce.NotFound(fmt.Sprintf("auth binding not found: %s", req.Binding))
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	token := h.broker.Mint(req.WorkspaceID, relay.RelayEnv(req.Binding, bindingValue), ttl)
	return &mintRes{Token: token}, nil
}

func (h *Handler) revoke(_ context.Context, raw json.RawMessage) (any, error) {
	req, err := decode[revokeReq](raw)
	if err != nil {
		return nil, err
	}
	if req.Token == "" {
		return nil, rpce.InvalidParams("token is required")
	}
	h.broker.Revoke(req.Token)
	return &revokeRes{Revoked: true}, nil
}
