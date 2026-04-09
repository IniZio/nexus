package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type serverTestDriver struct {
	backend string
}

func (d *serverTestDriver) Backend() string                                             { return d.backend }
func (d *serverTestDriver) Create(ctx context.Context, req runtime.CreateRequest) error { return nil }
func (d *serverTestDriver) Start(ctx context.Context, workspaceID string) error         { return nil }
func (d *serverTestDriver) Stop(ctx context.Context, workspaceID string) error          { return nil }
func (d *serverTestDriver) Restore(ctx context.Context, workspaceID string) error       { return nil }
func (d *serverTestDriver) Pause(ctx context.Context, workspaceID string) error         { return nil }
func (d *serverTestDriver) Resume(ctx context.Context, workspaceID string) error        { return nil }
func (d *serverTestDriver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	return nil
}
func (d *serverTestDriver) Destroy(ctx context.Context, workspaceID string) error { return nil }

type serverTestConnectorDriver struct {
	serverTestDriver
	mu           sync.Mutex
	workspace    string
	openCalled   bool
	openCommand  string
	openWorkdir  string
	writeCalled  bool
	resizeCalled bool
	writeSignal  chan struct{}
	resizeSignal chan struct{}
}

type serverTestNoConnectorDriver struct {
	serverTestDriver
}

func (d *serverTestConnectorDriver) AgentConn(ctx context.Context, workspaceID string) (net.Conn, error) {
	d.mu.Lock()
	d.workspace = workspaceID
	d.openCalled = true
	d.mu.Unlock()

	left, right := net.Pipe()
	go func() {
		defer right.Close()
		enc := json.NewEncoder(right)
		dec := json.NewDecoder(right)
		for {
			var req map[string]any
			if err := dec.Decode(&req); err != nil {
				return
			}
			typ, _ := req["type"].(string)
			sessionID, _ := req["id"].(string)
			switch typ {
			case "shell.open", "shell.write", "shell.resize", "shell.close":
				if typ == "shell.open" {
					command, _ := req["command"].(string)
					workdir, _ := req["workdir"].(string)
					d.mu.Lock()
					d.openCommand = command
					d.openWorkdir = workdir
					d.mu.Unlock()
				}
				if typ == "shell.write" {
					d.mu.Lock()
					d.writeCalled = true
					if d.writeSignal != nil {
						select {
						case d.writeSignal <- struct{}{}:
						default:
						}
					}
					d.mu.Unlock()
				}
				if typ == "shell.resize" {
					d.mu.Lock()
					d.resizeCalled = true
					if d.resizeSignal != nil {
						select {
						case d.resizeSignal <- struct{}{}:
						default:
						}
					}
					d.mu.Unlock()
				}
				if typ == "shell.close" {
					_ = enc.Encode(map[string]any{"id": sessionID, "type": "result", "exit_code": 0})
					return
				}
				_ = enc.Encode(map[string]any{"id": sessionID, "type": "result", "exit_code": 0})
			default:
				_ = enc.Encode(map[string]any{"id": sessionID, "type": "result", "exit_code": 1, "stderr": "unknown request"})
			}
		}
	}()

	return left, nil
}

func (d *serverTestConnectorDriver) actionDetails() (bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writeCalled, d.resizeCalled
}

func TestPTYOpenFirecrackerAliasFallsBackToDriverReportedBackend(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	connector := &serverTestConnectorDriver{
		serverTestDriver: serverTestDriver{backend: "firecracker"},
		writeSignal:      make(chan struct{}, 1),
		resizeSignal:     make(chan struct{}, 1),
	}

	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{"firecracker": connector},
	)
	srv.SetRuntimeFactory(factory)

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "firecracker")
	if err := srv.workspaceMgr.Start(ws.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*ptySession{}}

	payload, _ := json.Marshal(map[string]any{
		"workspaceId": ws.ID,
		"cols":        80,
		"rows":        24,
	})

	result, rpcErr := srv.handlePTYOpen(payload, conn, srv.ws)
	if rpcErr != nil {
		t.Fatalf("pty.open rpc error: %+v", rpcErr)
	}
	if result == nil {
		t.Fatal("expected pty.open result")
	}

	open, ok := result.(*ptyOpenResult)
	if !ok || strings.TrimSpace(open.SessionID) == "" {
		t.Fatalf("unexpected pty.open result type/value: %#v", result)
	}

	called, openCommand, openWorkdir := connector.openDetails(ws.ID)
	if !called {
		t.Fatalf("expected AgentConn to be called for alias-backed firecracker workspace=%s", ws.ID)
	}
	if openCommand != "bash" {
		t.Fatalf("expected remote shell command bash, got %q", openCommand)
	}
	if openWorkdir != "/workspace" {
		t.Fatalf("expected remote shell workdir /workspace, got %q", openWorkdir)
	}
}

