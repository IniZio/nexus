package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace/create"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type WorkspaceCreateParams struct {
	Spec workspacemgr.CreateSpec `json:"spec"`
}

type WorkspaceOpenParams struct {
	ID string `json:"id"`
}

type WorkspaceListParams struct {
	AgentProfile string `json:"agentProfile,omitempty"`
}

type WorkspaceRemoveParams struct {
	ID string `json:"id"`
}

type WorkspaceStopParams struct {
	ID string `json:"id"`
}

type WorkspaceStartParams struct {
	ID string `json:"id"`
}

type WorkspaceRestoreParams struct {
	ID string `json:"id"`
}

type WorkspacePauseParams struct {
	ID string `json:"id"`
}

type WorkspaceResumeParams struct {
	ID string `json:"id"`
}

type WorkspaceForkParams struct {
	ID                 string `json:"id"`
	ChildWorkspaceName string `json:"childWorkspaceName,omitempty"`
	ChildRef           string `json:"childRef,omitempty"`
}

type WorkspaceCreateResult struct {
	Workspace *workspacemgr.Workspace `json:"workspace"`
}

type WorkspaceOpenResult struct {
	Workspace *workspacemgr.Workspace `json:"workspace"`
}

type WorkspaceListResult struct {
	Workspaces []*workspacemgr.Workspace `json:"workspaces"`
}

type WorkspaceRemoveResult struct {
	Removed bool `json:"removed"`
}

type WorkspaceStopResult struct {
	Stopped bool `json:"stopped"`
}

type WorkspaceStartResult struct {
	Started bool `json:"started"`
}

type WorkspaceRestoreResult struct {
	Restored  bool                    `json:"restored"`
	Workspace *workspacemgr.Workspace `json:"workspace,omitempty"`
}

type WorkspacePauseResult struct {
	Paused bool `json:"paused"`
}

type WorkspaceResumeResult struct {
	Resumed bool `json:"resumed"`
}

type WorkspaceForkResult struct {
	Forked    bool                    `json:"forked"`
	Workspace *workspacemgr.Workspace `json:"workspace,omitempty"`
}

func HandleWorkspaceCreate(ctx context.Context, params json.RawMessage, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceCreateResult, *rpckit.RPCError) {
	var p WorkspaceCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	spec, prepErr, emptyResultOnErr := create.PrepareCreate(ctx, p.Spec, factory)
	if prepErr != nil {
		if emptyResultOnErr {
			return &WorkspaceCreateResult{}, prepErr
		}
		return nil, prepErr
	}

	ws, err := mgr.Create(ctx, spec)
	if err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	if rpcErr := ensureLocalRuntimeWorkspace(ctx, ws, factory, mgr, spec.ConfigBundle); rpcErr != nil {
		_ = mgr.Remove(ws.ID)
		return nil, rpcErr
	}

	return &WorkspaceCreateResult{Workspace: ws}, nil
}

