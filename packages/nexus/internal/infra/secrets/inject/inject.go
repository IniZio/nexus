package inject

import (
	"context"
	"fmt"
	"time"

	"github.com/inizio/nexus/packages/nexus/internal/creds/relay"
	rpcerrors "github.com/inizio/nexus/packages/nexus/internal/rpc/errors"
)

// WorkspaceAuthLookup is satisfied by any store/service that can return an
// auth binding value for a workspace.
// TODO: replace with domain/workspace.Repository once available in app layer.
type WorkspaceAuthLookup interface {
	GetAuthBinding(ctx context.Context, workspaceID, binding string) (string, error)
}

// AuthRelayMintParams holds parameters for auth.relay.mint.
type AuthRelayMintParams struct {
	WorkspaceID string `json:"workspaceId"`
	Binding     string `json:"binding"`
	TTLSeconds  int    `json:"ttlSeconds,omitempty"`
}

// AuthRelayMintResult is returned by auth.relay.mint.
type AuthRelayMintResult struct {
	Token string `json:"token"`
}

// AuthRelayRevokeParams holds parameters for auth.relay.revoke.
type AuthRelayRevokeParams struct {
	Token string `json:"token"`
}

// AuthRelayRevokeResult is returned by auth.relay.revoke.
type AuthRelayRevokeResult struct {
	Revoked bool `json:"revoked"`
}

// HandleAuthRelayMint mints a short-lived relay token for a workspace binding.
func HandleAuthRelayMint(_ context.Context, req AuthRelayMintParams, lookup WorkspaceAuthLookup, broker *relay.Broker) (*AuthRelayMintResult, *rpcerrors.RPCError) {
	if broker == nil || lookup == nil {
		return nil, rpcerrors.Internal("auth relay not configured")
	}

	if req.WorkspaceID == "" || req.Binding == "" {
		return nil, rpcerrors.InvalidParams("workspaceId and binding are required")
	}

	bindingValue, err := lookup.GetAuthBinding(context.Background(), req.WorkspaceID, req.Binding)
	if err != nil {
		return nil, &rpcerrors.RPCError{Code: 404, Message: fmt.Sprintf("auth binding not found: %s", req.Binding)}
	}
	if bindingValue == "" {
		return nil, &rpcerrors.RPCError{Code: 404, Message: fmt.Sprintf("auth binding empty: %s", req.Binding)}
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	token := broker.Mint(req.WorkspaceID, relay.RelayEnv(req.Binding, bindingValue), ttl)

	return &AuthRelayMintResult{Token: token}, nil
}

// HandleAuthRelayRevoke revokes an existing relay token.
func HandleAuthRelayRevoke(_ context.Context, req AuthRelayRevokeParams, broker *relay.Broker) (*AuthRelayRevokeResult, *rpcerrors.RPCError) {
	if broker == nil {
		return nil, rpcerrors.Internal("auth relay not configured")
	}

	if req.Token == "" {
		return nil, rpcerrors.InvalidParams("token is required")
	}

	broker.Revoke(req.Token)
	return &AuthRelayRevokeResult{Revoked: true}, nil
}
