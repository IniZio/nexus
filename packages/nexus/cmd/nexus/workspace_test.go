package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestPrintUsageIncludesFlatWorkspaceCommands(t *testing.T) {
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stderr = w

	printUsage()

	_ = w.Close()
	os.Stderr = oldStderr

	buf, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read usage output: %v", err)
	}

	got := string(buf)
	if !strings.Contains(got, "nexus <list|create|start|stop|remove|fork|ssh|tunnel>") {
		t.Fatalf("expected usage to include flattened workspace command list, got %q", got)
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

	runWorkspaceStartCommand([]string{"ws-123"})

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

	runWorkspaceTunnelCommand([]string{"ws-456"})

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
