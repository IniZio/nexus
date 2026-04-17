package handlers

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/project"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace/create"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func HandleWorkspaceCreate(ctx context.Context, req WorkspaceCreateParams, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceCreateResult, *rpckit.RPCError) {
	return HandleWorkspaceCreateWithProjects(ctx, req, mgr, nil, factory)
}

func HandleWorkspaceCreateWithProjects(ctx context.Context, req WorkspaceCreateParams, mgr *workspacemgr.Manager, projMgr *project.Manager, factory *runtime.Factory) (*WorkspaceCreateResult, *rpckit.RPCError) {
	spec, resolveErr := resolveCreateSpec(req, projMgr)
	if resolveErr != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: resolveErr.Error()}
	}
	sourceHint := resolveCreateSourceHint(mgr, req, spec)
	if shouldUseProjectRootPathForBase(req, spec, mgr) {
		spec.UseProjectRootPath = true
	}
	if !req.Fresh && strings.TrimSpace(req.ProjectID) != "" && strings.TrimSpace(sourceHint.SourceWorkspaceID) == "" {
		return nil, &rpckit.RPCError{
			Code:    rpckit.ErrInvalidParams.Code,
			Message: "project root sandbox is missing; create a fresh root sandbox first",
			Data: map[string]any{
				"kind":      "workspace.create.missingProjectRoot",
				"projectId": strings.TrimSpace(req.ProjectID),
			},
		}
	}
	spec, _ = create.PrepareCreate(ctx, spec, factory)

	log.Printf("[workspace.create] Creating workspace for repo: %s backend=%s", spec.Repo, strings.TrimSpace(spec.Backend))

	ws, err := mgr.Create(ctx, spec)
	if err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("workspace create failed: %v", err)}
	}
	usedCheckpointSnapshot := false
	if !req.Fresh && strings.TrimSpace(sourceHint.SourceWorkspaceID) != "" && isVMIsolationBackend(ws.Backend) {
		snapshotID, usedCheckpoint, snapshotErr := checkpointLatestFirecrackerSnapshotForCreate(ctx, mgr, factory, sourceHint.SourceWorkspaceID, ws.ID)
		if snapshotErr != nil {
			_ = mgr.Remove(ws.ID)
			return nil, &rpckit.RPCError{
				Code:    rpckit.ErrInternalError.Code,
				Message: fmt.Sprintf("workspace create firecracker checkpoint failed: %v", snapshotErr),
			}
		}
		usedCheckpointSnapshot = usedCheckpoint
		if snapshotID != "" {
			if setErr := mgr.SetLineageSnapshot(ws.ID, snapshotID); setErr != nil {
				_ = mgr.Remove(ws.ID)
				return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("workspace create snapshot persist failed: %v", setErr)}
			}
			if updatedWS, ok := mgr.Get(ws.ID); ok {
				ws = updatedWS
			}
		}
	}
	if !req.Fresh && strings.TrimSpace(ws.LineageSnapshotID) == "" {
		preferredSnapshotID := strings.TrimSpace(sourceHint.SnapshotID)
		if preferredSnapshotID == "" {
			preferredSnapshotID = preferredLineageSnapshotForCreate(mgr, ws)
		}
		if preferredSnapshotID != "" {
			if setErr := mgr.SetLineageSnapshot(ws.ID, preferredSnapshotID); setErr != nil {
				_ = mgr.Remove(ws.ID)
				return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("workspace create snapshot persist failed: %v", setErr)}
			}
			if updatedWS, ok := mgr.Get(ws.ID); ok {
				ws = updatedWS
			}
		}
	}
	if !req.Fresh && strings.TrimSpace(sourceHint.SourceBranch) != "" {
		if strings.TrimSpace(sourceHint.SourceWorkspaceID) != "" {
			_ = mgr.SetParentWorkspace(ws.ID, sourceHint.SourceWorkspaceID)
		}
		_ = mgr.SetDerivedFromRef(ws.ID, sourceHint.SourceBranch)
		if updatedWS, ok := mgr.Get(ws.ID); ok {
			ws = updatedWS
		}
	}
	if !req.Fresh && strings.TrimSpace(sourceHint.SourceWorkspaceID) != "" && shouldCopyDirtyStateForCreate(ws, usedCheckpointSnapshot) {
		if copyErr := mgr.CopyDirtyStateFromWorkspace(sourceHint.SourceWorkspaceID, ws.ID); copyErr != nil {
			_ = mgr.Remove(ws.ID)
			return nil, &rpckit.RPCError{
				Code:    rpckit.ErrInternalError.Code,
				Message: fmt.Sprintf("workspace create dirty-state sync failed: %v", copyErr),
			}
		}
	}

	log.Printf("[workspace.create] Workspace %s created, ensuring runtime...", ws.ID)

	if rpcErr := ensureLocalRuntimeWorkspace(ctx, ws, factory, mgr, spec.ConfigBundle); rpcErr != nil {
		_ = mgr.Remove(ws.ID)
		return nil, rpcErr
	}

	if !req.Fresh && strings.TrimSpace(ws.LineageSnapshotID) == "" {
		if baselineSnapshotID, baselineErr := checkpointBaselineLineageSnapshot(ctx, ws, factory); baselineErr == nil && strings.TrimSpace(baselineSnapshotID) != "" {
			if setErr := mgr.SetLineageSnapshot(ws.ID, baselineSnapshotID); setErr != nil {
				return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("workspace baseline snapshot persist failed: %v", baselineErr)}
			}
			if updatedWS, ok := mgr.Get(ws.ID); ok {
				ws = updatedWS
			}
		}
	}

	effectiveSourceBranch := strings.TrimSpace(sourceHint.SourceBranch)
	usedSnapshotID := strings.TrimSpace(ws.LineageSnapshotID)
	if req.Fresh {
		effectiveSourceBranch = ""
		usedSnapshotID = ""
	}

	enrichWorkspaceRuntimeLabel(ws)
	log.Printf("[workspace.create] Workspace %s ready runtime=%s", ws.ID, ws.RuntimeLabel)

	return &WorkspaceCreateResult{
		Workspace:             ws,
		EffectiveSourceBranch: effectiveSourceBranch,
		SourceWorkspaceID:     strings.TrimSpace(sourceHint.SourceWorkspaceID),
		UsedLineageSnapshotID: usedSnapshotID,
		FreshApplied:          req.Fresh,
	}, nil
}

