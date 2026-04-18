package server

import (
	"context"

	"github.com/inizio/nexus/packages/nexus/pkg/daemon"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/server/rpc"
)

func registerDaemonRPCs(r *rpc.Registry, s *Server) {
	rpc.TypedRegister(r, "node.info", func(ctx context.Context, _ struct{}) (*daemon.NodeInfoResult, *rpckit.RPCError) {
		return daemon.HandleNodeInfo(ctx, s.nodeCfg, s.runtimeFactory)
	})
	rpc.TypedRegister(r, "daemon.settings.get", func(ctx context.Context, req daemon.DaemonSettingsGetParams) (*daemon.DaemonSettingsGetResult, *rpckit.RPCError) {
		return daemon.HandleDaemonSettingsGet(ctx, req, s.workspaceMgr.SandboxResourceSettingsRepository())
	})
	rpc.TypedRegister(r, "daemon.settings.update", func(ctx context.Context, req daemon.DaemonSettingsUpdateParams) (*daemon.DaemonSettingsUpdateResult, *rpckit.RPCError) {
		return daemon.HandleDaemonSettingsUpdate(ctx, req, s.workspaceMgr.SandboxResourceSettingsRepository())
	})
}
