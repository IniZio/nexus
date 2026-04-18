package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/store"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func isVMIsolationBackend(backend string) bool {
	return strings.ToLower(strings.TrimSpace(backend)) == "firecracker"
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
	if inferred := workspacemgr.InferredWorktreePath(ws); inferred != "" {
		candidates = append(candidates, inferred)
	}
	candidates = append(candidates, strings.TrimSpace(ws.Repo))

	for _, candidate := range candidates {
		if canonical := workspacemgr.CanonicalWorkspaceCandidate(ws, candidate); canonical != "" {
			return canonical
		}
	}
	return ""
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

func RuntimeLabelForWorkspace(ws *workspacemgr.Workspace) string {
	return runtimeLabelForWorkspace(ws)
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
	case "process":
		if cfg.InternalFeatures.ProcessSandbox {
			fmt.Fprintf(&b, " processSandbox=relaxed")
		} else {
			fmt.Fprintf(&b, " processSandbox=strict")
		}
	}
	return b.String()
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
