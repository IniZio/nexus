package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/authrelay"
	rpckit "github.com/nexus/nexus/packages/workspace-daemon/pkg/rpcerrors"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/workspacemgr"
)

type AuthRelayMintParams struct {
	WorkspaceID string `json:"workspaceId"`
	Binding     string `json:"binding"`
	TTLSeconds  int    `json:"ttlSeconds,omitempty"`
}

type AuthRelayMintResult struct {
	Token string `json:"token"`
}

type AuthRelayRevokeParams struct {
	Token string `json:"token"`
}

type AuthRelayRevokeResult struct {
	Revoked bool `json:"revoked"`
}

func HandleAuthRelayMint(_ context.Context, params json.RawMessage, mgr *workspacemgr.Manager, broker *authrelay.Broker) (*AuthRelayMintResult, *rpckit.RPCError) {
	if broker == nil || mgr == nil {
		return nil, rpckit.ErrInternalError
	}

	var p AuthRelayMintParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}
	if p.WorkspaceID == "" || p.Binding == "" {
		return nil, rpckit.ErrInvalidParams
	}

	ws, ok := mgr.Get(p.WorkspaceID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	bindingValue, ok := ws.AuthBinding[p.Binding]
	if !ok || bindingValue == "" {
		return nil, &rpckit.RPCError{Code: rpckit.ErrAuthBindingAbsent.Code, Message: fmt.Sprintf("auth binding not found: %s", p.Binding)}
	}

	ttl := time.Duration(p.TTLSeconds) * time.Second
	token := broker.Mint(p.WorkspaceID, map[string]string{
		"NEXUS_AUTH_BINDING": p.Binding,
		"NEXUS_AUTH_VALUE":   bindingValue,
	}, ttl)

	return &AuthRelayMintResult{Token: token}, nil
}

func HandleAuthRelayRevoke(_ context.Context, params json.RawMessage, broker *authrelay.Broker) (*AuthRelayRevokeResult, *rpckit.RPCError) {
	if broker == nil {
		return nil, rpckit.ErrInternalError
	}

	var p AuthRelayRevokeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}
	if p.Token == "" {
		return nil, rpckit.ErrInvalidParams
	}

	broker.Revoke(p.Token)
	return &AuthRelayRevokeResult{Revoked: true}, nil
}
