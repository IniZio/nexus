package handlers

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/config"
	rpckit "github.com/nexus/nexus/packages/workspace-daemon/pkg/rpcerrors"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/services"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/workspace"
)

const opencodeACPServiceName = "opencode-acp"

var opencodeAvailable = func() bool {
	_, err := exec.LookPath("opencode")
	return err == nil
}

var startOpencodeACP = func(ctx context.Context, svcMgr *services.Manager, workspaceID, rootPath string) error {
	_, err := svcMgr.Start(ctx, workspaceID, opencodeACPServiceName, rootPath, "opencode", []string{"serve", "--hostname", "127.0.0.1", "--port", "4096"}, services.StartOptions{})
	if err != nil {
		status := svcMgr.Status(workspaceID, opencodeACPServiceName)
		running, _ := status["running"].(bool)
		if running {
			return nil
		}
	}
	return err
}

type WorkspaceReadyCheck struct {
	Name          string   `json:"name"`
	Type          string   `json:"type,omitempty"`
	Command       string   `json:"command,omitempty"`
	Args          []string `json:"args,omitempty"`
	ServiceName   string   `json:"serviceName,omitempty"`
	ExpectRunning *bool    `json:"expectRunning,omitempty"`
}

type WorkspaceReadyParams struct {
	WorkspaceID string                `json:"workspaceId,omitempty"`
	Profile     string                `json:"profile,omitempty"`
	Checks      []WorkspaceReadyCheck `json:"checks"`
	TimeoutMs   int                   `json:"timeoutMs,omitempty"`
	IntervalMs  int                   `json:"intervalMs,omitempty"`
}

type WorkspaceReadyResult struct {
	Ready       bool           `json:"ready"`
	WorkspaceID string         `json:"workspaceId"`
	Profile     string         `json:"profile,omitempty"`
	ElapsedMs   int64          `json:"elapsedMs"`
	Attempts    int            `json:"attempts"`
	LastResults map[string]int `json:"lastResults"`
}

func HandleWorkspaceReady(ctx context.Context, params json.RawMessage, ws *workspace.Workspace, svcMgr *services.Manager) (*WorkspaceReadyResult, *rpckit.RPCError) {
	var p WorkspaceReadyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}
	if p.Profile != "" {
		checks, ok := readinessProfileForWorkspace(ws.Path(), p.Profile)
		if !ok {
			return nil, rpckit.ErrInvalidParams
		}
		p.Checks = checks
	}

	if len(p.Checks) == 0 {
		return nil, rpckit.ErrInvalidParams
	}

	timeout := 2000 * time.Millisecond
	if p.TimeoutMs > 0 {
		timeout = time.Duration(p.TimeoutMs) * time.Millisecond
	}
	interval := 100 * time.Millisecond
	if p.IntervalMs > 0 {
		interval = time.Duration(p.IntervalMs) * time.Millisecond
	}

	workspaceID := p.WorkspaceID
	if workspaceID == "" {
		workspaceID = ws.ID()
	}

	start := time.Now()
	deadline := start.Add(timeout)
	attempts := 0
	last := map[string]int{}

	for {
		attempts++
		allOK := true
		for _, check := range p.Checks {
			code, ok := runReadinessCheck(ctx, check, ws, workspaceID, svcMgr)
			last[check.Name] = code
			if !ok {
				allOK = false
			}
		}

		if allOK {
			return &WorkspaceReadyResult{
				Ready:       true,
				WorkspaceID: workspaceID,
				Profile:     p.Profile,
				ElapsedMs:   time.Since(start).Milliseconds(),
				Attempts:    attempts,
				LastResults: last,
			}, nil
		}

		if time.Now().After(deadline) {
			return &WorkspaceReadyResult{
				Ready:       false,
				WorkspaceID: workspaceID,
				Profile:     p.Profile,
				ElapsedMs:   time.Since(start).Milliseconds(),
				Attempts:    attempts,
				LastResults: last,
			}, nil
		}

		select {
		case <-ctx.Done():
			return nil, rpckit.ErrTimeout
		case <-time.After(interval):
		}
	}
}

func runReadinessCheck(ctx context.Context, check WorkspaceReadyCheck, ws *workspace.Workspace, workspaceID string, svcMgr *services.Manager) (int, bool) {
	switch checkType(check) {
	case "service":
		if check.ServiceName == "" || svcMgr == nil {
			return -1, false
		}
		if check.ServiceName == opencodeACPServiceName {
			if !opencodeAvailable() {
				return 0, true
			}

			status := svcMgr.Status(workspaceID, check.ServiceName)
			running, _ := status["running"].(bool)
			expected := true
			if check.ExpectRunning != nil {
				expected = *check.ExpectRunning
			}
			if expected && !running {
				if err := startOpencodeACP(ctx, svcMgr, workspaceID, ws.Path()); err != nil {
					return -1, false
				}
			}
		}

		status := svcMgr.Status(workspaceID, check.ServiceName)
		running, _ := status["running"].(bool)
		expected := true
		if check.ExpectRunning != nil {
			expected = *check.ExpectRunning
		}
		if running == expected {
			return 0, true
		}
		return 1, false
	case "command":
		if check.Command == "" {
			return -1, false
		}
		res, rpcErr := HandleExec(ctx, mustJSON(map[string]any{
			"command": check.Command,
			"args":    check.Args,
		}), ws)
		if rpcErr != nil {
			return -1, false
		}
		if res.ExitCode == 0 {
			return 0, true
		}
		return res.ExitCode, false
	default:
		return -1, false
	}
}

func checkType(check WorkspaceReadyCheck) string {
	if check.Type != "" {
		return check.Type
	}
	if check.ServiceName != "" {
		return "service"
	}
	return "command"
}

func readinessProfiles() map[string][]WorkspaceReadyCheck {
	return map[string][]WorkspaceReadyCheck{
		"default-services": {
			{Name: "student-portal", Type: "service", ServiceName: "student-portal"},
			{Name: "api", Type: "service", ServiceName: "api"},
			{Name: "opencode-acp", Type: "service", ServiceName: opencodeACPServiceName},
		},
	}
}

func readinessProfileForWorkspace(root, profile string) ([]WorkspaceReadyCheck, bool) {
	if root != "" {
		cfg, _, err := config.LoadWorkspaceConfig(root)
		if err == nil {
			if checks, ok := cfg.Readiness.Profiles[profile]; ok {
				return mapConfigChecks(checks), true
			}
		}

		cfg2, _, err := config.LoadWorkspaceConfig(filepath.Clean(root))
		if err == nil {
			if checks, ok := cfg2.Readiness.Profiles[profile]; ok {
				return mapConfigChecks(checks), true
			}
		}
	}

	checks, ok := readinessProfiles()[profile]
	return checks, ok
}

func mapConfigChecks(in []config.ReadinessCheck) []WorkspaceReadyCheck {
	out := make([]WorkspaceReadyCheck, 0, len(in))
	for _, c := range in {
		out = append(out, WorkspaceReadyCheck{
			Name:          c.Name,
			Type:          c.Type,
			Command:       c.Command,
			Args:          c.Args,
			ServiceName:   c.ServiceName,
			ExpectRunning: c.ExpectRunning,
		})
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
