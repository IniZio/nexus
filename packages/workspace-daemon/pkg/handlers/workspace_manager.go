package handlers

import (
	"context"
	"encoding/json"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/config"

	rpckit "github.com/nexus/nexus/packages/workspace-daemon/pkg/rpcerrors"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/workspacemgr"
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

func HandleWorkspaceCreate(ctx context.Context, params json.RawMessage, mgr *workspacemgr.Manager) (*WorkspaceCreateResult, *rpckit.RPCError) {
	var p WorkspaceCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	ws, err := mgr.Create(ctx, p.Spec)
	if err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	cfg, _, cfgErr := config.LoadWorkspaceConfig(ws.RootPath)
	if cfgErr == nil {
		applyAuthDefaults(&ws.Policy, cfg.Auth.Defaults)
	}

	return &WorkspaceCreateResult{Workspace: ws}, nil
}

func applyAuthDefaults(policy *workspacemgr.Policy, defaults config.AuthDefaults) {
	if policy == nil {
		return
	}
	if len(policy.AuthProfiles) == 0 && len(defaults.AuthProfiles) > 0 {
		profiles := make([]workspacemgr.AuthProfile, 0, len(defaults.AuthProfiles))
		for _, p := range defaults.AuthProfiles {
			profiles = append(profiles, workspacemgr.AuthProfile(p))
		}
		policy.AuthProfiles = profiles
	}
	if !policy.SSHAgentForward && defaults.SSHAgentForward != nil {
		policy.SSHAgentForward = *defaults.SSHAgentForward
	}
	if policy.GitCredentialMode == "" && defaults.GitCredentialMode != "" {
		policy.GitCredentialMode = workspacemgr.GitCredentialMode(defaults.GitCredentialMode)
	}
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

func HandleWorkspaceRemove(_ context.Context, params json.RawMessage, mgr *workspacemgr.Manager) (*WorkspaceRemoveResult, *rpckit.RPCError) {
	var p WorkspaceRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	removed := mgr.Remove(p.ID)
	if !removed {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &WorkspaceRemoveResult{Removed: true}, nil
}
