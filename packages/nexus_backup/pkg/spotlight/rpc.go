package spotlight

import (
	"context"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
)

type SpotlightExposeParams struct {
	Spec ExposeSpec `json:"spec"`
}

type SpotlightListParams struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type SpotlightCloseParams struct {
	ID string `json:"id"`
}

type SpotlightExposeResult struct {
	Forward *Forward `json:"forward"`
}

type SpotlightListResult struct {
	Forwards []*Forward `json:"forwards"`
}

type SpotlightCloseResult struct {
	Closed bool `json:"closed"`
}

func HandleSpotlightExpose(ctx context.Context, p SpotlightExposeParams, mgr *Manager) (*SpotlightExposeResult, *rpckit.RPCError) {
	fwd, err := mgr.Expose(ctx, p.Spec)
	if err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	return &SpotlightExposeResult{Forward: fwd}, nil
}

func HandleSpotlightList(_ context.Context, p SpotlightListParams, mgr *Manager) (*SpotlightListResult, *rpckit.RPCError) {
	all := mgr.List(p.WorkspaceID)
	return &SpotlightListResult{Forwards: all}, nil
}

func HandleSpotlightClose(_ context.Context, p SpotlightCloseParams, mgr *Manager) (*SpotlightCloseResult, *rpckit.RPCError) {
	closed := mgr.Close(p.ID)
	if !closed {
		return nil, rpckit.ErrInvalidParams
	}

	return &SpotlightCloseResult{Closed: true}, nil
}
