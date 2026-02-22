package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	rpckit "github.com/nexus/nexus/packages/nexusd/pkg/rpcerrors"
	"github.com/nexus/nexus/packages/nexusd/pkg/workspace"
	"github.com/nexus/nexus/packages/nexusd/internal/interfaces"
)

const (
	DefaultTimeout = 30 * time.Second
	MaxTimeout     = 5 * time.Minute
)

type ExecParams struct {
	Command string      `json:"command"`
	Args    []string    `json:"args"`
	Options ExecOptions `json:"options"`
}

type ExecOptions struct {
	Timeout int64    `json:"timeout"`
	WorkDir string   `json:"work_dir"`
	Env     []string `json:"env"`
}

type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Command  string `json:"command"`
}

func HandleExec(ctx context.Context, params json.RawMessage, ws *workspace.Workspace, backend interfaces.Backend) (*ExecResult, *rpckit.RPCError) {
	var p ExecParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	if p.Command == "" {
		return nil, rpckit.ErrInvalidParams
	}

	execCtx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	if p.Options.Timeout > 0 {
		timeout := time.Duration(p.Options.Timeout) * time.Second
		if timeout > MaxTimeout {
			timeout = MaxTimeout
		}
		var cancelFn context.CancelFunc
		execCtx, cancelFn = context.WithTimeout(execCtx, timeout)
		defer cancelFn()
	}

	if backend != nil {
		workspaceID := ws.ID()
		cmd := []string{p.Command}
		cmd = append(cmd, p.Args...)

		output, err := backend.ExecViaSSH(execCtx, workspaceID, cmd)
		if err != nil {
			return &ExecResult{
				Stdout:   "",
				Stderr:   fmt.Sprintf("exec in container failed: %v", err),
				ExitCode: 1,
				Command:  strings.Join(cmd, " "),
			}, nil
		}

		return &ExecResult{
			Stdout:   strings.TrimSuffix(output, "\n"),
			Stderr:   "",
			ExitCode: 0,
			Command:  strings.Join(cmd, " "),
		}, nil
	}

	workDir := ws.Path()
	if p.Options.WorkDir != "" {
		safePath, err := ws.SecurePath(p.Options.WorkDir)
		if err != nil {
			return nil, rpckit.ErrInvalidPath
		}
		workDir = safePath
	}

	args := p.Args
	if args == nil {
		parts := strings.Fields(p.Command)
		if len(parts) > 0 {
			p.Command = parts[0]
			args = parts[1:]
		}
	}

	cmd := exec.CommandContext(execCtx, p.Command, args...)
	cmd.Dir = workDir

	if p.Options.Env != nil {
		cmd.Env = append(cmd.Env, p.Options.Env...)
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
		result.Command = fmt.Sprintf("%s %s", p.Command, strings.Join(args, " "))
	} else {
		result.Command = p.Command
	}

	return result, nil
}