func (d *serverTestConnectorDriver) openDetails(workspaceID string) (bool, string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !(d.openCalled && d.workspace == workspaceID) {
		return false, "", ""
	}
	return true, d.openCommand, d.openWorkdir
}

func createWorkspaceForPTYTest(t *testing.T, mgr *workspacemgr.Manager, backend string) *workspacemgr.Workspace {
	t.Helper()

	wsName := "pty-" + backend

	ws, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "https://example.com/repo.git",
		Ref:           "main",
		WorkspaceName: wsName,
		AgentProfile:  "codex",
		Policy:        workspacemgr.Policy{},
		Backend:       backend,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return ws
}

func TestPTYOpenUsesRemoteConnectorForFirecracker(t *testing.T) {
	testPTYOpenUsesRemoteConnectorForBackend(t, "firecracker")
}

func TestPTYOpenUsesRemoteConnectorForSeatbelt(t *testing.T) {
	testPTYOpenUsesRemoteConnectorForBackend(t, "seatbelt")
}

func testPTYOpenUsesRemoteConnectorForBackend(t *testing.T, backend string) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	driver := &serverTestConnectorDriver{
		serverTestDriver: serverTestDriver{backend: backend},
		writeSignal:      make(chan struct{}, 1),
		resizeSignal:     make(chan struct{}, 1),
	}
	capabilities := []runtime.Capability{{Name: "runtime.firecracker", Available: true}}
	drivers := map[string]runtime.Driver{"firecracker": driver}
	if backend == "seatbelt" {
		capabilities = append(capabilities, runtime.Capability{Name: "runtime.seatbelt", Available: true})
		drivers["seatbelt"] = driver
	}
	factory := runtime.NewFactory(
		capabilities,
		drivers,
	)
	srv.SetRuntimeFactory(factory)

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, backend)
	if err := srv.workspaceMgr.Start(ws.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*ptySession{}}

	payload, _ := json.Marshal(map[string]any{
		"workspaceId": ws.ID,
		"cols":        80,
		"rows":        24,
	})

	result, rpcErr := srv.handlePTYOpen(payload, conn, srv.ws)
	if rpcErr != nil {
		t.Fatalf("pty.open rpc error: %+v", rpcErr)
	}
	if result == nil {
		t.Fatal("expected pty.open result")
	}

	open, ok := result.(*ptyOpenResult)
	if !ok || strings.TrimSpace(open.SessionID) == "" {
		t.Fatalf("unexpected pty.open result type/value: %#v", result)
	}

	called, openCommand, openWorkdir := driver.openDetails(ws.ID)
	if !called {
		t.Fatalf("expected AgentConn to be called for backend=%s workspace=%s", backend, ws.ID)
	}
	if openCommand != "bash" {
		t.Fatalf("expected remote shell command bash, got %q", openCommand)
	}
	if openWorkdir != "/workspace" {
		t.Fatalf("expected remote shell workdir /workspace, got %q", openWorkdir)
	}

	writeParams, _ := json.Marshal(map[string]any{"sessionId": open.SessionID, "data": "echo ok\n"})
	if _, rpcErr := srv.handlePTYWrite(writeParams, conn); rpcErr != nil {
		t.Fatalf("pty.write rpc error: %+v", rpcErr)
	}

	if backend == "firecracker" {
		resizeParams, _ := json.Marshal(map[string]any{"sessionId": open.SessionID, "cols": 100, "rows": 30})
		if _, rpcErr := srv.handlePTYResize(resizeParams, conn); rpcErr != nil {
			t.Fatalf("pty.resize rpc error: %+v", rpcErr)
		}
	}

	select {
	case <-driver.writeSignal:
	case <-time.After(2 * time.Second):
		wrote, resized := driver.actionDetails()
		t.Fatalf("expected remote shell write call, got write=%v resize=%v", wrote, resized)
	}

	if backend == "firecracker" {
		select {
		case <-driver.resizeSignal:
		case <-time.After(2 * time.Second):
			wrote, resized := driver.actionDetails()
			t.Fatalf("expected remote shell resize call, got write=%v resize=%v", wrote, resized)
		}
	}

	closeParams, _ := json.Marshal(map[string]any{"sessionId": open.SessionID})
	closeResult, rpcErr := srv.handlePTYClose(closeParams, conn)
	if rpcErr != nil {
		t.Fatalf("pty.close rpc error: %+v", rpcErr)
	}
	if closeResultMap, ok := closeResult.(map[string]bool); !ok || !closeResultMap["closed"] {
		t.Fatalf("expected close result {closed:true}, got %#v", closeResult)
	}

	closeResult, rpcErr = srv.handlePTYClose(closeParams, conn)
	if rpcErr != nil {
		t.Fatalf("pty.close idempotent rpc error: %+v", rpcErr)
	}
	if closeResultMap, ok := closeResult.(map[string]bool); !ok || !closeResultMap["closed"] {
		t.Fatalf("expected idempotent close result {closed:true}, got %#v", closeResult)
	}
}

