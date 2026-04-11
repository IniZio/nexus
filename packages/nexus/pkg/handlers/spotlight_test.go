package handlers

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/compose"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
)

func TestHandleSpotlightApplyDefaults(t *testing.T) {
	root := t.TempDir()
	mgr := spotlight.NewManager()
	params, _ := json.Marshal(SpotlightApplyDefaultsParams{WorkspaceID: "ws-1"})

	res, rpcErr := HandleSpotlightApplyDefaults(context.Background(), params, root, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if len(res.Forwards) != 0 {
		t.Fatalf("expected 0 forwards, got %d", len(res.Forwards))
	}
}

func TestHandleSpotlightApplyComposePorts_ForwardsDiscoveredPorts(t *testing.T) {
	mgr := spotlight.NewManager()
	rootDir := t.TempDir()
	params, _ := json.Marshal(SpotlightApplyComposePortsParams{WorkspaceID: "ws-1"})
	studentPort := freeTCPPort(t)
	apiPort := freeTCPPort(t)

	orig := discoverPublishedPorts
	t.Cleanup(func() { discoverPublishedPorts = orig })
	discoverPublishedPorts = func(_ context.Context, _ string) ([]compose.PublishedPort, error) {
		return []compose.PublishedPort{
			{Service: "student", HostPort: studentPort, TargetPort: studentPort, Protocol: "tcp"},
			{Service: "api", HostPort: apiPort, TargetPort: apiPort, Protocol: "tcp"},
		}, nil
	}

	res, rpcErr := HandleSpotlightApplyComposePorts(context.Background(), params, rootDir, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if len(res.Forwards) != 2 {
		t.Fatalf("expected 2 forwards, got %d", len(res.Forwards))
	}
	if len(res.Errors) != 0 {
		t.Fatalf("expected 0 errors, got %d", len(res.Errors))
	}
}

func TestHandleSpotlightApplyComposePorts_ReportsCollisionsPerPort(t *testing.T) {
	mgr := spotlight.NewManager()
	busyPort := freeTCPPort(t)
	apiPort := freeTCPPort(t)

	_, err := mgr.Expose(context.Background(), spotlight.ExposeSpec{
		WorkspaceID: "ws-other",
		Service:     "busy",
		RemotePort:  8000,
		LocalPort:   busyPort,
	})
	if err != nil {
		t.Fatalf("seed forward: %v", err)
	}

	rootDir := t.TempDir()
	params, _ := json.Marshal(SpotlightApplyComposePortsParams{WorkspaceID: "ws-1"})
	orig := discoverPublishedPorts
	t.Cleanup(func() { discoverPublishedPorts = orig })
	discoverPublishedPorts = func(_ context.Context, _ string) ([]compose.PublishedPort, error) {
		return []compose.PublishedPort{
			{Service: "student", HostPort: busyPort, TargetPort: busyPort, Protocol: "tcp"},
			{Service: "api", HostPort: apiPort, TargetPort: apiPort, Protocol: "tcp"},
		}, nil
	}

	res, rpcErr := HandleSpotlightApplyComposePorts(context.Background(), params, rootDir, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if len(res.Forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(res.Forwards))
	}
	if len(res.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(res.Errors))
	}
	if res.Errors[0].HostPort != busyPort {
		t.Fatalf("expected collision on %d, got %+v", busyPort, res.Errors[0])
	}
}

func TestHandleSpotlightApplyComposePorts_NoComposeFileReturnsEmpty(t *testing.T) {
	mgr := spotlight.NewManager()
	rootDir := t.TempDir()
	params, _ := json.Marshal(SpotlightApplyComposePortsParams{WorkspaceID: "ws-1"})

	orig := discoverPublishedPorts
	t.Cleanup(func() { discoverPublishedPorts = orig })
	discoverPublishedPorts = func(_ context.Context, _ string) ([]compose.PublishedPort, error) {
		return nil, compose.ErrComposeFileNotFound
	}

	res, rpcErr := HandleSpotlightApplyComposePorts(context.Background(), params, rootDir, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if len(res.Forwards) != 0 || len(res.Errors) != 0 {
		t.Fatalf("expected empty result, got forwards=%d errors=%d", len(res.Forwards), len(res.Errors))
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free tcp port: %v", err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("expected tcp address")
	}
	return addr.Port
}
