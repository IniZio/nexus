package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/services"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

func TestHandleServiceCommand_StartStatusStop(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}

	mgr := services.NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startRes, rpcErr := HandleServiceCommand(ctx, ServiceCommandParams{
		WorkspaceID: "ws-1",
		Action:      "start",
		Params: map[string]interface{}{
			"name":    "probe",
			"command": "sleep",
			"args":    []interface{}{"2"},
		},
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("start rpc error: %+v", rpcErr)
	}
	if running, _ := startRes["running"].(bool); !running {
		t.Fatal("expected running true")
	}

	statusRes, rpcErr := HandleServiceCommand(ctx, ServiceCommandParams{
		WorkspaceID: "ws-1",
		Action:      "status",
		Params: map[string]interface{}{
			"name": "probe",
		},
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("status rpc error: %+v", rpcErr)
	}
	if running, _ := statusRes["running"].(bool); !running {
		t.Fatal("expected service running")
	}

	stopRes, rpcErr := HandleServiceCommand(ctx, ServiceCommandParams{
		WorkspaceID: "ws-1",
		Action:      "stop",
		Params: map[string]interface{}{
			"name": "probe",
		},
	}, ws, mgr)
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

	_, rpcErr := HandleServiceCommand(ctx, ServiceCommandParams{
		WorkspaceID: "ws-1",
		Action:      "start",
		Params: map[string]interface{}{
			"name":    "probe",
			"command": "sleep",
			"args":    []interface{}{"2"},
		},
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("start rpc error: %+v", rpcErr)
	}

	restartRes, rpcErr := HandleServiceCommand(ctx, ServiceCommandParams{
		WorkspaceID: "ws-1",
		Action:      "restart",
		Params: map[string]interface{}{
			"name":    "probe",
			"command": "sleep",
			"args":    []interface{}{"2"},
		},
	}, ws, mgr)
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

	_, rpcErr := HandleServiceCommand(ctx, ServiceCommandParams{
		WorkspaceID: "ws-1",
		Action:      "start",
		Params: map[string]interface{}{
			"name":    "probe",
			"command": "sleep",
			"args":    []interface{}{"2"},
		},
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("start rpc error: %+v", rpcErr)
	}

	stopRes, rpcErr := HandleServiceCommand(ctx, ServiceCommandParams{
		WorkspaceID: "ws-1",
		Action:      "stop",
		Params: map[string]interface{}{
			"name":          "probe",
			"stopTimeoutMs": 10,
		},
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("stop rpc error: %+v", rpcErr)
	}
	if stopped, _ := stopRes["stopped"].(bool); !stopped {
		t.Fatal("expected stopped=true")
	}
}
