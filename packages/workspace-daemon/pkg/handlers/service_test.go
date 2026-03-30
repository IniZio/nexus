package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/services"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/workspace"
)

func TestHandleServiceCommand_StartStatusStop(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}

	mgr := services.NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startParams, _ := json.Marshal(map[string]interface{}{
		"workspaceId": "ws-1",
		"action":      "start",
		"params": map[string]interface{}{
			"name":    "probe",
			"command": "sleep",
			"args":    []string{"2"},
		},
	})

	startRes, rpcErr := HandleServiceCommand(ctx, startParams, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("start rpc error: %+v", rpcErr)
	}
	if running, _ := startRes["running"].(bool); !running {
		t.Fatal("expected running true")
	}

	statusParams, _ := json.Marshal(map[string]interface{}{
		"workspaceId": "ws-1",
		"action":      "status",
		"params": map[string]interface{}{
			"name": "probe",
		},
	})
	statusRes, rpcErr := HandleServiceCommand(ctx, statusParams, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("status rpc error: %+v", rpcErr)
	}
	if running, _ := statusRes["running"].(bool); !running {
		t.Fatal("expected service running")
	}

	stopParams, _ := json.Marshal(map[string]interface{}{
		"workspaceId": "ws-1",
		"action":      "stop",
		"params": map[string]interface{}{
			"name": "probe",
		},
	})
	stopRes, rpcErr := HandleServiceCommand(ctx, stopParams, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("stop rpc error: %+v", rpcErr)
	}
	if stopped, _ := stopRes["stopped"].(bool); !stopped {
		t.Fatal("expected stopped true")
	}
	if forced, ok := stopRes["forced"].(bool); !ok || forced {
		t.Fatal("expected forced=false for graceful stop")
	}
}

func TestHandleServiceCommand_Restart(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	mgr := services.NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startParams, _ := json.Marshal(map[string]interface{}{
		"workspaceId": "ws-1",
		"action":      "start",
		"params": map[string]interface{}{
			"name":    "probe",
			"command": "sleep",
			"args":    []string{"2"},
		},
	})
	_, rpcErr := HandleServiceCommand(ctx, startParams, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("start rpc error: %+v", rpcErr)
	}

	restartParams, _ := json.Marshal(map[string]interface{}{
		"workspaceId": "ws-1",
		"action":      "restart",
		"params": map[string]interface{}{
			"name":    "probe",
			"command": "sleep",
			"args":    []string{"2"},
		},
	})
	restartRes, rpcErr := HandleServiceCommand(ctx, restartParams, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("restart rpc error: %+v", rpcErr)
	}
	if running, _ := restartRes["running"].(bool); !running {
		t.Fatal("expected running true on restart")
	}
}

func TestHandleServiceCommand_StopTimeoutOption(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	mgr := services.NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startParams, _ := json.Marshal(map[string]interface{}{
		"workspaceId": "ws-1",
		"action":      "start",
		"params": map[string]interface{}{
			"name":    "probe",
			"command": "sleep",
			"args":    []string{"2"},
		},
	})
	_, rpcErr := HandleServiceCommand(ctx, startParams, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("start rpc error: %+v", rpcErr)
	}

	stopParams, _ := json.Marshal(map[string]interface{}{
		"workspaceId": "ws-1",
		"action":      "stop",
		"params": map[string]interface{}{
			"name":          "probe",
			"stopTimeoutMs": 10,
		},
	})
	stopRes, rpcErr := HandleServiceCommand(ctx, stopParams, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("stop rpc error: %+v", rpcErr)
	}
	if stopped, _ := stopRes["stopped"].(bool); !stopped {
		t.Fatal("expected stopped=true")
	}
}
