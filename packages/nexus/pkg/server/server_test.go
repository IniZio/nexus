package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
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
					d.mu.Unlock()
				}
				if typ == "shell.resize" {
					d.mu.Lock()
					d.resizeCalled = true
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

func TestPTYOpenUsesRemoteConnectorForFirecrackerAndLXC(t *testing.T) {
	t.Run("firecracker", func(t *testing.T) {
		testPTYOpenUsesRemoteConnectorForBackend(t, "firecracker")
	})
	t.Run("lxc", func(t *testing.T) {
		testPTYOpenUsesRemoteConnectorForBackend(t, "lxc")
	})
}

func testPTYOpenUsesRemoteConnectorForBackend(t *testing.T, backend string) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	driver := &serverTestConnectorDriver{serverTestDriver: serverTestDriver{backend: backend}}
	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}, {Name: "runtime.lxc", Available: true}},
		map[string]runtime.Driver{"firecracker": driver, "lxc": driver},
	)
	srv.SetRuntimeFactory(factory)

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, backend)
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

	deadline := time.Now().Add(200 * time.Millisecond)
	var wrote, resized bool
	for time.Now().Before(deadline) {
		wrote, resized = driver.actionDetails()
		if wrote && resized {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !wrote || (backend == "firecracker" && !resized) {
		t.Fatalf("expected remote shell write+resize calls, got write=%v resize=%v", wrote, resized)
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
	if !strings.Contains(body, "Nexus Admin") {
		t.Fatalf("expected Vite UI index content, got: %s", body)
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
	if !strings.Contains(body, "Nexus Admin") {
		t.Fatalf("expected portal UI content fallback, got: %s", body)
	}
}
