package handlers

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
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
