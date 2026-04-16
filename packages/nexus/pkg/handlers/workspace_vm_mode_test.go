package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestEnsureLocalRuntimeWorkspace_SetsVMDedicatedModeForLima(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("mkdir .nexus: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".nexus", "workspace.json"), []byte(`{"version":1,"isolation":{"level":"vm","vm":{"mode":"dedicated"}}}`), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}

	ws := &workspacemgr.Workspace{
		ID:            "ws-vm-mode",
		Backend:       "firecracker",
		Repo:          repoRoot,
		WorkspaceName: "vm-mode-test",
	}

	var gotReq runtime.CreateRequest
	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{
			"firecracker": &mockDriver{
				backend: "firecracker",
				createFn: func(_ context.Context, req runtime.CreateRequest) error {
					gotReq = req
					return nil
				},
			},
		},
	)

	rpcErr := ensureLocalRuntimeWorkspace(context.Background(), ws, factory, nil, "")
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	wantMode := "dedicated"
	if gotReq.Options["vm.mode"] != wantMode {
		t.Fatalf("expected vm.mode=%s, got %q", wantMode, gotReq.Options["vm.mode"])
	}
}