type createSourceHint struct {
	SourceBranch      string
	SnapshotID        string
	SourceWorkspaceID string
}

func resolveCreateSpec(req WorkspaceCreateParams, projMgr *project.Manager) (workspacemgr.CreateSpec, error) {
	spec := req.Spec
	if strings.TrimSpace(req.ConfigBundle) != "" {
		spec.ConfigBundle = req.ConfigBundle
	}
	if strings.TrimSpace(req.WorkspaceName) != "" {
		spec.WorkspaceName = strings.TrimSpace(req.WorkspaceName)
	}
	if strings.TrimSpace(req.AgentProfile) != "" {
		spec.AgentProfile = strings.TrimSpace(req.AgentProfile)
	}
	if strings.TrimSpace(req.Backend) != "" {
		spec.Backend = strings.TrimSpace(req.Backend)
	}
	if req.AuthBinding != nil {
		spec.AuthBinding = req.AuthBinding
	}
	if hasExplicitPolicy(req.Policy) {
		spec.Policy = req.Policy
	}

	if branch := strings.TrimSpace(req.TargetBranch); branch != "" {
		spec.Ref = branch
	} else if branch := strings.TrimSpace(req.SourceBranch); branch != "" && strings.TrimSpace(spec.Ref) == "" {
		spec.Ref = branch
	}

	if repo := strings.TrimSpace(req.Repo); repo != "" {
		spec.Repo = repo
	}
	if strings.TrimSpace(spec.Repo) == "" && strings.TrimSpace(req.ProjectID) != "" {
		if projMgr == nil {
			return workspacemgr.CreateSpec{}, fmt.Errorf("project manager unavailable for project-first create")
		}
		project, ok := projMgr.Get(strings.TrimSpace(req.ProjectID))
		if !ok || project == nil || strings.TrimSpace(project.PrimaryRepo) == "" {
			return workspacemgr.CreateSpec{}, fmt.Errorf("project not found: %s", strings.TrimSpace(req.ProjectID))
		}
		spec.Repo = strings.TrimSpace(project.PrimaryRepo)
	}

	if strings.TrimSpace(spec.Repo) == "" {
		return workspacemgr.CreateSpec{}, fmt.Errorf("repo is required")
	}
	if strings.TrimSpace(spec.WorkspaceName) == "" {
		return workspacemgr.CreateSpec{}, fmt.Errorf("workspaceName is required")
	}
	return spec, nil
}

