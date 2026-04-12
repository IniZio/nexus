package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/authrelay"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
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

func HandleAuthRelayMint(_ context.Context, req AuthRelayMintParams, mgr *workspacemgr.Manager, broker *authrelay.Broker) (*AuthRelayMintResult, *rpckit.RPCError) {
	if broker == nil || mgr == nil {
		return nil, rpckit.ErrInternalError
	}

	if req.WorkspaceID == "" || req.Binding == "" {
		return nil, rpckit.ErrInvalidParams
	}

	ws, ok := mgr.Get(req.WorkspaceID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	bindingValue, ok := ws.AuthBinding[req.Binding]
	if !ok || bindingValue == "" {
		return nil, &rpckit.RPCError{Code: rpckit.ErrAuthBindingAbsent.Code, Message: fmt.Sprintf("auth binding not found: %s", req.Binding)}
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	token := broker.Mint(req.WorkspaceID, authrelay.RelayEnv(req.Binding, bindingValue), ttl)

	return &AuthRelayMintResult{Token: token}, nil
}

func HandleAuthRelayRevoke(_ context.Context, req AuthRelayRevokeParams, broker *authrelay.Broker) (*AuthRelayRevokeResult, *rpckit.RPCError) {
	if broker == nil {
		return nil, rpckit.ErrInternalError
	}

	if req.Token == "" {
		return nil, rpckit.ErrInvalidParams
	}

	broker.Revoke(req.Token)
	return &AuthRelayRevokeResult{Revoked: true}, nil
}