func TestPTYOpenRejectsWorkspaceNotStarted(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "")
	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*ptySession{}}

	payload, _ := json.Marshal(map[string]any{"workspaceId": ws.ID})
	_, rpcErr := srv.handlePTYOpen(payload, conn, srv.ws)
	if rpcErr == nil {
		t.Fatal("expected workspace-not-started rpc error")
	}
	if rpcErr.Code != rpckit.ErrWorkspaceNotStarted.Code {
		t.Fatalf("expected ErrWorkspaceNotStarted code %d, got %+v", rpckit.ErrWorkspaceNotStarted.Code, rpcErr)
	}
	if rpcErr.Message != rpckit.ErrWorkspaceNotStarted.Message {
		t.Fatalf("expected Workspace not started, got %+v", rpcErr)
	}
}

func TestWorkspaceReadyRejectsWorkspaceNotStarted(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "")
	msg := &RPCMessage{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "workspace.ready",
		Params: mustRawJSON(map[string]any{
			"workspaceId": ws.ID,
			"checks": []map[string]any{{
				"name":    "compose",
				"command": "docker-compose",
				"args":    []string{"ps"},
			}},
			"timeoutMs":  500,
			"intervalMs": 100,
		}),
	}

	resp := srv.processRPC(msg, &Connection{send: make(chan []byte, 1), clientID: "test", pty: map[string]*ptySession{}})
	if resp.Error == nil {
		t.Fatal("expected workspace-not-started error")
	}
	if resp.Error.Code != rpckit.ErrWorkspaceNotStarted.Code {
		t.Fatalf("expected ErrWorkspaceNotStarted code %d, got %+v", rpckit.ErrWorkspaceNotStarted.Code, resp.Error)
	}
	if resp.Error.Message != rpckit.ErrWorkspaceNotStarted.Message {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
}

func TestWorkspaceReadyAllowsStartedWorkspace(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "")
	if err := srv.workspaceMgr.Start(ws.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}

	msg := &RPCMessage{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "workspace.ready",
		Params: mustRawJSON(map[string]any{
			"workspaceId": ws.ID,
			"checks": []map[string]any{{
				"name":    "compose",
				"command": "sh",
				"args":    []string{"-lc", "exit 0"},
			}},
			"timeoutMs":  500,
			"intervalMs": 100,
		}),
	}

	resp := srv.processRPC(msg, &Connection{send: make(chan []byte, 1), clientID: "test", pty: map[string]*ptySession{}})
	if resp.Error != nil {
		t.Fatalf("expected workspace.ready success for started workspace, got %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("expected workspace.ready result")
	}
}

func TestPTYOpenAllowsStartedWorkspace(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "")
	if err := srv.workspaceMgr.Start(ws.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}

	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*ptySession{}}
	payload, _ := json.Marshal(map[string]any{"workspaceId": ws.ID, "shell": "sh", "rows": 12, "cols": 40})
	result, rpcErr := srv.handlePTYOpen(payload, conn, srv.ws)
	if rpcErr != nil {
		t.Fatalf("expected started workspace to open PTY, got %+v", rpcErr)
	}
	if result == nil {
		t.Fatal("expected pty.open result")
	}

	open := result.(*ptyOpenResult)
	closeParams, _ := json.Marshal(map[string]any{"sessionId": open.SessionID})
	if _, rpcErr := srv.handlePTYClose(closeParams, conn); rpcErr != nil {
		t.Fatalf("pty.close rpc error: %+v", rpcErr)
	}
}