func resolveCreateSourceHint(mgr *workspacemgr.Manager, req WorkspaceCreateParams, spec workspacemgr.CreateSpec) createSourceHint {
	if mgr == nil || req.Fresh {
		return createSourceHint{}
	}
	if sourceWorkspaceID := strings.TrimSpace(req.SourceWorkspaceID); sourceWorkspaceID != "" {
		ws, ok := mgr.Get(sourceWorkspaceID)
		if ok && ws != nil {
			if projectID := strings.TrimSpace(req.ProjectID); projectID == "" || strings.TrimSpace(ws.ProjectID) == projectID {
				sourceBranch := strings.TrimSpace(ws.CurrentRef)
				if sourceBranch == "" {
					sourceBranch = strings.TrimSpace(ws.Ref)
				}
				return createSourceHint{
					SourceBranch:      sourceBranch,
					SnapshotID:        strings.TrimSpace(ws.LineageSnapshotID),
					SourceWorkspaceID: strings.TrimSpace(ws.ID),
				}
			}
		}
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return createSourceHint{SourceBranch: strings.TrimSpace(req.SourceBranch)}
	}
	targetSource := normalizeBranchForHint(req.SourceBranch)
	if targetSource == "" {
		if root := resolveProjectRootWorkspace(mgr, projectID, workspacemgr.DeriveRepoID(spec.Repo)); root != nil {
			sourceBranch := strings.TrimSpace(root.CurrentRef)
			if sourceBranch == "" {
				sourceBranch = strings.TrimSpace(root.Ref)
			}
			return createSourceHint{
				SourceBranch:      sourceBranch,
				SnapshotID:        strings.TrimSpace(root.LineageSnapshotID),
				SourceWorkspaceID: strings.TrimSpace(root.ID),
			}
		}
	}
	repoID := workspacemgr.DeriveRepoID(spec.Repo)
	var best *workspacemgr.Workspace
	for _, ws := range mgr.List() {
		if ws == nil {
			continue
		}
		if strings.TrimSpace(ws.ProjectID) != projectID {
			continue
		}
		if repoID != "" && strings.TrimSpace(ws.RepoID) != "" && strings.TrimSpace(ws.RepoID) != repoID {
			continue
		}
		wsBranch := normalizeBranchForHint(ws.CurrentRef)
		if wsBranch == "" {
			wsBranch = normalizeBranchForHint(ws.Ref)
		}
		if targetSource != "" && wsBranch != targetSource {
			continue
		}
		if best == nil || ws.UpdatedAt.After(best.UpdatedAt) {
			best = ws
		}
	}
	if best == nil {
		return createSourceHint{SourceBranch: strings.TrimSpace(req.SourceBranch)}
	}
	sourceBranch := strings.TrimSpace(best.CurrentRef)
	if sourceBranch == "" {
		sourceBranch = strings.TrimSpace(best.Ref)
	}
	if sourceBranch == "" {
		sourceBranch = strings.TrimSpace(req.SourceBranch)
	}
	return createSourceHint{
		SourceBranch:      sourceBranch,
		SnapshotID:        strings.TrimSpace(best.LineageSnapshotID),
		SourceWorkspaceID: strings.TrimSpace(best.ID),
	}
}

func normalizeBranchForHint(branch string) string {
	return strings.TrimSpace(branch)
}

func hasExplicitPolicy(p workspacemgr.Policy) bool {
	return len(p.AuthProfiles) > 0 || p.SSHAgentForward || p.GitCredentialMode != ""
}

func shouldUseProjectRootPathForBase(req WorkspaceCreateParams, spec workspacemgr.CreateSpec, mgr *workspacemgr.Manager) bool {
	if mgr == nil || !req.Fresh {
		return false
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return false
	}
	return resolveProjectRootWorkspace(mgr, projectID, workspacemgr.DeriveRepoID(spec.Repo)) == nil
}

func resolveProjectRootWorkspace(mgr *workspacemgr.Manager, projectID, repoID string) *workspacemgr.Workspace {
	if mgr == nil || strings.TrimSpace(projectID) == "" {
		return nil
	}
	var best *workspacemgr.Workspace
	for _, ws := range mgr.List() {
		if ws == nil {
			continue
		}
		if strings.TrimSpace(ws.ProjectID) != strings.TrimSpace(projectID) {
			continue
		}
		if strings.TrimSpace(ws.ParentWorkspaceID) != "" {
			continue
		}
		if strings.TrimSpace(repoID) != "" && strings.TrimSpace(ws.RepoID) != "" && strings.TrimSpace(ws.RepoID) != strings.TrimSpace(repoID) {
			continue
		}
		if best == nil || ws.CreatedAt.Before(best.CreatedAt) {
			best = ws
		}
	}
	return best
}
