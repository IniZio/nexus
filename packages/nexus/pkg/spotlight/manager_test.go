package spotlight

import (
	"context"
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
