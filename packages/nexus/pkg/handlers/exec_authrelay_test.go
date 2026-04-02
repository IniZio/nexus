package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/authrelay"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

func TestHandleExecWithAuthRelay_InjectsEnvAndConsumesTokenOnce(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}

	broker := authrelay.NewBroker()
	token := broker.Mint("ws-1", map[string]string{"NEXUS_AUTH_VALUE": "secret"}, time.Minute)

	params, _ := json.Marshal(map[string]any{
		"workspaceId": "ws-1",
		"command":     "sh",
		"args":        []string{"-lc", "printf \"%s\" \"$NEXUS_AUTH_VALUE\""},
		"options": map[string]any{
			"authRelayToken": token,
		},
	})

	res, rpcErr := HandleExecWithAuthRelay(context.Background(), params, ws, broker)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if res.Stdout != "secret" {
		t.Fatalf("expected injected auth value, got %q", res.Stdout)
	}

	_, rpcErr = HandleExecWithAuthRelay(context.Background(), params, ws, broker)
	if rpcErr == nil {
		t.Fatal("expected second consume to fail")
	}
}

func TestHandleExecWithAuthRelay_RejectsWrongWorkspace(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}

	broker := authrelay.NewBroker()
	token := broker.Mint("ws-1", map[string]string{"NEXUS_AUTH_VALUE": "secret"}, time.Minute)

	params, _ := json.Marshal(map[string]any{
		"workspaceId": "ws-2",
		"command":     "sh",
		"args":        []string{"-lc", "printf \"%s\" \"$NEXUS_AUTH_VALUE\""},
		"options": map[string]any{
			"authRelayToken": token,
		},
	})

	_, rpcErr := HandleExecWithAuthRelay(context.Background(), params, ws, broker)
	if rpcErr == nil {
		t.Fatal("expected auth relay token rejection for mismatched workspace")
	}
}
