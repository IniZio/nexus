package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
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

var firecrackerPreflightRunner = func(repo string, opts runtime.PreflightOptions) runtime.FirecrackerPreflightResult {
	return runtime.RunFirecrackerPreflight(repo, opts)
}

var (
	runtimeSetupGOOS     = goruntime.GOOS
	runtimeSetupIsRootFn = func() bool {
		return os.Geteuid() == 0
	}
	runtimeSetupSudoNOKFn = func() bool {
		return exec.Command("sudo", "-n", "true").Run() == nil
	}
	runtimeSetupIsTTYFn = func(f *os.File) bool {
		if f == nil {
			return false
		}
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return (fi.Mode() & os.ModeCharDevice) != 0
	}
	runtimeSetupResolveBinaryFn = resolveNexusBinaryPath
	runtimeSetupRunCommandFn    = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		return cmd.CombinedOutput()
	}
)

var runtimeSetupRunner = func(ctx context.Context, repo, backend string) error {
	if strings.TrimSpace(backend) != "firecracker" {
		return nil
	}
	if strings.TrimSpace(repo) == "" {
		return fmt.Errorf("repo is required for runtime setup")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if runtimeSetupRequiresManualPrivilege() {
		return runtimeSetupManualPrivilegeError(repo)
	}

	binary, err := runtimeSetupResolveBinaryFn()
	if err != nil {
		return err
	}

	if out, err := runtimeSetupRunCommandFn(ctx, binary, "init", "--project-root", repo); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("nexus init failed: %w", err)
		}
		return fmt.Errorf("nexus init failed: %w: %s", err, msg)
	}
	return nil
}

func setPreflightSequenceForTest(sequence []runtime.FirecrackerPreflightResult) {
	seq := append([]runtime.FirecrackerPreflightResult(nil), sequence...)
	idx := 0
	firecrackerPreflightRunner = func(_ string, _ runtime.PreflightOptions) runtime.FirecrackerPreflightResult {
		if len(seq) == 0 {
			return runtime.FirecrackerPreflightResult{Status: runtime.PreflightPass}
		}
		if idx >= len(seq) {
			return seq[len(seq)-1]
		}
		result := seq[idx]
		idx++
		return result
	}
}

func resetPreflightRunnerForTest() {
	firecrackerPreflightRunner = func(repo string, opts runtime.PreflightOptions) runtime.FirecrackerPreflightResult {
		return runtime.RunFirecrackerPreflight(repo, opts)
	}
}

func setRuntimeSetupRunnerForTest(runner func(ctx context.Context, repo, backend string) error) {
	runtimeSetupRunner = runner
}

func resetRuntimeSetupRunnerForTest() {
	runtimeSetupGOOS = goruntime.GOOS
	runtimeSetupIsRootFn = func() bool {
		return os.Geteuid() == 0
	}
	runtimeSetupSudoNOKFn = func() bool {
		return exec.Command("sudo", "-n", "true").Run() == nil
	}
	runtimeSetupIsTTYFn = func(f *os.File) bool {
		if f == nil {
			return false
		}
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return (fi.Mode() & os.ModeCharDevice) != 0
	}
	runtimeSetupResolveBinaryFn = resolveNexusBinaryPath
	runtimeSetupRunCommandFn = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		return cmd.CombinedOutput()
	}

	runtimeSetupRunner = func(ctx context.Context, repo, backend string) error {
		if strings.TrimSpace(backend) != "firecracker" {
			return nil
		}
		if strings.TrimSpace(repo) == "" {
			return fmt.Errorf("repo is required for runtime setup")
		}
		if ctx == nil {
			ctx = context.Background()
		}
		if runtimeSetupRequiresManualPrivilege() {
			return runtimeSetupManualPrivilegeError(repo)
		}

		binary, err := runtimeSetupResolveBinaryFn()
		if err != nil {
			return err
		}

		if out, err := runtimeSetupRunCommandFn(ctx, binary, "init", "--project-root", repo); err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				return fmt.Errorf("nexus init failed: %w", err)
			}
			return fmt.Errorf("nexus init failed: %w: %s", err, msg)
		}
		return nil
	}
}

func runtimeSetupRequiresManualPrivilege() bool {
	if runtimeSetupGOOS != "linux" {
		return false
	}
	if runtimeSetupIsRootFn() || runtimeSetupSudoNOKFn() || runtimeSetupIsTTYFn(os.Stdin) {
		return false
	}
	return true
}

func runtimeSetupManualPrivilegeError(repo string) error {
	return fmt.Errorf("firecracker runtime setup requires passwordless sudo or root access in non-interactive sessions\n\nmanual next steps:\n  sudo -E nexus init --project-root %s", repo)
}

