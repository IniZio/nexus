package spotlight

import (
	"context"
	"path/filepath"
	"testing"
)

func TestExpose_FailsOnLocalPortCollision(t *testing.T) {
	mgr := NewManager()
	_, err := mgr.Expose(context.Background(), ExposeSpec{WorkspaceID: "ws-1", LocalPort: 5173, RemotePort: 5173})
	if err != nil {
		t.Fatalf("expected first expose to succeed, got %v", err)
	}

	_, err = mgr.Expose(context.Background(), ExposeSpec{WorkspaceID: "ws-2", LocalPort: 5173, RemotePort: 8000})
	if err == nil {
		t.Fatal("expected second expose to fail due to port collision")
	}
}

func TestListAndClose(t *testing.T) {
	mgr := NewManager()
	fwd, err := mgr.Expose(context.Background(), ExposeSpec{WorkspaceID: "ws-1", LocalPort: 5173, RemotePort: 5173})
	if err != nil {
		t.Fatalf("unexpected expose error: %v", err)
	}

	list := mgr.List("ws-1")
	if len(list) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(list))
	}

	if !mgr.Close(fwd.ID) {
		t.Fatal("expected close to succeed")
	}

	list = mgr.List("ws-1")
	if len(list) != 0 {
		t.Fatalf("expected 0 forwards, got %d", len(list))
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "spotlight-state.json")

	mgr := NewManager()
	_, err := mgr.Expose(context.Background(), ExposeSpec{
		WorkspaceID: "ws-1",
		Service:     "api",
		RemotePort:  8000,
		LocalPort:   18000,
	})
	if err != nil {
		t.Fatalf("expose ws-1: %v", err)
	}
	_, err = mgr.Expose(context.Background(), ExposeSpec{
		WorkspaceID: "ws-2",
		Service:     "web",
		RemotePort:  3000,
		LocalPort:   13000,
	})
	if err != nil {
		t.Fatalf("expose ws-2: %v", err)
	}

	if err := mgr.Save(statePath); err != nil {
		t.Fatalf("save state: %v", err)
	}

	reloaded := NewManager()
	if err := reloaded.Load(statePath); err != nil {
		t.Fatalf("load state: %v", err)
	}

	all := reloaded.List("")
	if len(all) != 2 {
		t.Fatalf("expected 2 forwards after load, got %d", len(all))
	}
	ws1 := reloaded.List("ws-1")
	if len(ws1) != 1 {
		t.Fatalf("expected 1 ws-1 forward after load, got %d", len(ws1))
	}
}
