package handlers

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

type GitCommandParams struct {
	WorkspaceID string                 `json:"workspaceId,omitempty"`
	Action      string                 `json:"action"`
	Params      map[string]interface{} `json:"params,omitempty"`
}

func HandleGitCommand(ctx context.Context, p GitCommandParams, ws *workspace.Workspace) (map[string]interface{}, *rpckit.RPCError) {
	if p.Action == "" {
		return nil, rpckit.ErrInvalidParams
	}

	args := mapGitActionToArgs(p)
	if len(args) == 0 {
		return nil, rpckit.ErrInvalidParams
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = ws.Path()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, rpckit.ErrInternalError
		}
	}

	return map[string]interface{}{
		"stdout":    strings.TrimSuffix(stdout.String(), "\n"),
		"stderr":    strings.TrimSuffix(stderr.String(), "\n"),
		"exit_code": exitCode,
		"action":    p.Action,
	}, nil
}

func mapGitActionToArgs(p GitCommandParams) []string {
	switch p.Action {
	case "status":
		return []string{"status", "--short", "--branch"}
	case "diff":
		return []string{"diff"}
	case "add":
		if path, ok := p.Params["path"].(string); ok && path != "" {
			return []string{"add", path}
		}
		return []string{"add", "."}
	case "commit":
		msg, _ := p.Params["message"].(string)
		if strings.TrimSpace(msg) == "" {
			return nil
		}
		return []string{"commit", "-m", msg}
	case "revParse":
		ref, _ := p.Params["ref"].(string)
		if ref == "" {
			ref = "HEAD"
		}
		return []string{"rev-parse", ref}
	case "checkout":
		ref, _ := p.Params["ref"].(string)
		if strings.TrimSpace(ref) == "" {
			return nil
		}
		return []string{"checkout", ref}
	default:
		return nil
	}
}
