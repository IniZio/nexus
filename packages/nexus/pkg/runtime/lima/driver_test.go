package lima

import (
	"context"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

type stubDriver struct {
	lastReq runtime.CreateRequest
}

func (d *stubDriver) Backend() string { return "lima" }
func (d *stubDriver) Create(_ context.Context, req runtime.CreateRequest) error {
	d.lastReq = req
	return nil
}
func (d *stubDriver) Start(_ context.Context, _ string) error   { return nil }
func (d *stubDriver) Stop(_ context.Context, _ string) error    { return nil }
func (d *stubDriver) Restore(_ context.Context, _ string) error { return nil }
func (d *stubDriver) Pause(_ context.Context, _ string) error   { return nil }
func (d *stubDriver) Resume(_ context.Context, _ string) error  { return nil }
func (d *stubDriver) Fork(_ context.Context, _, _ string) error { return nil }
func (d *stubDriver) Destroy(_ context.Context, _ string) error { return nil }

type checkpointStubDriver struct {
	stubDriver
	lastWorkspaceID string
	lastChildID     string
	err             error
	snapshotID      string
}

func (d *checkpointStubDriver) CheckpointFork(_ context.Context, workspaceID, childWorkspaceID string) (string, error) {
	d.lastWorkspaceID = workspaceID
	d.lastChildID = childWorkspaceID
	if d.err != nil {
		return "", d.err
	}
	if d.snapshotID != "" {
		return d.snapshotID, nil
	}
	return "snap-from-inner", nil
}

func TestDriver_Backend(t *testing.T) {
	d := NewDriver(&stubDriver{})
	if got := d.Backend(); got != "lima" {
		t.Fatalf("expected lima backend, got %q", got)
	}
}

func TestLimaDriverBackendName(t *testing.T) {
	d := &Driver{}
	if got := d.Backend(); got != "lima" {
		t.Fatalf("expected backend 'lima', got %q", got)
	}
}

func TestDriver_CreateUsesDedicatedInstanceByDefault(t *testing.T) {
	inner := &stubDriver{}
	d := NewDriver(inner)

	req := runtime.CreateRequest{
		WorkspaceID:   "ws-1",
		WorkspaceName: "alpha",
		ProjectRoot:   "/tmp/repo",
		Options:       map[string]string{},
	}
	if err := d.Create(context.Background(), req); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if got := inner.lastReq.Options["lima.instance"]; got != "nexus-ws-1" {
		t.Fatalf("expected default lima.instance %q, got %q", "nexus-ws-1", got)
	}
}

func TestDriver_CreateUsesDedicatedInstanceWhenRequested(t *testing.T) {
	inner := &stubDriver{}
	d := NewDriver(inner)

	req := runtime.CreateRequest{
		WorkspaceID:   "ws-prod_42",
		WorkspaceName: "alpha",
		ProjectRoot:   "/tmp/repo",
		Options:       map[string]string{"vm.mode": "dedicated"},
	}
	if err := d.Create(context.Background(), req); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if got := inner.lastReq.Options["lima.instance"]; got != "nexus-ws-prod-42" {
		t.Fatalf("expected dedicated lima.instance %q, got %q", "nexus-ws-prod-42", got)
	}
}

func TestDriver_CheckpointForkDelegatesToInner(t *testing.T) {
	inner := &checkpointStubDriver{snapshotID: "snap-from-inner"}
	d := NewDriver(inner)

	snapshotID, err := d.CheckpointFork(context.Background(), "ws-parent", "ws-child")
	if err != nil {
		t.Fatalf("unexpected checkpoint error: %v", err)
	}
	if snapshotID != "snap-from-inner" {
		t.Fatalf("expected snapshot id %q, got %q", "snap-from-inner", snapshotID)
	}
	if inner.lastWorkspaceID != "ws-parent" || inner.lastChildID != "ws-child" {
		t.Fatalf("expected delegated ids ws-parent/ws-child, got %q/%q", inner.lastWorkspaceID, inner.lastChildID)
	}
}

func TestDriver_CheckpointFork_UsesExplicitCheckpointDelegate(t *testing.T) {
	inner := &stubDriver{}
	checkpoint := &checkpointStubDriver{snapshotID: "snap-from-checkpoint-driver"}
	d := NewDriverWithCheckpoint(inner, checkpoint)

	snapshotID, err := d.CheckpointFork(context.Background(), "ws-parent", "ws-child")
	if err != nil {
		t.Fatalf("unexpected checkpoint error: %v", err)
	}
	if snapshotID != "snap-from-checkpoint-driver" {
		t.Fatalf("expected delegated snapshot id %q, got %q", "snap-from-checkpoint-driver", snapshotID)
	}
	if checkpoint.lastWorkspaceID != "ws-parent" || checkpoint.lastChildID != "ws-child" {
		t.Fatalf("expected checkpoint delegate ids ws-parent/ws-child, got %q/%q", checkpoint.lastWorkspaceID, checkpoint.lastChildID)
	}
}
