package handlers

import (
	"context"
	"encoding/json"
	"os"
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

	pathParams, _ := json.Marshal(map[string]any{
		"workspaceId": "ws-1",
		"command":     "sh",
		"args":        []string{"-lc", "test -n \"$PATH\" && echo path-ok"},
		"options": map[string]any{
			"authRelayToken": broker.Mint("ws-1", map[string]string{"NEXUS_AUTH_VALUE": "x"}, time.Minute),
		},
	})
	pathRes, pathErr := HandleExecWithAuthRelay(context.Background(), pathParams, ws, broker)
	if pathErr != nil {
		t.Fatalf("path check rpc error: %+v", pathErr)
	}
	if pathRes.ExitCode != 0 || pathRes.Stdout != "path-ok" {
		t.Fatalf("expected PATH available in auth relay exec, got exit=%d stdout=%q stderr=%q", pathRes.ExitCode, pathRes.Stdout, pathRes.Stderr)
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

func TestHandleExecWithAuthRelay_DoesNotInheritDaemonSecretEnv(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := os.Setenv("NEXUS_DAEMON_SIDE_SECRET_TEST", "should-not-leak"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("NEXUS_DAEMON_SIDE_SECRET_TEST") })

	params, _ := json.Marshal(map[string]any{
		"command": "sh",
		"args":    []string{"-lc", "printf \"%s\" \"${NEXUS_DAEMON_SIDE_SECRET_TEST:-}\""},
	})

	res, rpcErr := HandleExecWithAuthRelay(context.Background(), params, ws, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if res.Stdout != "" {
		t.Fatalf("expected daemon secret env to be absent, got %q", res.Stdout)
	}
}
