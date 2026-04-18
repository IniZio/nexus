package server

import (
	"context"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
)

func registerSpotlightRPCs(r *rpc.Registry, s *Server) {
	rpc.TypedRegister(r, "spotlight.start", func(ctx context.Context, req spotlight.SpotlightExposeParams) (*spotlight.SpotlightExposeResult, *rpckit.RPCError) {
		return spotlight.HandleSpotlightExpose(ctx, req, s.spotlightMgr)
	})
	rpc.TypedRegister(r, "spotlight.list", func(ctx context.Context, req spotlight.SpotlightListParams) (*spotlight.SpotlightListResult, *rpckit.RPCError) {
		return spotlight.HandleSpotlightList(ctx, req, s.spotlightMgr)
	})
	rpc.TypedRegister(r, "spotlight.close", func(ctx context.Context, req spotlight.SpotlightCloseParams) (*spotlight.SpotlightCloseResult, *rpckit.RPCError) {
		return spotlight.HandleSpotlightClose(ctx, req, s.spotlightMgr)
	})
}
