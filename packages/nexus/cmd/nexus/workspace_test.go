package main

import (
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestRootCommandIncludesWorkspaceSubcommands(t *testing.T) {
	usage := rootCmd.UsageString()
	for _, name := range []string{
		"create", "list", "start", "stop", "shell", "exec", "run", "fork",
		"doctor", "init", "remove", "tunnel", "pause", "resume", "restore",
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

func TestRunWorkspaceTunnelCommandCallsApplyComposeRPC(t *testing.T) {
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
	closedTunnelID := ""

	ensureDaemonFn = func() (*websocket.Conn, error) {
		return nil, nil
	}
	daemonRPCFn = func(_ *websocket.Conn, method string, params interface{}, out interface{}) error {
		calledMethods = append(calledMethods, method)
		switch method {
		case "spotlight.applyComposePorts":
			payload, ok := params.(map[string]any)
			if !ok {
				t.Fatalf("expected map params, got %T", params)
			}
			calledID, _ := payload["workspaceId"].(string)
			if calledID != "ws-456" {
				t.Fatalf("expected workspace id ws-456, got %q", calledID)
			}
			if typed, ok := out.(*struct {
				Forwards []struct {
					ID         string `json:"id"`
					Service    string `json:"service"`
					Host       string `json:"host"`
					LocalPort  int    `json:"localPort"`
					RemotePort int    `json:"remotePort"`
				} `json:"forwards"`
				Errors []struct {
					Service    string `json:"service"`
					HostPort   int    `json:"hostPort"`
					TargetPort int    `json:"targetPort"`
					Message    string `json:"message"`
				} `json:"errors"`
			}); ok {
				typed.Forwards = append(typed.Forwards, struct {
					ID         string `json:"id"`
					Service    string `json:"service"`
					Host       string `json:"host"`
					LocalPort  int    `json:"localPort"`
					RemotePort int    `json:"remotePort"`
				}{ID: "tun-123", Service: "web", Host: "127.0.0.1", LocalPort: 8080, RemotePort: 80})
			}
		case "spotlight.close":
			payload, ok := params.(map[string]any)
			if !ok {
				t.Fatalf("expected map params, got %T", params)
			}
			closedTunnelID, _ = payload["id"].(string)
		default:
			t.Fatalf("unexpected rpc method %q", method)
		}
		return nil
	}

	tunnelWorkspace("ws-456")

	if len(calledMethods) != 2 {
		t.Fatalf("expected 2 rpc calls, got %d (%v)", len(calledMethods), calledMethods)
	}
	if calledMethods[0] != "spotlight.applyComposePorts" || calledMethods[1] != "spotlight.close" {
		t.Fatalf("unexpected rpc method sequence: %v", calledMethods)
	}
	if closedTunnelID != "tun-123" {
		t.Fatalf("expected closed tunnel id tun-123, got %q", closedTunnelID)
	}
}