func TestPTYOpenRejectsMissingWorkspaceID(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*ptySession{}}
	payload, _ := json.Marshal(map[string]any{"shell": "sh", "rows": 12, "cols": 40})
	_, rpcErr := srv.handlePTYOpen(payload, conn, srv.ws)
	if rpcErr == nil {
		t.Fatal("expected invalid params error")
	}
	if rpcErr.Code != rpckit.ErrInvalidParams.Code {
		t.Fatalf("expected invalid params code %d, got %+v", rpckit.ErrInvalidParams.Code, rpcErr)
	}
}

func TestWorkspaceReadyRejectsMissingWorkspaceID(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	msg := &RPCMessage{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "workspace.ready",
		Params: mustRawJSON(map[string]any{
			"checks": []map[string]any{{
				"name":    "compose",
				"command": "docker-compose",
				"args":    []string{"ps"},
			}},
			"timeoutMs":  500,
			"intervalMs": 100,
		}),
	}

	resp := srv.processRPC(msg, &Connection{send: make(chan []byte, 1), clientID: "test", pty: map[string]*ptySession{}})
	if resp.Error == nil {
		t.Fatal("expected invalid params error")
	}
	if resp.Error.Code != rpckit.ErrInvalidParams.Code {
		t.Fatalf("expected invalid params code %d, got %+v", rpckit.ErrInvalidParams.Code, resp.Error)
	}
}

func mustRawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestUIAPIEndpointsRemoved(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	paths := []string{
		"/ui/api/summary",
		"/ui/api/workspaces",
		"/ui/api/relations",
		"/ui/api/spotlight",
	}

	for _, p := range paths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rr := httptest.NewRecorder()
		srv.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s, got %d", p, rr.Code)
		}
	}
}

func TestUIServesEmbeddedHTML(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	rr := httptest.NewRecorder()
	srv.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Nexus Admin") && !strings.Contains(body, "Nexus Workspace Control") {
		t.Fatalf("expected embedded portal index content, got: %s", body)
	}
}

func TestPortalSummaryEndpointRemoved(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/portal/api", nil)
	rr := httptest.NewRecorder()
	srv.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 from portal UI fallback, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Nexus Admin") && !strings.Contains(body, "Nexus Workspace Control") {
		t.Fatalf("expected portal UI content fallback, got: %s", body)
	}
}

func TestServer_IgnoresLegacySpotlightJSON(t *testing.T) {
	workspaceDir := t.TempDir()
	statePath := filepath.Join(workspaceDir, ".nexus", "state", "spotlight-forwards.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	seed := []map[string]any{{
		"id":          "spot-seed-1",
		"workspaceId": "ws-seed-1",
		"service":     "api",
		"remotePort":  8000,
		"localPort":   18000,
		"host":        "127.0.0.1",
		"createdAt":   "2026-04-09T12:00:00Z",
	}}
	data, _ := json.Marshal(seed)
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		t.Fatalf("write spotlight state: %v", err)
	}

	srv, err := NewServer(0, workspaceDir, "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	forwards := srv.spotlightMgr.List("ws-seed-1")
	if len(forwards) != 0 {
		t.Fatalf("expected legacy JSON spotlight file to be ignored, got %d forwards", len(forwards))
	}
}

func TestServer_ShutdownDoesNotWriteSpotlightJSON(t *testing.T) {
	workspaceDir := t.TempDir()
	srv, err := NewServer(0, workspaceDir, "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	_, exposeErr := srv.spotlightMgr.Expose(context.Background(), spotlight.ExposeSpec{
		WorkspaceID: "ws-1",
		Service:     "api",
		RemotePort:  8000,
		LocalPort:   18000,
	})
	if exposeErr != nil {
		t.Fatalf("expose spotlight forward: %v", exposeErr)
	}

	srv.Shutdown()

	statePath := filepath.Join(workspaceDir, ".nexus", "state", "spotlight-forwards.json")
	_, readErr := os.ReadFile(statePath)
	if !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("expected spotlight json state file to not exist, got err=%v", readErr)
	}

	resumed, err := NewServer(0, workspaceDir, "secret-token")
	if err != nil {
		t.Fatalf("new resumed server: %v", err)
	}

	forwards := resumed.spotlightMgr.List("ws-1")
	if len(forwards) != 1 {
		t.Fatalf("expected sqlite-backed spotlight persistence with 1 forward, got %d", len(forwards))
	}
	if forwards[0].LocalPort != 18000 {
		t.Fatalf("expected persisted local port 18000, got %d", forwards[0].LocalPort)
	}
}
