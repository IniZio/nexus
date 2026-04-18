package server

import (
	"context"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

func registerFsRPCs(r *rpc.Registry, s *Server) {
	rpc.TypedRegister(r, "fs.readFile", func(ctx context.Context, req workspace.ReadFileParams) (*workspace.ReadFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleReadFile(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.writeFile", func(ctx context.Context, req workspace.WriteFileParams) (*workspace.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleWriteFile(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.exists", func(ctx context.Context, req workspace.ExistsParams) (*workspace.ExistsResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleExists(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.readdir", func(ctx context.Context, req workspace.ReaddirParams) (*workspace.ReaddirResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleReaddir(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.mkdir", func(ctx context.Context, req workspace.MkdirParams) (*workspace.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleMkdir(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.rm", func(ctx context.Context, req workspace.RmParams) (*workspace.WriteFileResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleRm(ctx, req, ws)
	})
	rpc.TypedRegister(r, "fs.stat", func(ctx context.Context, req workspace.StatParams) (*workspace.StatResult, *rpckit.RPCError) {
		ws := s.resolveWorkspaceTyped(req)
		return workspace.HandleStat(ctx, req, ws)
	})
}
