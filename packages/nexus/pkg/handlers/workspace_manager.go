package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/store"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace/create"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type WorkspaceCreateParams struct {
	Spec              workspacemgr.CreateSpec `json:"spec,omitempty"`
	ProjectID         string                  `json:"projectId,omitempty"`
	Repo              string                  `json:"repo,omitempty"`
	TargetBranch      string                  `json:"targetBranch,omitempty"`
	SourceBranch      string                  `json:"sourceBranch,omitempty"`
	SourceWorkspaceID string                  `json:"sourceWorkspaceId,omitempty"`
	Fresh             bool                    `json:"fresh,omitempty"`
	WorkspaceName     string                  `json:"workspaceName,omitempty"`
	AgentProfile      string                  `json:"agentProfile,omitempty"`
	Policy            workspacemgr.Policy     `json:"policy,omitempty"`
	Backend           string                  `json:"backend,omitempty"`
	AuthBinding       map[string]string       `json:"authBinding,omitempty"`
	ConfigBundle      string                  `json:"configBundle,omitempty"`
}

type WorkspaceOpenParams struct {
	ID string `json:"id"`
}

type WorkspaceListParams struct {
	AgentProfile string `json:"agentProfile,omitempty"`
}

type WorkspaceRemoveParams struct {
	ID             string `json:"id"`
	DeleteHostPath bool   `json:"deleteHostPath,omitempty"`
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

type WorkspaceForkParams struct {
	ID                 string `json:"id"`
	ChildWorkspaceName string `json:"childWorkspaceName,omitempty"`
	ChildRef           string `json:"childRef,omitempty"`
	SourceWorkspaceID  string `json:"sourceWorkspaceId,omitempty"`
}

type WorkspaceCheckoutParams struct {
	ID          string `json:"id,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	TargetRef   string `json:"targetRef"`
	OnConflict  string `json:"onConflict,omitempty"`
}

type WorkspaceCreateResult struct {
	Workspace             *workspacemgr.Workspace `json:"workspace"`
	EffectiveSourceBranch string                  `json:"effectiveSourceBranch,omitempty"`
	SourceWorkspaceID     string                  `json:"sourceWorkspaceId,omitempty"`
	UsedLineageSnapshotID string                  `json:"usedLineageSnapshotId,omitempty"`
	FreshApplied          bool                    `json:"freshApplied"`
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
	Workspace *workspacemgr.Workspace `json:"workspace"`
}

type WorkspaceRestoreResult struct {
	Restored  bool                    `json:"restored"`
	Workspace *workspacemgr.Workspace `json:"workspace,omitempty"`
}

type WorkspaceForkResult struct {
	Forked    bool                    `json:"forked"`
	Workspace *workspacemgr.Workspace `json:"workspace,omitempty"`
}

type WorkspaceCheckoutResult struct {
	Workspace     *workspacemgr.Workspace `json:"workspace"`
	CurrentRef    string                  `json:"currentRef"`
	CurrentCommit string                  `json:"currentCommit,omitempty"`
}

func HandleWorkspaceCreate(ctx context.Context, req WorkspaceCreateParams, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceCreateResult, *rpckit.RPCError) {
	return HandleWorkspaceCreateWithProjects(ctx, req, mgr, nil, factory)
}

func HandleWorkspaceCreateWithProjects(ctx context.Context, req WorkspaceCreateParams, mgr *workspacemgr.Manager, projMgr *projectmgr.Manager, factory *runtime.Factory) (*WorkspaceCreateResult, *rpckit.RPCError) {
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
				return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("workspace baseline snapshot persist failed: %v", setErr)}
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

func shouldUseProjectRootPathForBase(req WorkspaceCreateParams, spec workspacemgr.CreateSpec, mgr *workspacemgr.Manager) bool {
	if mgr == nil || !req.Fresh {
		return false
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return false
	}
	// Only the first/root sandbox in a project should mount the project root path.
	return resolveProjectRootWorkspace(mgr, projectID, deriveProjectRepoID(spec.Repo)) == nil
}

func shouldCopyDirtyStateForCreate(ws *workspacemgr.Workspace, usedCheckpointSnapshot bool) bool {
	if ws == nil {
		return false
	}
	if isVMIsolationBackend(ws.Backend) {
		return !usedCheckpointSnapshot
	}
	return true
}

// isVMIsolationBackend returns true if the backend is a VM isolation backend (firecracker only now).
func isVMIsolationBackend(backend string) bool {
	return strings.ToLower(strings.TrimSpace(backend)) == "firecracker"
}

func resolveCreateSpec(req WorkspaceCreateParams, projMgr *projectmgr.Manager) (workspacemgr.CreateSpec, error) {
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

type createSourceHint struct {
	SourceBranch      string
	SnapshotID        string
	SourceWorkspaceID string
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
		if root := resolveProjectRootWorkspace(mgr, projectID, deriveProjectRepoID(spec.Repo)); root != nil {
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
	repoID := deriveProjectRepoID(spec.Repo)
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

func HandleWorkspaceOpen(_ context.Context, req WorkspaceOpenParams, mgr *workspacemgr.Manager) (*WorkspaceOpenResult, *rpckit.RPCError) {
	ws, ok := mgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	enrichWorkspaceRuntimeLabel(ws)
	return &WorkspaceOpenResult{Workspace: ws}, nil
}

func HandleWorkspaceList(_ context.Context, _ WorkspaceListParams, mgr *workspacemgr.Manager) (*WorkspaceListResult, *rpckit.RPCError) {
	all := mgr.List()
	if len(all) == 0 {
		log.Printf("[workspace.list] count=0")
		return &WorkspaceListResult{Workspaces: all}, nil
	}
	parts := make([]string, 0, len(all))
	for _, ws := range all {
		if ws == nil {
			continue
		}
		enrichWorkspaceRuntimeLabel(ws)
		parts = append(parts, fmt.Sprintf("%s:%q:%s", ws.ID, ws.WorkspaceName, ws.RuntimeLabel))
	}
	log.Printf("[workspace.list] count=%d %s", len(parts), strings.Join(parts, " | "))
	return &WorkspaceListResult{Workspaces: all}, nil
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
				_ = destroyErr
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
					Message: fmt.Sprintf("backend selection failed: driver not registered for workspace backend %q", explicitBackend),
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
			return &WorkspaceRestoreResult{}, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend persist failed: %v", err)}
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

func HandleWorkspaceFork(ctx context.Context, req WorkspaceForkParams, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceForkResult, *rpckit.RPCError) {
	requestedParent, ok := mgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	forkSource := resolveProjectRootForkSource(mgr, requestedParent)
	if explicitSourceID := strings.TrimSpace(req.SourceWorkspaceID); explicitSourceID != "" {
		explicitSource, explicitOK := mgr.Get(explicitSourceID)
		if !explicitOK || explicitSource == nil {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		// Keep explicit override bounded to the same project/repo scope.
		if strings.TrimSpace(explicitSource.ProjectID) != strings.TrimSpace(requestedParent.ProjectID) ||
			strings.TrimSpace(explicitSource.RepoID) != strings.TrimSpace(requestedParent.RepoID) {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "sourceWorkspaceId must belong to the same project and repo"}
		}
		forkSource = explicitSource
	}
	child, err := mgr.Fork(forkSource.ID, req.ChildWorkspaceName, req.ChildRef)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "workspace not found") {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: err.Error()}
	}

	if factory != nil {
		parent, ok := mgr.Get(forkSource.ID)
		if !ok {
			return nil, rpckit.ErrWorkspaceNotFound
		}

		driver, selErr := selectDriverForWorkspaceBackend(factory, parent.Backend)
		if selErr != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", selErr)}
		}
		if forkErr := driver.Fork(context.Background(), parent.ID, child.ID); forkErr != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime fork failed: %v", forkErr)}
		}

		if snapshotter, ok := driver.(runtime.ForkSnapshotter); ok {
			if snapshotID, snapErr := snapshotter.CheckpointFork(ctx, parent.ID, child.ID); snapErr != nil {
				return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime fork checkpoint failed: %v", snapErr)}
			} else if strings.TrimSpace(snapshotID) != "" {
				if setErr := mgr.SetLineageSnapshot(child.ID, snapshotID); setErr != nil {
					return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("workspace snapshot persist failed: %v", setErr)}
				}
			}
		}
	}

	updatedChild, ok := mgr.Get(child.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	enrichWorkspaceRuntimeLabel(updatedChild)
	return &WorkspaceForkResult{Forked: true, Workspace: updatedChild}, nil
}

func resolveProjectRootForkSource(mgr *workspacemgr.Manager, requestedParent *workspacemgr.Workspace) *workspacemgr.Workspace {
	if mgr == nil || requestedParent == nil {
		return requestedParent
	}
	candidates := make([]*workspacemgr.Workspace, 0, 4)
	for _, ws := range mgr.List() {
		if ws == nil {
			continue
		}
		if strings.TrimSpace(ws.ProjectID) != strings.TrimSpace(requestedParent.ProjectID) {
			continue
		}
		if strings.TrimSpace(ws.RepoID) != strings.TrimSpace(requestedParent.RepoID) {
			continue
		}
		if strings.TrimSpace(ws.ParentWorkspaceID) != "" {
			continue
		}
		candidates = append(candidates, ws)
	}
	if len(candidates) == 0 {
		return requestedParent
	}
	best := candidates[0]
	for _, ws := range candidates[1:] {
		if ws.CreatedAt.Before(best.CreatedAt) {
			best = ws
		}
	}
	return best
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

func HandleWorkspaceCheckout(_ context.Context, req WorkspaceCheckoutParams, mgr *workspacemgr.Manager) (*WorkspaceCheckoutResult, *rpckit.RPCError) {
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(req.ID)
	}
	if workspaceID == "" || strings.TrimSpace(req.TargetRef) == "" {
		return nil, rpckit.ErrInvalidParams
	}
	onConflict, ok := normalizeCheckoutConflictMode(req.OnConflict)
	if !ok {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "invalid onConflict mode (expected: fail, stash, discard)"}
	}
	ws, found := mgr.Get(workspaceID)
	if !found {
		return nil, rpckit.ErrWorkspaceNotFound
	}
	if err := mgr.CanCheckout(workspaceID, req.TargetRef); err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: err.Error()}
	}

	currentCommit, checkoutErr := checkoutRefOnHost(ws, req.TargetRef, onConflict)
	if checkoutErr != nil {
		return nil, checkoutErr
	}

	updated, err := mgr.Checkout(workspaceID, req.TargetRef)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "workspace not found") {
			return nil, rpckit.ErrWorkspaceNotFound
		}
		return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: err.Error()}
	}
	result := &WorkspaceCheckoutResult{
		Workspace:     updated,
		CurrentRef:    strings.TrimSpace(updated.Ref),
		CurrentCommit: strings.TrimSpace(currentCommit),
	}
	if strings.TrimSpace(currentCommit) != "" {
		if setErr := mgr.SetCurrentCommit(workspaceID, currentCommit); setErr == nil {
			if refreshed, ok := mgr.Get(workspaceID); ok {
				result.Workspace = refreshed
				result.CurrentCommit = strings.TrimSpace(refreshed.CurrentCommit)
			}
		}
	}
	enrichWorkspaceRuntimeLabel(result.Workspace)
	return result, nil
}

func normalizeCheckoutConflictMode(raw string) (string, bool) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		return "prompt", true
	}
	switch mode {
	case "prompt", "fail", "stash", "discard":
		return mode, true
	default:
		return "", false
	}
}

func checkoutRefOnHost(ws *workspacemgr.Workspace, targetRef string, onConflict string) (string, *rpckit.RPCError) {
	root := preferredProjectRootForRuntime(ws)
	if strings.TrimSpace(root) == "" {
		return "", nil
	}

	if _, err := runGitAt(root, "rev-parse", "--is-inside-work-tree"); err != nil {
		// Some backends may not expose a local git checkout path.
		return "", nil
	}

	statusOut, statusErr := runGitAt(root, "status", "--porcelain", "--untracked-files=no")
	if statusErr != nil {
		return "", &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("git status failed before checkout: %v", statusErr)}
	}
	if strings.TrimSpace(statusOut) != "" {
		switch onConflict {
		case "stash":
			if _, err := runGitAt(root, "stash", "push", "-u", "-m", fmt.Sprintf("nexus checkout %d", time.Now().UTC().Unix())); err != nil {
				return "", &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("checkout conflict: unable to stash local changes: %v", err)}
			}
		case "discard":
			if _, err := runGitAt(root, "reset", "--hard"); err != nil {
				return "", &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("checkout conflict: unable to reset local changes: %v", err)}
			}
			if _, err := runGitAt(root, "clean", "-fd"); err != nil {
				return "", &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("checkout conflict: unable to clean local changes: %v", err)}
			}
		case "prompt":
			return "", checkoutConflictPromptError(targetRef, statusOut)
		default:
			return "", &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "checkout conflict: workspace has uncommitted changes (use onConflict=stash or onConflict=discard)"}
		}
	}

	normalizedTarget := strings.TrimSpace(targetRef)
	if normalizedTarget == "" {
		return "", &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: "target ref is required"}
	}
	if _, err := runGitAt(root, "show-ref", "--verify", "--quiet", "refs/heads/"+normalizedTarget); err == nil {
		if _, err := runGitAt(root, "checkout", "--ignore-other-worktrees", normalizedTarget); err != nil {
			return "", &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("git checkout failed: %v", err)}
		}
	} else {
		if _, err := runGitAt(root, "checkout", "--ignore-other-worktrees", "-B", normalizedTarget); err != nil {
			return "", &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: fmt.Sprintf("git checkout failed: %v", err)}
		}
	}
	commit, err := runGitAt(root, "rev-parse", "HEAD")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(commit), nil
}

func runGitAt(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("%s", errMsg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func checkoutConflictPromptError(targetRef string, statusPorcelain string) *rpckit.RPCError {
	lines := strings.Split(strings.TrimSpace(statusPorcelain), "\n")
	preview := make([]string, 0, 3)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		preview = append(preview, strings.TrimSpace(line))
		if len(preview) >= 3 {
			break
		}
	}
	sample := strings.Join(preview, "; ")
	if sample == "" {
		sample = "working tree has pending changes"
	}
	changedFiles := parseChangedFiles(statusPorcelain)
	suggestedActions := []map[string]any{
		{"id": "stash", "label": "Stash changes and switch", "destructive": false},
		{"id": "discard", "label": "Discard changes and switch", "destructive": true},
		{"id": "cancel", "label": "Cancel", "destructive": false},
	}
	return &rpckit.RPCError{
		Code: rpckit.ErrCheckoutConflict.Code,
		Message: fmt.Sprintf(
			"checkout to %q requires resolving local changes (%s). Retry with onConflict=stash, onConflict=discard, or cancel.",
			strings.TrimSpace(targetRef),
			sample,
		),
		Data: map[string]any{
			"kind":             "workspace.checkout.conflict",
			"targetRef":        strings.TrimSpace(targetRef),
			"changedFiles":     changedFiles,
			"suggestedActions": suggestedActions,
		},
	}
}

func parseChangedFiles(statusPorcelain string) []string {
	lines := strings.Split(strings.TrimSpace(statusPorcelain), "\n")
	out := make([]string, 0, 5)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 3 {
			path := strings.TrimSpace(line[3:])
			if strings.Contains(path, " -> ") {
				parts := strings.Split(path, " -> ")
				path = strings.TrimSpace(parts[len(parts)-1])
			}
			if path != "" {
				out = append(out, path)
			}
		}
		if len(out) >= 5 {
			break
		}
	}
	return out
}

func checkpointBaselineLineageSnapshot(ctx context.Context, ws *workspacemgr.Workspace, factory *runtime.Factory) (string, error) {
	if ws == nil || factory == nil || strings.TrimSpace(ws.Backend) == "" {
		return "", nil
	}
	driver, selErr := selectDriverForWorkspaceBackend(factory, ws.Backend)
	if selErr != nil {
		return "", nil
	}
	snapshotter, ok := driver.(runtime.ForkSnapshotter)
	if !ok {
		return "", nil
	}
	snapshotID, snapErr := snapshotter.CheckpointFork(ctx, ws.ID, ws.ID)
	if snapErr != nil {
		return "", fmt.Errorf("baseline checkpoint failed: %w", snapErr)
	}
	return strings.TrimSpace(snapshotID), nil
}

func checkpointLatestFirecrackerSnapshotForCreate(ctx context.Context, mgr *workspacemgr.Manager, factory *runtime.Factory, sourceWorkspaceID string, childWorkspaceID string) (string, bool, error) {
	if mgr == nil || factory == nil {
		return "", false, nil
	}
	sourceWorkspaceID = strings.TrimSpace(sourceWorkspaceID)
	childWorkspaceID = strings.TrimSpace(childWorkspaceID)
	if sourceWorkspaceID == "" || childWorkspaceID == "" {
		return "", false, fmt.Errorf("source and child workspace ids are required")
	}
	sourceWS, ok := mgr.Get(sourceWorkspaceID)
	if !ok || sourceWS == nil {
		return "", false, fmt.Errorf("source workspace not found: %s", sourceWorkspaceID)
	}
	if rpcErr := ensureLocalRuntimeWorkspace(ctx, sourceWS, factory, mgr, ""); rpcErr != nil {
		return "", false, fmt.Errorf(rpcErr.Message)
	}

	driver, err := selectDriverForWorkspaceBackend(factory, sourceWS.Backend)
	if err != nil {
		return "", false, err
	}
	snapshotter, ok := driver.(runtime.ForkSnapshotter)
	if !ok {
		// Some environments (e.g. macOS shim backends) expose VM isolation semantics
		// without native checkpoint support; caller should fall back to worktree sync.
		return "", false, nil
	}
	snapshotID, snapErr := snapshotter.CheckpointFork(ctx, sourceWorkspaceID, childWorkspaceID)
	if snapErr != nil {
		return "", true, snapErr
	}
	trimmed := strings.TrimSpace(snapshotID)
	if trimmed == "" {
		return "", true, fmt.Errorf("empty checkpoint snapshot id")
	}
	return trimmed, true, nil
}

func ensureLocalRuntimeWorkspace(ctx context.Context, ws *workspacemgr.Workspace, factory *runtime.Factory, mgr *workspacemgr.Manager, configBundle string) *rpckit.RPCError {
	if factory == nil || ws == nil {
		return nil
	}

	driver, err := selectDriverForWorkspaceBackend(factory, ws.Backend)
	if err != nil {
		return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", err)}
	}

	projectRoot := preferredProjectRootForRuntime(ws)

	options := map[string]string{
		"host_cli_sync": "true",
	}
	if isVMIsolationBackend(ws.Backend) {
		options["vm.mode"] = "dedicated"
	}
	if strings.TrimSpace(ws.LineageSnapshotID) != "" {
		options["lineage_snapshot_id"] = strings.TrimSpace(ws.LineageSnapshotID)
	}
	var settingsRepo store.SandboxResourceSettingsRepository
	if mgr != nil {
		settingsRepo = mgr.SandboxResourceSettingsRepository()
	}
	options = applySandboxResourcePolicy(options, settingsRepo)

	req := runtime.CreateRequest{
		WorkspaceID:   ws.ID,
		WorkspaceName: ws.WorkspaceName,
		ProjectRoot:   projectRoot,
		ConfigBundle:  configBundle,
		Options:       options,
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

func preferredProjectRootForRuntime(ws *workspacemgr.Workspace) string {
	if ws == nil {
		return ""
	}
	candidates := make([]string, 0, 3)
	candidates = append(candidates, strings.TrimSpace(ws.HostWorkspacePath))
	candidates = append(candidates, strings.TrimSpace(ws.LocalWorktreePath))
	if inferred := inferredWorktreePath(ws); inferred != "" {
		candidates = append(candidates, inferred)
	}
	candidates = append(candidates, strings.TrimSpace(ws.Repo))

	for _, candidate := range candidates {
		if canonical := canonicalWorkspaceCandidate(ws, candidate); canonical != "" {
			return canonical
		}
	}
	return ""
}

func inferredWorktreePath(ws *workspacemgr.Workspace) string {
	if ws == nil {
		return ""
	}
	repoPath := canonicalExistingDir(strings.TrimSpace(ws.Repo))
	if repoPath == "" {
		return ""
	}
	ref := strings.TrimSpace(ws.CurrentRef)
	if ref == "" {
		ref = strings.TrimSpace(ws.TargetBranch)
	}
	if ref == "" {
		ref = strings.TrimSpace(ws.Ref)
	}
	return filepath.Join(repoPath, ".worktrees", workspacemgr.HostWorkspaceDirName(ref))
}

func canonicalExistingDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ""
	}
	resolved := filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(resolved); err == nil && strings.TrimSpace(real) != "" {
		resolved = filepath.Clean(real)
	}
	return resolved
}

func canonicalWorkspaceCandidate(ws *workspacemgr.Workspace, candidate string) string {
	canonical := canonicalExistingDir(candidate)
	if canonical == "" {
		return ""
	}
	if ws == nil {
		return canonical
	}
	if workspacemgr.IsManagedHostWorkspacePath(canonical) && !workspacemgr.HasValidHostWorkspaceMarker(canonical, ws.ID) {
		return ""
	}
	return canonical
}

func preferredLineageSnapshotForCreate(mgr *workspacemgr.Manager, target *workspacemgr.Workspace) string {
	if mgr == nil || target == nil {
		return ""
	}
	targetRepoID := strings.TrimSpace(target.RepoID)
	targetBackend := strings.TrimSpace(target.Backend)
	if targetRepoID == "" || targetBackend == "" {
		return ""
	}

	var best *workspacemgr.Workspace
	for _, candidate := range mgr.List() {
		if candidate == nil {
			continue
		}
		if candidate.ID == target.ID {
			continue
		}
		if strings.TrimSpace(candidate.RepoID) != targetRepoID {
			continue
		}
		candidateBackend := strings.TrimSpace(candidate.Backend)
		if candidateBackend != targetBackend {
			// Allow cross-backend lineage snapshots between VM isolation backends
			if !(isVMIsolationBackend(candidateBackend) && isVMIsolationBackend(targetBackend)) {
				continue
			}
		}
		if strings.TrimSpace(candidate.LineageSnapshotID) == "" {
			continue
		}
		if best == nil || candidate.UpdatedAt.After(best.UpdatedAt) {
			best = candidate
		}
	}
	if best == nil {
		return ""
	}
	return strings.TrimSpace(best.LineageSnapshotID)
}

func suspendRuntimeWorkspace(ctx context.Context, ws *workspacemgr.Workspace, factory *runtime.Factory, mgr *workspacemgr.Manager) *rpckit.RPCError {
	if rpcErr := ensureLocalRuntimeWorkspace(ctx, ws, factory, mgr, ""); rpcErr != nil {
		return rpcErr
	}

	driver, err := selectDriverForWorkspaceBackend(factory, ws.Backend)
	if err != nil {
		return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", err)}
	}

	if err := driver.Pause(ctx, ws.ID); err != nil {
		if errors.Is(err, runtime.ErrOperationNotSupported) {
			if stopErr := driver.Stop(ctx, ws.ID); stopErr != nil {
				return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime stop fallback failed: %v", stopErr)}
			}
			return nil
		}
		return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime pause failed: %v", err)}
	}

	return nil
}

func resumeRuntimeWorkspace(ctx context.Context, ws *workspacemgr.Workspace, factory *runtime.Factory, mgr *workspacemgr.Manager) *rpckit.RPCError {
	if rpcErr := ensureLocalRuntimeWorkspace(ctx, ws, factory, mgr, ""); rpcErr != nil {
		return rpcErr
	}

	driver, err := selectDriverForWorkspaceBackend(factory, ws.Backend)
	if err != nil {
		return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v", err)}
	}

	if err := driver.Resume(ctx, ws.ID); err != nil {
		if errors.Is(err, runtime.ErrOperationNotSupported) {
			if startErr := driver.Start(ctx, ws.ID); startErr != nil {
				return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime start fallback failed: %v", startErr)}
			}
			return nil
		}
		return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime resume failed: %v", err)}
	}

	return nil
}

func selectDriverForWorkspaceBackend(factory *runtime.Factory, backend string) (runtime.Driver, error) {
	trimmed := normalizeWorkspaceBackend(strings.TrimSpace(backend))
	if trimmed == "" {
		return nil, fmt.Errorf("workspace backend is empty")
	}
	if driver, ok := factory.DriverForBackend(trimmed); ok {
		return driver, nil
	}
	return factory.SelectDriver([]string{trimmed}, nil)
}

func normalizeWorkspaceBackend(backend string) string {
	return strings.TrimSpace(backend)
}

func enrichWorkspaceRuntimeLabel(ws *workspacemgr.Workspace) {
	if ws == nil {
		return
	}
	ws.RuntimeLabel = runtimeLabelForWorkspace(ws)
}

func runtimeLabelForWorkspace(ws *workspacemgr.Workspace) string {
	if ws == nil {
		return ""
	}
	backend := strings.TrimSpace(ws.Backend)
	repo := strings.TrimSpace(ws.Repo)
	if repo == "" {
		return fmt.Sprintf("backend=%s", backend)
	}
	cfg, _, err := config.LoadWorkspaceConfig(repo)
	if err != nil {
		return fmt.Sprintf("backend=%s", backend)
	}
	level := strings.TrimSpace(cfg.Isolation.Level)
	if level == "" {
		level = "vm"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "backend=%s isolation=%s", backend, level)
	switch strings.ToLower(backend) {
	case "firecracker":
		fmt.Fprintf(&b, " vm.mode=dedicated")
	case "process":
		if cfg.InternalFeatures.ProcessSandbox {
			fmt.Fprintf(&b, " processSandbox=relaxed")
		} else {
			fmt.Fprintf(&b, " processSandbox=strict")
		}
	}
	return b.String()
}
