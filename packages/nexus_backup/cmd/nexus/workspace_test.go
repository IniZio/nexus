package main

import (
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestRootCommandIncludesSandboxCommand(t *testing.T) {
	usage := rootCmd.UsageString()
	for _, name := range []string{
		"sandbox", "run", "doctor", "init",
		"version", "update",
	} {
		if !strings.Contains(usage, name) {
			t.Errorf("usage missing subcommand %q", name)
		}
	}
}

func TestRunWorkspaceStartCommandCallsWorkspaceStartRPC(t *testing.T) {
	origEnsure := ensureDaemonFn
	origRPC := daemonRPCFn
	t.Cleanup(func() {
		ensureDaemonFn = origEnsure
		daemonRPCFn = origRPC
	})

	calledMethod := ""
	calledID := ""

	ensureDaemonFn = func() (*websocket.Conn, error) {
		return nil, nil
	}
	daemonRPCFn = func(_ *websocket.Conn, method string, params interface{}, out interface{}) error {
		calledMethod = method
		payload, ok := params.(map[string]any)
		if !ok {
			t.Fatalf("expected map params, got %T", params)
		}
		calledID, _ = payload["id"].(string)
		return nil
	}

	startWorkspace("ws-123")

	if calledMethod != "workspace.start" {
		t.Fatalf("expected workspace.start method, got %q", calledMethod)
	}
	if calledID != "ws-123" {
		t.Fatalf("expected workspace id ws-123, got %q", calledID)
	}
}

func TestRunWorkspaceTunnelCommandActivatesAndDeactivates(t *testing.T) {
	origEnsure := ensureDaemonFn
	origRPC := daemonRPCFn
	origWait := waitForInterruptFn
	t.Cleanup(func() {
		ensureDaemonFn = origEnsure
		daemonRPCFn = origRPC
		waitForInterruptFn = origWait
	})

	waitForInterruptFn = func() {}
	calledMethods := make([]string, 0, 2)

	ensureDaemonFn = func() (*websocket.Conn, error) {
		return nil, nil
	}
	daemonRPCFn = func(_ *websocket.Conn, method string, params interface{}, out interface{}) error {
		calledMethods = append(calledMethods, method)
		switch method {
		case "workspace.tunnels.activate":
			payload, ok := params.(map[string]any)
			if !ok {
				t.Fatalf("expected map params, got %T", params)
			}
			calledID, _ := payload["workspaceId"].(string)
			if calledID != "ws-456" {
				t.Fatalf("expected workspace id ws-456, got %q", calledID)
			}
			if typed, ok := out.(*struct {
				Active            bool   `json:"active"`
				ActiveWorkspaceID string `json:"activeWorkspaceId"`
			}); ok {
				typed.Active = true
				typed.ActiveWorkspaceID = "ws-456"
			}
		case "workspace.tunnels.deactivate":
			payload, ok := params.(map[string]any)
			if !ok {
				t.Fatalf("expected map params, got %T", params)
			}
			calledID, _ := payload["workspaceId"].(string)
			if calledID != "ws-456" {
				t.Fatalf("expected workspace id ws-456, got %q", calledID)
			}
		default:
			t.Fatalf("unexpected rpc method %q", method)
		}
		return nil
	}

	tunnelWorkspace("ws-456")

	if len(calledMethods) != 2 {
		t.Fatalf("expected 2 rpc calls, got %d (%v)", len(calledMethods), calledMethods)
	}
	if calledMethods[0] != "workspace.tunnels.activate" || calledMethods[1] != "workspace.tunnels.deactivate" {
		t.Fatalf("unexpected rpc method sequence: %v", calledMethods)
	}
}

func TestCreateWorkspaceLocalWorktreePath_UsesDaemonPath(t *testing.T) {
	ws := workspacemgr.Workspace{
		LocalWorktreePath: "/Users/newman/magic/hanlun-lms/.worktrees/main",
		Repo:              "/Users/newman/magic/hanlun-lms",
	}
	got := createWorkspaceLocalWorktreePath(ws)
	if got != ws.LocalWorktreePath {
		t.Fatalf("expected daemon local worktree path %q, got %q", ws.LocalWorktreePath, got)
	}
}

func TestCreateWorkspaceLocalWorktreePath_EmptyWhenUnset(t *testing.T) {
	ws := workspacemgr.Workspace{}
	got := createWorkspaceLocalWorktreePath(ws)
	if got != "" {
		t.Fatalf("expected empty local worktree path, got %q", got)
	}
}
