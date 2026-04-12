package handlers

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/authrelay"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/safeenv"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

const (
	DefaultTimeout = 30 * time.Second
	MaxTimeout     = 5 * time.Minute
)

type ExecParams struct {
	WorkspaceID string      `json:"workspaceId,omitempty"`
	Command     string      `json:"command"`
	Args        []string    `json:"args"`
	Options     ExecOptions `json:"options"`
}

type ExecOptions struct {
	Timeout        int64    `json:"timeout"`
	WorkDir        string   `json:"work_dir"`
	Env            []string `json:"env"`
	AuthRelayToken string   `json:"authRelayToken,omitempty"`
}

type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Command  string `json:"command"`
}

func HandleExec(ctx context.Context, req ExecParams, ws *workspace.Workspace) (*ExecResult, *rpckit.RPCError) {
	return HandleExecWithAuthRelay(ctx, req, ws, nil)
}

func HandleExecWithAuthRelay(ctx context.Context, req ExecParams, ws *workspace.Workspace, broker *authrelay.Broker) (*ExecResult, *rpckit.RPCError) {
	if req.Command == "" {
		return nil, rpckit.ErrInvalidParams
	}

	execCtx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	if req.Options.Timeout > 0 {
		timeout := time.Duration(req.Options.Timeout) * time.Second
		if timeout > MaxTimeout {
			timeout = MaxTimeout
		}
		var cancelFn context.CancelFunc
		execCtx, cancelFn = context.WithTimeout(execCtx, timeout)
		defer cancelFn()
	}

	workDir := ws.Path()
	if req.Options.WorkDir != "" {
		safePath, err := ws.SecurePath(req.Options.WorkDir)
		if err != nil {
			return nil, rpckit.ErrInvalidPath
		}
		workDir = safePath
	}

	args := req.Args
	if args == nil {
		parts := strings.Fields(req.Command)
		if len(parts) > 0 {
			req.Command = parts[0]
			args = parts[1:]
		}
	}

	cmd := exec.CommandContext(execCtx, req.Command, args...)
	cmd.Dir = workDir

	cmd.Env = safeenv.Base()
	if req.Options.Env != nil {
		cmd.Env = append(cmd.Env, req.Options.Env...)
	}

	if req.Options.AuthRelayToken != "" {
		if broker == nil {
			return nil, rpckit.ErrAuthRelayInvalid
		}
		if req.WorkspaceID == "" {
			return nil, rpckit.ErrInvalidParams
		}
		injected, ok := broker.Consume(req.Options.AuthRelayToken, req.WorkspaceID)
		if !ok {
			return nil, rpckit.ErrAuthRelayInvalid
		}
		cmd.Env = append(cmd.Env, toEnvPairs(injected)...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmdErr := cmd.Run()

	if execCtx.Err() == context.DeadlineExceeded {
		return nil, rpckit.ErrTimeout
	}

	exitCode := 0
	if cmdErr != nil {
		if exitError, ok := cmdErr.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		}
	}

	result := &ExecResult{
		Stdout:   strings.TrimSuffix(stdout.String(), "\n"),
		Stderr:   strings.TrimSuffix(stderr.String(), "\n"),
		ExitCode: exitCode,
	}

	if len(args) > 0 {
		result.Command = fmt.Sprintf("%s %s", req.Command, strings.Join(args, " "))
	} else {
		result.Command = req.Command
	}

	return result, nil
}

func toEnvPairs(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, env[k]))
	}
	return pairs
}
