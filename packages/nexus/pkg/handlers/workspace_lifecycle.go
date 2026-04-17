package handlers

import (
	"context"
	"strings"
	"time"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func HandleWorkspaceList(_ context.Context, _ WorkspaceListParams, mgr *workspacemgr.Manager) (*WorkspaceListResult, *rpckit.RPCError) {
	all := mgr.List()
	if len(all) == 0 {
		return &WorkspaceListResult{Workspaces: all}, nil
	}
	parts := make([]string, 0, len(all))
	for _, ws := range all {
		if ws == nil {
			continue
		}
		enrichWorkspaceRuntimeLabel(ws)
		parts = append(parts, ws.ID+":\""+ws.WorkspaceName+":"+ws.RuntimeLabel+"\"")
	}
	return &WorkspaceListResult{Workspaces: all}, nil
}

func HandleWorkspaceOpen(_ context.Context, req WorkspaceOpenParams, mgr *workspacemgr.Manager) (*WorkspaceOpenResult, *rpckit.RPCError) {
	ws, ok := mgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	enrichWorkspaceRuntimeLabel(ws)
	return &WorkspaceOpenResult{Workspace: ws}, nil
}

func HandleWorkspaceRemove(ctx context.Context, req WorkspaceRemoveParams, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceRemoveResult, *rpckit.RPCError) {
	ws, ok := mgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	if req.DeleteHostPath && strings.TrimSpace(ws.ProjectID) != "" && strings.TrimSpace(ws.ParentWorkspaceID) == "" {
		return nil, &rpckit.RPCError{
			Code:    rpckit.ErrInvalidParams.Code,
			Message: "cannot delete host path for project root sandbox",
		}
	}

	if factory != nil && strings.TrimSpace(ws.Backend) != "" {
		if driver, selErr := selectDriverForWorkspaceBackend(factory, ws.Backend); selErr == nil {
			destroyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if destroyErr := driver.Destroy(destroyCtx, req.ID); destroyErr != nil {
			}
		}
	}

	removed, removeErr := mgr.RemoveWithOptions(req.ID, workspacemgr.RemoveOptions{DeleteHostPath: req.DeleteHostPath})
	if removeErr != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: removeErr.Error()}
	}
	if !removed {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &WorkspaceRemoveResult{Removed: true}, nil
}

func HandleWorkspaceStop(_ context.Context, req WorkspaceStopParams, mgr *workspacemgr.Manager) (*WorkspaceStopResult, *rpckit.RPCError) {
	if err := mgr.Stop(req.ID); err != nil {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	return &WorkspaceStopResult{Stopped: true}, nil
}

func HandleWorkspaceStopWithRuntime(ctx context.Context, req WorkspaceStopParams, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceStopResult, *rpckit.RPCError) {
	ws, ok := mgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	if factory != nil {
		if rpcErr := suspendRuntimeWorkspace(ctx, ws, factory, mgr); rpcErr != nil {
			return nil, rpcErr
		}
	}
	if err := mgr.Stop(req.ID); err != nil {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	return &WorkspaceStopResult{Stopped: true}, nil
}

func HandleWorkspaceStart(ctx context.Context, req WorkspaceStartParams, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceStartResult, *rpckit.RPCError) {
	ws, ok := mgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	if factory != nil {
		if rpcErr := resumeRuntimeWorkspace(ctx, ws, factory, mgr); rpcErr != nil {
			return nil, rpcErr
		}
	}
	if err := mgr.Start(req.ID); err != nil {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	ws, ok = mgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	enrichWorkspaceRuntimeLabel(ws)
	return &WorkspaceStartResult{Workspace: ws}, nil
}

func HandleWorkspaceRestore(ctx context.Context, req WorkspaceRestoreParams, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceRestoreResult, *rpckit.RPCError) {
	ws, ok := mgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	var selectedDriver runtime.Driver
	var requiredBackends []string

	if factory != nil {
		explicitBackend := normalizeWorkspaceBackend(strings.TrimSpace(ws.Backend))
		if explicitBackend != "" {
			if driver, exists := factory.DriverForBackend(explicitBackend); exists {
				selectedDriver = driver
				requiredBackends = []string{explicitBackend}
			} else {
				return &WorkspaceRestoreResult{}, &rpckit.RPCError{
					Code:    rpckit.ErrInternalError.Code,
					Message: "backend selection failed: driver not registered for workspace backend %q",
				}
			}
		} else {
			driver, exists := factory.DriverForBackend("firecracker")
			if !exists {
				return &WorkspaceRestoreResult{}, &rpckit.RPCError{
					Code:    rpckit.ErrInternalError.Code,
					Message: "firecracker driver not registered",
				}
			}
			selectedDriver = driver
			requiredBackends = []string{"firecracker"}
		}
	}

	ws, ok = mgr.Restore(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	resolvedBackend := ws.Backend
	if selectedDriver != nil {
		if resolvedBackend != "" {
			allowed := false
			for _, b := range requiredBackends {
				if b == resolvedBackend {
					allowed = true
					break
				}
			}
			if !allowed {
				resolvedBackend = selectedDriver.Backend()
			}
		} else {
			resolvedBackend = selectedDriver.Backend()
		}
	}

	if resolvedBackend != ws.Backend {
		if err := mgr.SetBackend(req.ID, resolvedBackend); err != nil {
			return &WorkspaceRestoreResult{}, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "backend persist failed"}
		}
		updated, ok := mgr.Get(req.ID)
		if !ok {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		ws = updated
	}

	if factory != nil {
		if rpcErr := resumeRuntimeWorkspace(ctx, ws, factory, mgr); rpcErr != nil {
			return nil, rpcErr
		}
	}

	enrichWorkspaceRuntimeLabel(ws)
	return &WorkspaceRestoreResult{Restored: true, Workspace: ws}, nil
}