func runtimePreflightFailure(result runtime.FirecrackerPreflightResult, setupErr error) *rpckit.RPCError {
	if setupErr != nil && strings.TrimSpace(result.SetupOutcome) == "" {
		result.SetupOutcome = fmt.Sprintf("failed: %v", setupErr)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime preflight failed: status=%s", result.Status)}
	}
	return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime preflight failed: %s", string(payload))}
}

func HandleWorkspaceCreate(ctx context.Context, params json.RawMessage, mgr *workspacemgr.Manager, factory *runtime.Factory) (*WorkspaceCreateResult, *rpckit.RPCError) {
	var p WorkspaceCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	spec := p.Spec

	if factory != nil {
		requiredBackends, requiredCaps, cfgErr := loadRuntimeSelectionFromRepoConfig(spec.Repo)
		if cfgErr != nil {
			return &WorkspaceCreateResult{}, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: cfgErr.Error()}
		}

		preflightOpts := runtime.PreflightOptions{UseOverrides: internalPreflightOverrideEnabled()}
		preflight := firecrackerPreflightRunner(spec.Repo, preflightOpts)
		var setupErr error
		var selectedBackend string

		switch preflight.Status {
		case runtime.PreflightPass:
			selectedBackend = "firecracker"
		case runtime.PreflightInstallableMissing:
			setupErr = runtimeSetupRunner(ctx, spec.Repo, "firecracker")
			if setupErr != nil {
				preflight.SetupAttempted = true
				preflight.SetupOutcome = fmt.Sprintf("failed: %v", setupErr)
				return nil, runtimePreflightFailure(preflight, setupErr)
			}
			postSetup := firecrackerPreflightRunner(spec.Repo, preflightOpts)
			postSetup.SetupAttempted = true
			postSetup.SetupOutcome = "succeeded"
			if postSetup.Status != runtime.PreflightPass {
				return nil, runtimePreflightFailure(postSetup, setupErr)
			}
			selectedBackend = "firecracker"
		case runtime.PreflightUnsupportedNested:
			selectedBackend = "seatbelt"
		default:
			return nil, runtimePreflightFailure(preflight, nil)
		}

		driver, err := factory.SelectDriver([]string{selectedBackend}, requiredCaps)
		if err != nil {
			return &WorkspaceCreateResult{}, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("backend selection failed: %v (required=%v)", err, requiredBackends)}
		}
		spec.Backend = driver.Backend()
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

func internalPreflightOverrideEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("NEXUS_INTERNAL_ENABLE_PREFLIGHT_OVERRIDE")))
	return value == "1" || value == "true" || value == "yes"
}

func resolveNexusBinaryPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("NEXUS_CLI_PATH")); p != "" {
		clean := filepath.Clean(p)
		st, err := os.Stat(clean)
		if err != nil {
			return "", fmt.Errorf("resolve nexus binary: NEXUS_CLI_PATH %q: %w", clean, err)
		}
		if st.IsDir() {
			return "", fmt.Errorf("resolve nexus binary: NEXUS_CLI_PATH %q is a directory", clean)
		}
		return clean, nil
	}

	exe, exeErr := os.Executable()
	if exeErr == nil {
		name := "nexus"
		if goruntime.GOOS == "windows" {
			name = "nexus.exe"
		}
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}

	path, err := exec.LookPath("nexus")
	if err != nil {
		if exeErr != nil {
			return "", fmt.Errorf("resolve nexus binary: executable lookup failed: %w", exeErr)
		}
		return "", fmt.Errorf("resolve nexus binary: nexus not found next to %s or in PATH", exe)
	}
	return path, nil
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
		requiredBackends, requiredCaps, cfgErr := loadRuntimeSelectionFromRepoConfig(ws.Repo)
		if cfgErr != nil {
			return &WorkspaceRestoreResult{}, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: cfgErr.Error()}
		}

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

func loadRuntimeSelectionFromRepoConfig(repo string) ([]string, []string, error) {
	repoPath := strings.TrimSpace(repo)
	if repoPath == "" {
		return nil, nil, fmt.Errorf("repo is required")
	}
	if !filepath.IsAbs(repoPath) {
		abs, err := filepath.Abs(repoPath)
		if err == nil {
			repoPath = abs
		}
	}

	info, err := os.Stat(repoPath)
	if err != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("repo must be a local directory with .nexus/workspace.json: %s", repo)
	}

	return []string{"darwin", "linux"}, nil, nil
}

func ensureLocalRuntimeWorkspace(ctx context.Context, ws *workspacemgr.Workspace, factory *runtime.Factory, mgr *workspacemgr.Manager, configBundle string) *rpckit.RPCError {
	if factory == nil || ws == nil || (ws.Backend != "firecracker" && ws.Backend != "seatbelt") {
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
