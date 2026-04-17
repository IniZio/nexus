package handlers

import (
	"context"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
)

type SpotlightExposeParams struct {
	Spec spotlight.ExposeSpec `json:"spec"`
}

type SpotlightListParams struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type SpotlightCloseParams struct {
	ID string `json:"id"`
}

type SpotlightExposeResult struct {
	Forward *spotlight.Forward `json:"forward"`
}

type SpotlightListResult struct {
	Forwards []*spotlight.Forward `json:"forwards"`
}

type SpotlightCloseResult struct {
	Closed bool `json:"closed"`
}

func HandleSpotlightExpose(ctx context.Context, p SpotlightExposeParams, mgr *spotlight.Manager) (*SpotlightExposeResult, *rpckit.RPCError) {
	fwd, err := mgr.Expose(ctx, p.Spec)
	if err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	return &SpotlightExposeResult{Forward: fwd}, nil
}

func HandleSpotlightList(_ context.Context, p SpotlightListParams, mgr *spotlight.Manager) (*SpotlightListResult, *rpckit.RPCError) {
	all := mgr.List(p.WorkspaceID)
	return &SpotlightListResult{Forwards: all}, nil
}

func HandleSpotlightClose(_ context.Context, p SpotlightCloseParams, mgr *spotlight.Manager) (*SpotlightCloseResult, *rpckit.RPCError) {
	closed := mgr.Close(p.ID)
	if !closed {
		return nil, rpckit.ErrInvalidParams
	}

	return &SpotlightCloseResult{Closed: true}, nil
}
