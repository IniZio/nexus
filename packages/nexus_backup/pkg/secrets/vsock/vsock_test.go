package vsock

import (
	"context"
	"testing"
	"time"
)

func TestServerStartAndStop(t *testing.T) {
	srv := NewServer(0)
	if srv.IsRunning() {
		t.Fatal("expected server not running before Start")
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !srv.IsRunning() {
		t.Fatal("expected IsRunning after Start")
	}
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if srv.IsRunning() {
		t.Fatal("expected not running after Stop")
	}
}

func TestClientServerIntegration(t *testing.T) {
	srv := NewServer(0)
	expires := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	srv.HandleRequest = func(ctx context.Context, req Request) Response {
		if req.WorkspaceID != "ws-1" || req.Provider != "opencode" {
			return Response{Error: "unexpected request"}
		}
		return Response{
			Token:     "tok-integration",
			ExpiresAt: expires,
		}
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client := NewClient(srv.Port())
	resp, err := client.RequestToken("ws-1", "opencode")
	if err != nil {
		t.Fatalf("RequestToken: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Token != "tok-integration" {
		t.Fatalf("token: got %q", resp.Token)
	}
	if !resp.ExpiresAt.Equal(expires) {
		t.Fatalf("expires: got %v want %v", resp.ExpiresAt, expires)
	}
}
