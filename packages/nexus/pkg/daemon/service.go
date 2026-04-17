package daemon

import (
	"context"
	"time"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/services"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

type ServiceCommandParams struct {
	WorkspaceID string                 `json:"workspaceId,omitempty"`
	Action      string                 `json:"action"`
	Params      map[string]interface{} `json:"params,omitempty"`
}

func HandleServiceCommand(ctx context.Context, p ServiceCommandParams, ws *workspace.Workspace, mgr *services.Manager) (map[string]interface{}, *rpckit.RPCError) {
	if p.Action == "" {
		return nil, rpckit.ErrInvalidParams
	}

	workspaceID := p.WorkspaceID
	if workspaceID == "" {
		workspaceID = ws.ID()
	}

	svcName, _ := p.Params["name"].(string)
	opts := parseStartOptions(p.Params)

	switch p.Action {
	case "start":
		command, _ := p.Params["command"].(string)
		args := []string{}
		if rawArgs, ok := p.Params["args"].([]interface{}); ok {
			for _, arg := range rawArgs {
				if s, ok := arg.(string); ok {
					args = append(args, s)
				}
			}
		}
		proc, err := mgr.Start(ctx, workspaceID, svcName, ws.Path(), command, args, opts)
		if err != nil {
			return nil, rpckit.ErrInvalidParams
		}
		return map[string]interface{}{
			"running": true,
			"name":    proc.Name,
			"pid":     proc.PID,
		}, nil
	case "restart":
		command, _ := p.Params["command"].(string)
		args := []string{}
		if rawArgs, ok := p.Params["args"].([]interface{}); ok {
			for _, arg := range rawArgs {
				if s, ok := arg.(string); ok {
					args = append(args, s)
				}
			}
		}
		proc, err := mgr.Restart(ctx, workspaceID, svcName, ws.Path(), command, args, opts)
		if err != nil {
			return nil, rpckit.ErrInvalidParams
		}
		return map[string]interface{}{
			"running": true,
			"name":    proc.Name,
			"pid":     proc.PID,
		}, nil
	case "stop":
		res := mgr.StopWithTimeout(workspaceID, svcName, opts.StopTimeout)
		return map[string]interface{}{"stopped": res.Stopped, "forced": res.Forced}, nil
	case "status":
		return mgr.Status(workspaceID, svcName), nil
	case "logs":
		return mgr.Logs(workspaceID, svcName), nil
	default:
		return nil, rpckit.ErrInvalidParams
	}
}

func parseStartOptions(params map[string]interface{}) services.StartOptions {
	var opts services.StartOptions

	if raw, ok := params["stopTimeoutMs"]; ok {
		switch v := raw.(type) {
		case float64:
			opts.StopTimeout = time.Duration(v) * time.Millisecond
		case int:
			opts.StopTimeout = time.Duration(v) * time.Millisecond
		}
	}

	if raw, ok := params["autoRestart"].(bool); ok {
		opts.AutoRestart = raw
	}

	if raw, ok := params["maxRestarts"]; ok {
		switch v := raw.(type) {
		case float64:
			opts.MaxRestarts = int(v)
		case int:
			opts.MaxRestarts = v
		}
	}

	if raw, ok := params["restartDelayMs"]; ok {
		switch v := raw.(type) {
		case float64:
			opts.RestartDelay = time.Duration(v) * time.Millisecond
		case int:
			opts.RestartDelay = time.Duration(v) * time.Millisecond
		}
	}

	return opts
}
