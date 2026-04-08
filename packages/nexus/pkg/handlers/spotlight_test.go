package handlers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/compose"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
)

func TestHandleSpotlightApplyDefaults(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}

	configJSON := `{
  "version": 1,
  "runtime": {
    "required": ["linux"]
  },
  "spotlight": {
    "defaults": [
      {"service":"student-portal","remotePort":5173,"localPort":5173},
      {"service":"api","remotePort":8000,"localPort":8000}
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(root, ".nexus", "workspace.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := spotlight.NewManager()
	params, _ := json.Marshal(SpotlightApplyDefaultsParams{WorkspaceID: "ws-1", RootPath: root})

	res, rpcErr := HandleSpotlightApplyDefaults(context.Background(), params, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if len(res.Forwards) != 2 {
		t.Fatalf("expected 2 forwards, got %d", len(res.Forwards))
	}
}

func TestHandleSpotlightApplyComposePorts_ForwardsDiscoveredPorts(t *testing.T) {
	mgr := spotlight.NewManager()
	params, _ := json.Marshal(SpotlightApplyComposePortsParams{WorkspaceID: "ws-1", RootPath: t.TempDir()})

	orig := discoverPublishedPorts
	t.Cleanup(func() { discoverPublishedPorts = orig })
	discoverPublishedPorts = func(_ context.Context, _ string) ([]compose.PublishedPort, error) {
		return []compose.PublishedPort{
			{Service: "student", HostPort: 5173, TargetPort: 5173, Protocol: "tcp"},
			{Service: "api", HostPort: 8000, TargetPort: 8000, Protocol: "tcp"},
		}, nil
	}

	res, rpcErr := HandleSpotlightApplyComposePorts(context.Background(), params, mgr)
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

	_, err := mgr.Expose(context.Background(), spotlight.ExposeSpec{
		WorkspaceID: "ws-other",
		Service:     "busy",
		RemotePort:  8000,
		LocalPort:   5173,
	})
	if err != nil {
		t.Fatalf("seed forward: %v", err)
	}

	params, _ := json.Marshal(SpotlightApplyComposePortsParams{WorkspaceID: "ws-1", RootPath: t.TempDir()})
	orig := discoverPublishedPorts
	t.Cleanup(func() { discoverPublishedPorts = orig })
	discoverPublishedPorts = func(_ context.Context, _ string) ([]compose.PublishedPort, error) {
		return []compose.PublishedPort{
			{Service: "student", HostPort: 5173, TargetPort: 5173, Protocol: "tcp"},
			{Service: "api", HostPort: 8000, TargetPort: 8000, Protocol: "tcp"},
		}, nil
	}

	res, rpcErr := HandleSpotlightApplyComposePorts(context.Background(), params, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if len(res.Forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(res.Forwards))
	}
	if len(res.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(res.Errors))
	}
	if res.Errors[0].HostPort != 5173 {
		t.Fatalf("expected collision on 5173, got %+v", res.Errors[0])
	}
}

func TestHandleSpotlightApplyComposePorts_NoComposeFileReturnsEmpty(t *testing.T) {
	mgr := spotlight.NewManager()
	params, _ := json.Marshal(SpotlightApplyComposePortsParams{WorkspaceID: "ws-1", RootPath: t.TempDir()})

	orig := discoverPublishedPorts
	t.Cleanup(func() { discoverPublishedPorts = orig })
	discoverPublishedPorts = func(_ context.Context, _ string) ([]compose.PublishedPort, error) {
		return nil, compose.ErrComposeFileNotFound
	}

	res, rpcErr := HandleSpotlightApplyComposePorts(context.Background(), params, mgr)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if len(res.Forwards) != 0 || len(res.Errors) != 0 {
		t.Fatalf("expected empty result, got forwards=%d errors=%d", len(res.Forwards), len(res.Errors))
	}
}
