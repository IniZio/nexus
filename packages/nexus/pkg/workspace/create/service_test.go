package create

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type stubDriver struct {
	backend string
}

func (d *stubDriver) Backend() string { return d.backend }
func (d *stubDriver) Create(context.Context, runtime.CreateRequest) error {
	return nil
}
func (d *stubDriver) Start(context.Context, string) error   { return nil }
func (d *stubDriver) Stop(context.Context, string) error    { return nil }
func (d *stubDriver) Restore(context.Context, string) error { return nil }
func (d *stubDriver) Pause(context.Context, string) error   { return nil }
func (d *stubDriver) Resume(context.Context, string) error  { return nil }
func (d *stubDriver) Fork(context.Context, string, string) error {
	return nil
}
func (d *stubDriver) Destroy(context.Context, string) error { return nil }

func TestPrepareCreate_UsesLocalBackendWhenWorkspaceConfigEnablesLocalDriver(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nexus"), 0o755); err != nil {
		t.Fatalf("mkdir .nexus: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repo, ".nexus", "workspace.json"),
		[]byte(`{"version":1,"internalFeatures":{"localDriver":true}}`),
		0o644,
	); err != nil {
		t.Fatalf("write workspace.json: %v", err)
	}

	factory := runtime.NewFactory(nil, map[string]runtime.Driver{
		"local":       &stubDriver{backend: "local"},
		"firecracker": &stubDriver{backend: "firecracker"},
	})
	spec := workspacemgr.CreateSpec{
		Repo:          repo,
		WorkspaceName: "nexus",
	}
	prepared, rpcErr, _ := PrepareCreate(context.Background(), spec, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %v", rpcErr)
	}
	if prepared.Backend != "local" {
		t.Fatalf("expected local backend, got %q", prepared.Backend)
	}
}