func HandleWorkspaceOpen(_ context.Context, params json.RawMessage, mgr *workspacemgr.Manager) (*WorkspaceOpenResult, *rpckit.RPCError) {
	var p WorkspaceOpenParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	ws, ok := mgr.Get(p.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &WorkspaceOpenResult{Workspace: ws}, nil
}

func HandleWorkspaceList(_ context.Context, _ json.RawMessage, mgr *workspacemgr.Manager) (*WorkspaceListResult, *rpckit.RPCError) {
	all := mgr.List()
	return &WorkspaceListResult{Workspaces: all}, nil
}

func HandleWorkspaceRemove(ctx context.Context, params json.RawMessage, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceRemoveResult, *rpckit.RPCError) {
	var p WorkspaceRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	ws, ok := mgr.Get(p.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	if factory != nil && strings.TrimSpace(ws.Backend) != "" {
		if driver, selErr := factory.SelectDriver([]string{ws.Backend}, nil); selErr == nil {
			destroyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if destroyErr := driver.Destroy(destroyCtx, p.ID); destroyErr != nil {
				_ = destroyErr
			}
		}
	}

	removed := mgr.Remove(p.ID)
	if !removed {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &WorkspaceRemoveResult{Removed: true}, nil
}

func HandleWorkspaceStop(_ context.Context, params json.RawMessage, mgr *workspacemgr.Manager) (*WorkspaceStopResult, *rpckit.RPCError) {
	var p WorkspaceStopParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	if err := mgr.Stop(p.ID); err != nil {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &WorkspaceStopResult{Stopped: true}, nil
}

func HandleWorkspaceStart(_ context.Context, params json.RawMessage, mgr *workspacemgr.Manager) (*WorkspaceStartResult, *rpckit.RPCError) {
	var p WorkspaceStartParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	if err := mgr.Start(p.ID); err != nil {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &WorkspaceStartResult{Started: true}, nil
}

func HandleWorkspaceRestore(ctx context.Context, params json.RawMessage, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceRestoreResult, *rpckit.RPCError) {
	var p WorkspaceRestoreParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	ws, ok := mgr.Get(p.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	var selectedDriver runtime.Driver
	var requiredBackends []string

	if factory != nil {
		requiredBackends, requiredCaps := create.DefaultPlatformHints()

		driver, err := factory.SelectDriver(requiredBackends, requiredCaps)
		if err != nil {
			return &WorkspaceRestoreResult{}, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", err)}
		}
		selectedDriver = driver
	}

	ws, ok = mgr.Restore(p.ID)
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
		if err := mgr.SetBackend(p.ID, resolvedBackend); err != nil {
			return &WorkspaceRestoreResult{}, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend persist failed: %v", err)}
		}
		updated, ok := mgr.Get(p.ID)
		if !ok {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		ws = updated
	}

	return &WorkspaceRestoreResult{Restored: true, Workspace: ws}, nil
}

func HandleWorkspacePause(ctx context.Context, params json.RawMessage, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspacePauseResult, *rpckit.RPCError) {
	_ = ctx
	var p WorkspacePauseParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	ws, ok := mgr.Get(p.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	if factory != nil {
		if rpcErr := ensureLocalRuntimeWorkspace(ctx, ws, factory, mgr, ""); rpcErr != nil {
			return nil, rpcErr
		}

		driver, err := factory.SelectDriver([]string{ws.Backend}, nil)
		if err != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", err)}
		}
		if err := driver.Pause(context.Background(), ws.ID); err != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime pause failed: %v", err)}
		}
	}

	if err := mgr.Pause(p.ID); err != nil {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &WorkspacePauseResult{Paused: true}, nil
}

func HandleWorkspaceResume(ctx context.Context, params json.RawMessage, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceResumeResult, *rpckit.RPCError) {
	_ = ctx
	var p WorkspaceResumeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	ws, ok := mgr.Get(p.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	if factory != nil {
		if rpcErr := ensureLocalRuntimeWorkspace(ctx, ws, factory, mgr, ""); rpcErr != nil {
			return nil, rpcErr
		}

		driver, err := factory.SelectDriver([]string{ws.Backend}, nil)
		if err != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", err)}
		}
		if err := driver.Resume(context.Background(), ws.ID); err != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime resume failed: %v", err)}
		}
	}

	if err := mgr.Resume(p.ID); err != nil {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &WorkspaceResumeResult{Resumed: true}, nil
}

func HandleWorkspaceFork(ctx context.Context, params json.RawMessage, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceForkResult, *rpckit.RPCError) {
	_ = ctx
	var p WorkspaceForkParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	child, err := mgr.Fork(p.ID, p.ChildWorkspaceName, p.ChildRef)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "workspace not found") {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: err.Error()}
	}

	if factory != nil {
		parent, ok := mgr.Get(p.ID)
		if !ok {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		if rpcErr := ensureLocalRuntimeWorkspace(ctx, parent, factory, mgr, ""); rpcErr != nil {
			return nil, rpcErr
		}

		driver, selErr := factory.SelectDriver([]string{parent.Backend}, nil)
		if selErr != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", selErr)}
		}
		if forkErr := driver.Fork(context.Background(), parent.ID, child.ID); forkErr != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime fork failed: %v", forkErr)}
		}
	}

	return &WorkspaceForkResult{Forked: true, Workspace: child}, nil
}

func ensureLocalRuntimeWorkspace(ctx context.Context, ws *workspacemgr.Workspace, factory *runtime.Factory, mgr *workspacemgr.Manager, configBundle string) *rpckit.RPCError {
	if factory == nil || ws == nil {
		return nil
	}

	driver, err := factory.SelectDriver([]string{ws.Backend}, nil)
	if err != nil {
		return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", err)}
	}

	req := runtime.CreateRequest{
		WorkspaceID:   ws.ID,
		WorkspaceName: ws.WorkspaceName,
		ProjectRoot:   ws.RootPath,
		ConfigBundle:  configBundle,
		Options: map[string]string{
			"host_cli_sync": "true",
		},
	}
	err = driver.Create(ctx, req)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		if errors.Is(err, runtime.ErrWorkspaceMountFailed) {
			return nil
		}
		return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime create failed: %v", err)}
	}

	return nil
}
