package local

import (
	"context"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

func TestDriver_Backend(t *testing.T) {
	d := NewDriver()
	if d.Backend() != "local" {
		t.Fatalf("expected backend 'local', got %q", d.Backend())
	}
}

func TestDriver_Create(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	err := d.Create(ctx, runtime.CreateRequest{
		WorkspaceID:   "ws-1",
		WorkspaceName: "test-workspace",
		ProjectRoot:   "/projects/test",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Verify workspace exists
	state, ok := d.GetState("ws-1")
	if !ok {
		t.Fatal("workspace should exist after create")
	}
	if state != "created" {
		t.Fatalf("expected state 'created', got %q", state)
	}

	// Verify project ID
	projectID, ok := d.GetProjectID("ws-1")
	if !ok {
		t.Fatal("workspace should have project ID")
	}
	if projectID != "/projects/test" {
		t.Fatalf("expected project ID '/projects/test', got %q", projectID)
	}
}

func TestDriver_Create_Duplicate(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	err := d.Create(ctx, runtime.CreateRequest{
		WorkspaceID:   "ws-1",
		WorkspaceName: "test-workspace",
		ProjectRoot:   "/projects/test",
	})
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	// Second create with same ID should fail
	err = d.Create(ctx, runtime.CreateRequest{
		WorkspaceID:   "ws-1",
		WorkspaceName: "another-workspace",
		ProjectRoot:   "/projects/other",
	})
	if err == nil {
		t.Fatal("expected error for duplicate workspace")
	}
}

func TestDriver_StartStop(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	// Create workspace
	err := d.Create(ctx, runtime.CreateRequest{WorkspaceID: "ws-1", ProjectRoot: "/projects/test"})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Start workspace
	err = d.Start(ctx, "ws-1")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	state, _ := d.GetState("ws-1")
	if state != "running" {
		t.Fatalf("expected state 'running', got %q", state)
	}

	// Stop workspace
	err = d.Stop(ctx, "ws-1")
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	state, _ = d.GetState("ws-1")
	if state != "stopped" {
		t.Fatalf("expected state 'stopped', got %q", state)
	}
}

func TestDriver_Start_UnknownWorkspace(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	err := d.Start(ctx, "unknown-ws")
	if err == nil {
		t.Fatal("expected error for unknown workspace")
	}
}

func TestDriver_Restore(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	// Create and stop workspace
	d.Create(ctx, runtime.CreateRequest{WorkspaceID: "ws-1", ProjectRoot: "/projects/test"})
	d.Stop(ctx, "ws-1")

	// Restore workspace
	err := d.Restore(ctx, "ws-1")
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	state, _ := d.GetState("ws-1")
	if state != "running" {
		t.Fatalf("expected state 'running' after restore, got %q", state)
	}
}

func TestDriver_Restore_UnknownWorkspace(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	err := d.Restore(ctx, "unknown-ws")
	if err == nil {
		t.Fatal("expected error for unknown workspace")
	}
}

func TestDriver_PauseResume_NoOp(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	// Create workspace
	d.Create(ctx, runtime.CreateRequest{WorkspaceID: "ws-1", ProjectRoot: "/projects/test"})

	// Pause should succeed (no-op)
	err := d.Pause(ctx, "ws-1")
	if err != nil {
		t.Fatalf("pause failed: %v", err)
	}

	// Resume should succeed (no-op)
	err = d.Resume(ctx, "ws-1")
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
}

func TestDriver_Pause_UnknownWorkspace(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	err := d.Pause(ctx, "unknown-ws")
	if err == nil {
		t.Fatal("expected error for unknown workspace")
	}
}

func TestDriver_Resume_UnknownWorkspace(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	err := d.Resume(ctx, "unknown-ws")
	if err == nil {
		t.Fatal("expected error for unknown workspace")
	}
}

func TestDriver_Fork(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	// Create parent workspace
	err := d.Create(ctx, runtime.CreateRequest{
		WorkspaceID:   "parent-ws",
		WorkspaceName: "parent",
		ProjectRoot:   "/projects/parent",
	})
	if err != nil {
		t.Fatalf("create parent failed: %v", err)
	}

	// Fork to create child
	err = d.Fork(ctx, "parent-ws", "child-ws")
	if err != nil {
		t.Fatalf("fork failed: %v", err)
	}

	// Verify child exists with same project ID
	projectID, ok := d.GetProjectID("child-ws")
	if !ok {
		t.Fatal("child workspace should exist")
	}
	if projectID != "/projects/parent" {
		t.Fatalf("expected child to inherit parent project ID, got %q", projectID)
	}

	state, _ := d.GetState("child-ws")
	if state != "created" {
		t.Fatalf("expected child state 'created', got %q", state)
	}
}

func TestDriver_Fork_UnknownParent(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	err := d.Fork(ctx, "unknown-parent", "child-ws")
	if err == nil {
		t.Fatal("expected error for unknown parent workspace")
	}
}

func TestDriver_Fork_DuplicateChild(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	// Create parent and child
	d.Create(ctx, runtime.CreateRequest{WorkspaceID: "parent-ws", ProjectRoot: "/projects/parent"})
	d.Create(ctx, runtime.CreateRequest{WorkspaceID: "child-ws", ProjectRoot: "/projects/other"})

	// Fork with existing child should fail
	err := d.Fork(ctx, "parent-ws", "child-ws")
	if err == nil {
		t.Fatal("expected error for duplicate child workspace")
	}
}

func TestDriver_Destroy(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	// Create workspace
	d.Create(ctx, runtime.CreateRequest{WorkspaceID: "ws-1", ProjectRoot: "/projects/test"})

	// Destroy workspace
	err := d.Destroy(ctx, "ws-1")
	if err != nil {
		t.Fatalf("destroy failed: %v", err)
	}

	// Verify workspace removed
	_, ok := d.GetState("ws-1")
	if ok {
		t.Fatal("workspace should not exist after destroy")
	}
}

func TestDriver_Destroy_UnknownWorkspace(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	err := d.Destroy(ctx, "unknown-ws")
	if err == nil {
		t.Fatal("expected error for unknown workspace")
	}
}

func TestDriver_ImplementsDriver(t *testing.T) {
	var _ runtime.Driver = (*Driver)(nil)
}

func TestDriver_DogfoodingParallelWorkspaceOperations(t *testing.T) {
	d := NewDriver()
	ctx := context.Background()

	err := d.Create(ctx, runtime.CreateRequest{WorkspaceID: "ws-a", ProjectRoot: "/projects/a"})
	if err != nil {
		t.Fatalf("create ws-a failed: %v", err)
	}
	err = d.Create(ctx, runtime.CreateRequest{WorkspaceID: "ws-b", ProjectRoot: "/projects/b"})
	if err != nil {
		t.Fatalf("create ws-b failed: %v", err)
	}

	err = d.Start(ctx, "ws-a")
	if err != nil {
		t.Fatalf("start ws-a failed: %v", err)
	}
	err = d.Start(ctx, "ws-b")
	if err != nil {
		t.Fatalf("start ws-b failed: %v", err)
	}

	err = d.Pause(ctx, "ws-a")
	if err != nil {
		t.Fatalf("pause ws-a failed: %v", err)
	}
	err = d.Resume(ctx, "ws-a")
	if err != nil {
		t.Fatalf("resume ws-a failed: %v", err)
	}

	err = d.Fork(ctx, "ws-a", "ws-a-child")
	if err != nil {
		t.Fatalf("fork ws-a failed: %v", err)
	}

	childProjectID, ok := d.GetProjectID("ws-a-child")
	if !ok {
		t.Fatal("expected child workspace to exist")
	}
	if childProjectID != "/projects/a" {
		t.Fatalf("expected child project id '/projects/a', got %q", childProjectID)
	}

	stateA, _ := d.GetState("ws-a")
	stateB, _ := d.GetState("ws-b")
	if stateA != "running" {
		t.Fatalf("expected ws-a state 'running', got %q", stateA)
	}
	if stateB != "running" {
		t.Fatalf("expected ws-b state 'running', got %q", stateB)
	}
}
