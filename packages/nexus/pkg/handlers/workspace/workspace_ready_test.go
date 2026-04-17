package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/services"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

func TestHandleWorkspaceReady_Immediate(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	mgr := services.NewManager()

	res, rpcErr := HandleWorkspaceReady(context.Background(), WorkspaceReadyParams{
		WorkspaceID: "ws-1",
		Checks: []WorkspaceReadyCheck{
			{Name: "api", Command: "sh", Args: []string{"-lc", "exit 0"}},
		},
		TimeoutMs:  500,
		IntervalMs: 50,
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !res.Ready {
		t.Fatalf("expected ready=true, got %#v", res)
	}
}

func TestHandleWorkspaceReady_TimesOut(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	mgr := services.NewManager()

	start := time.Now()
	res, rpcErr := HandleWorkspaceReady(context.Background(), WorkspaceReadyParams{
		WorkspaceID: "ws-1",
		Checks: []WorkspaceReadyCheck{
			{Name: "api", Command: "sh", Args: []string{"-lc", "exit 1"}},
		},
		TimeoutMs:  200,
		IntervalMs: 50,
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if res.Ready {
		t.Fatalf("expected ready=false, got %#v", res)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("expected to wait until timeout; elapsed=%s", time.Since(start))
	}
}

func TestHandleWorkspaceReady_Profile_InternalDogfoodDefault(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	mgr := services.NewManager()

	for _, name := range []string{"student-portal", "api", "opencode-acp"} {
		_, err = mgr.Start(context.Background(), "ws-1", name, ws.Path(), "sleep", []string{"2"}, services.StartOptions{})
		if err != nil {
			t.Fatalf("start service %s: %v", name, err)
		}
	}

	res, rpcErr := HandleWorkspaceReady(context.Background(), WorkspaceReadyParams{
		WorkspaceID: "ws-1",
		Profile:     "default-services",
		TimeoutMs:   200,
		IntervalMs:  50,
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if res.Profile != "default-services" {
		t.Fatalf("expected profile default-services, got %q", res.Profile)
	}

	for _, name := range []string{"student-portal", "api", "opencode-acp"} {
		_ = mgr.Stop("ws-1", name)
	}
}

func TestHandleWorkspaceReady_Profile_Unknown(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	mgr := services.NewManager()

	_, rpcErr := HandleWorkspaceReady(context.Background(), WorkspaceReadyParams{
		WorkspaceID: "ws-1",
		Profile:     "unknown-profile",
	}, ws, mgr)
	if rpcErr == nil {
		t.Fatal("expected invalid params for unknown readiness profile")
	}
}

func TestHandleWorkspaceReady_OpencodeACPOptionalWhenBinaryMissing(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	mgr := services.NewManager()

	origAvailable := opencodeAvailable
	origStart := startOpencodeACP
	t.Cleanup(func() {
		opencodeAvailable = origAvailable
		startOpencodeACP = origStart
	})

	opencodeAvailable = func() bool { return false }
	startOpencodeACP = func(context.Context, *services.Manager, string, string) error {
		t.Fatal("startOpencodeACP should not be called when opencode is unavailable")
		return nil
	}

	for _, name := range []string{"student-portal", "api"} {
		_, err = mgr.Start(context.Background(), "ws-1", name, ws.Path(), "sleep", []string{"2"}, services.StartOptions{})
		if err != nil {
			t.Fatalf("start service %s: %v", name, err)
		}
	}

	res, rpcErr := HandleWorkspaceReady(context.Background(), WorkspaceReadyParams{
		WorkspaceID: "ws-1",
		Profile:     "default-services",
		TimeoutMs:   200,
		IntervalMs:  50,
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !res.Ready {
		t.Fatalf("expected ready=true when opencode unavailable; got %#v", res)
	}

	for _, name := range []string{"student-portal", "api"} {
		_ = mgr.Stop("ws-1", name)
	}
}

func TestHandleWorkspaceReady_StartsOpencodeACPWhenAvailable(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	mgr := services.NewManager()

	origAvailable := opencodeAvailable
	origStart := startOpencodeACP
	t.Cleanup(func() {
		opencodeAvailable = origAvailable
		startOpencodeACP = origStart
	})

	started := false
	opencodeAvailable = func() bool { return true }
	startOpencodeACP = func(ctx context.Context, mgr *services.Manager, workspaceID, rootPath string) error {
		started = true
		_, err := mgr.Start(ctx, workspaceID, opencodeACPServiceName, rootPath, "sleep", []string{"2"}, services.StartOptions{})
		return err
	}

	for _, name := range []string{"student-portal", "api"} {
		_, err = mgr.Start(context.Background(), "ws-1", name, ws.Path(), "sleep", []string{"2"}, services.StartOptions{})
		if err != nil {
			t.Fatalf("start service %s: %v", name, err)
		}
	}

	res, rpcErr := HandleWorkspaceReady(context.Background(), WorkspaceReadyParams{
		WorkspaceID: "ws-1",
		Profile:     "default-services",
		TimeoutMs:   500,
		IntervalMs:  50,
	}, ws, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !res.Ready {
		t.Fatalf("expected ready=true, got %#v", res)
	}
	if !started {
		t.Fatal("expected opencode ACP start helper to be invoked")
	}

	for _, name := range []string{"student-portal", "api", opencodeACPServiceName} {
		_ = mgr.Stop("ws-1", name)
	}
}
