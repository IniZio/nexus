package handlers

import (
	"context"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/store"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestEnsureLocalRuntimeWorkspaceAppliesSandboxResourcePolicy(t *testing.T) {
	repoRoot := t.TempDir()
	ws := &workspacemgr.Workspace{
		ID:            "ws-policy",
		Backend:       "firecracker",
		Repo:          repoRoot,
		WorkspaceName: "policy-test",
	}
	mgr := workspacemgr.NewManager(t.TempDir())
	if err := mgr.SandboxResourceSettingsRepository().UpsertSandboxResourceSettings(store.SandboxResourceSettingsRow{
		DefaultMemoryMiB: 2048,
		DefaultVCPUs:     2,
		MaxMemoryMiB:     1536,
		MaxVCPUs:         1,
	}); err != nil {
		t.Fatalf("upsert sandbox resource settings: %v", err)
	}

	var gotReq runtime.CreateRequest
	factory := runtime.NewFactory(
		[]runtime.Capability{
			{Name: "runtime.linux", Available: true},
			{Name: "runtime.firecracker", Available: true},
		},
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

	rpcErr := ensureLocalRuntimeWorkspace(context.Background(), ws, factory, mgr, "")
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if gotReq.Options["mem_mib"] != "1536" {
		t.Fatalf("expected mem_mib to be clamped to 1536, got %q", gotReq.Options["mem_mib"])
	}
	if gotReq.Options["vcpus"] != "1" {
		t.Fatalf("expected vcpus to be clamped to 1, got %q", gotReq.Options["vcpus"])
	}
}
