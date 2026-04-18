package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/inizio/nexus/packages/nexus/pkg/server/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestNewServer_FallsBackToInMemorySpotlightManagerOnRepositoryInitError(t *testing.T) {
	originalFactory := newSpotlightManagerForServer
	t.Cleanup(func() {
		newSpotlightManagerForServer = originalFactory
	})

	newSpotlightManagerForServer = func(_ *workspacemgr.Manager) (*spotlight.Manager, error) {
		return nil, fmt.Errorf("forced spotlight hydration failure")
	}

	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("expected NewServer to succeed when spotlight repository hydration fails, got err: %v", err)
	}

	forwards := srv.spotlightMgr.List("")
	if len(forwards) != 0 {
		t.Fatalf("expected empty in-memory spotlight manager after fallback, got %d forwards", len(forwards))
	}
}

func TestResolveWorkspacePrefersLocalWorktreePath(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "firecracker")
	localWorktree := t.TempDir()
	if err := srv.workspaceMgr.SetLocalWorktree(ws.ID, localWorktree, ""); err != nil {
		t.Fatalf("set local worktree: %v", err)
	}

	resolved := srv.resolveWorkspaceTyped(struct {
		WorkspaceID string `json:"workspaceId"`
	}{
		WorkspaceID: ws.ID,
	})

	want := localWorktree
	if real, err := filepath.EvalSymlinks(localWorktree); err == nil {
		want = real
	}
	if got := resolved.Path(); got != want {
		t.Fatalf("expected resolveWorkspace to prefer local worktree path %q, got %q", want, got)
	}
}

func TestRPCRegistry_ProjectMethodsAreRegistered(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if srv.rpcReg == nil {
		t.Fatal("expected rpc registry")
	}

	raw := json.RawMessage(`{}`)
	if _, rpcErr := srv.rpcReg.Dispatch(context.Background(), "project.list", "1", raw, nil); rpcErr != nil {
		t.Fatalf("expected project.list to be registered, got rpc error: %+v", rpcErr)
	}
	if _, rpcErr := srv.rpcReg.Dispatch(context.Background(), "project.create", "1.5", json.RawMessage(`{"repo":"git@example/repo.git"}`), nil); rpcErr != nil {
		t.Fatalf("expected project.create to be registered, got rpc error: %+v", rpcErr)
	}
	if _, rpcErr := srv.rpcReg.Dispatch(context.Background(), "project.get", "2", json.RawMessage(`{"id":"missing"}`), nil); rpcErr == nil {
		t.Fatal("expected project.get to execute and return a handler error, got nil")
	} else if rpcErr.Code == rpckit.ErrMethodNotFound.Code {
		t.Fatalf("expected project.get to be registered, got method not found: %+v", rpcErr)
	}
	if _, rpcErr := srv.rpcReg.Dispatch(context.Background(), "daemon.settings.get", "3", json.RawMessage(`{}`), nil); rpcErr != nil {
		t.Fatalf("expected daemon.settings.get to be registered, got rpc error: %+v", rpcErr)
	}
	if _, rpcErr := srv.rpcReg.Dispatch(context.Background(), "daemon.settings.update", "4", json.RawMessage(`{"sandboxResources":{"defaultMemoryMiB":1024,"defaultVCPUs":1,"maxMemoryMiB":4096,"maxVCPUs":4}}`), nil); rpcErr != nil {
		t.Fatalf("expected daemon.settings.update to be registered, got rpc error: %+v", rpcErr)
	}
}

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
func (d *serverTestDriver) GuestWorkdir(workspaceID string) string {
	return "/workspace"
}

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
					if data, ok := req["data"].(string); ok && strings.Contains(data, "__NEXUS_TMUX_OK__") {
						_ = enc.Encode(map[string]any{
							"id":   sessionID,
							"type": "chunk",
							"data": "__NEXUS_TMUX_OK__\n",
						})
					}
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
				if typ == "shell.write" || typ == "shell.resize" {
					_ = enc.Encode(map[string]any{"id": sessionID, "type": "ack", "ok": true})
					continue
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
	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*pty.Session{}}

	payload, _ := json.Marshal(map[string]any{
		"workspaceId": ws.ID,
		"cols":        80,
		"rows":        24,
	})

	result, rpcErr := pty.HandleOpen(srv.ptyDeps(), conn, payload, srv.ws)
	if rpcErr != nil {
		t.Fatalf("pty.open rpc error: %+v", rpcErr)
	}
	if result == nil {
		t.Fatal("expected pty.open result")
	}

	open, ok := result.(*pty.OpenResult)
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
	factory := runtime.NewFactory(
		capabilities,
		drivers,
	)
	srv.SetRuntimeFactory(factory)

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, backend)
	if err := srv.workspaceMgr.Start(ws.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*pty.Session{}}

	payload, _ := json.Marshal(map[string]any{
		"workspaceId": ws.ID,
		"cols":        80,
		"rows":        24,
	})

	result, rpcErr := pty.HandleOpen(srv.ptyDeps(), conn, payload, srv.ws)
	if rpcErr != nil {
		t.Fatalf("pty.open rpc error: %+v", rpcErr)
	}
	if result == nil {
		t.Fatal("expected pty.open result")
	}

	open, ok := result.(*pty.OpenResult)
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
	wantWorkdir := "/workspace"
	if openWorkdir != wantWorkdir {
		t.Fatalf("expected remote shell workdir %s, got %q", wantWorkdir, openWorkdir)
	}

	writeParams, _ := json.Marshal(map[string]any{"sessionId": open.SessionID, "data": "echo ok\n"})
	if _, rpcErr := pty.HandleWrite(srv.ptyDeps(), writeParams, conn); rpcErr != nil {
		t.Fatalf("pty.write rpc error: %+v", rpcErr)
	}

	if backend == "firecracker" {
		resizeParams, _ := json.Marshal(map[string]any{"sessionId": open.SessionID, "cols": 100, "rows": 30})
		if _, rpcErr := pty.HandleResize(srv.ptyDeps(), resizeParams, conn); rpcErr != nil {
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
	closeResult, rpcErr := pty.HandleClose(srv.ptyDeps(), closeParams, conn)
	if rpcErr != nil {
		t.Fatalf("pty.close rpc error: %+v", rpcErr)
	}
	if closeResultMap, ok := closeResult.(map[string]bool); !ok || !closeResultMap["closed"] {
		t.Fatalf("expected close result {closed:true}, got %#v", closeResult)
	}

	closeResult, rpcErr = pty.HandleClose(srv.ptyDeps(), closeParams, conn)
	if rpcErr != nil {
		t.Fatalf("pty.close idempotent rpc error: %+v", rpcErr)
	}
	if closeResultMap, ok := closeResult.(map[string]bool); !ok || !closeResultMap["closed"] {
		t.Fatalf("expected idempotent close result {closed:true}, got %#v", closeResult)
	}
}

func TestPTYAttachAllowsReattachAfterConnectionDetach(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	driver := &serverTestConnectorDriver{
		serverTestDriver: serverTestDriver{backend: "firecracker"},
		writeSignal:      make(chan struct{}, 1),
		resizeSignal:     make(chan struct{}, 1),
	}
	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{"firecracker": driver},
	)
	srv.SetRuntimeFactory(factory)

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "firecracker")
	if err := srv.workspaceMgr.Start(ws.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}

	conn1 := &Connection{send: make(chan []byte, 16), clientID: "test-1", pty: map[string]*pty.Session{}}
	openPayload, _ := json.Marshal(map[string]any{
		"workspaceId": ws.ID,
		"cols":        80,
		"rows":        24,
	})
	openResult, rpcErr := pty.HandleOpen(srv.ptyDeps(), conn1, openPayload, srv.ws)
	if rpcErr != nil {
		t.Fatalf("pty.open rpc error: %+v", rpcErr)
	}
	open := openResult.(*pty.OpenResult)

	conn2 := &Connection{send: make(chan []byte, 16), clientID: "test-2", pty: map[string]*pty.Session{}}
	attachPayload, _ := json.Marshal(map[string]any{"sessionId": open.SessionID})
	attachResult, rpcErr := pty.HandleAttach(srv.ptyDeps(), attachPayload, conn2)
	if rpcErr != nil {
		t.Fatalf("pty.attach rpc error: %+v", rpcErr)
	}
	if result, ok := attachResult.(*pty.AttachResult); !ok || !result.Attached {
		t.Fatalf("expected attached=true, got %#v", attachResult)
	}

	// Simulate connection 1 disconnect without killing PTY.
	srv.ptyRegistry.UnsubscribeConn(conn1)
	conn1.DetachAllPTY()

	writePayload, _ := json.Marshal(map[string]any{"sessionId": open.SessionID, "data": "echo reattach\n"})
	if _, rpcErr := pty.HandleWrite(srv.ptyDeps(), writePayload, conn2); rpcErr != nil {
		t.Fatalf("pty.write via reattached connection failed: %+v", rpcErr)
	}

	select {
	case <-driver.writeSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("expected write to reach remote shell after reattach")
	}

	closePayload, _ := json.Marshal(map[string]any{"sessionId": open.SessionID})
	if _, rpcErr := pty.HandleClose(srv.ptyDeps(), closePayload, conn2); rpcErr != nil {
		t.Fatalf("pty.close via reattached connection failed: %+v", rpcErr)
	}
}

func TestPTYListRecoversTmuxSessionAfterDaemonRestart(t *testing.T) {
	workspaceDir := t.TempDir()

	srv1, err := NewServer(0, workspaceDir, "secret-token")
	if err != nil {
		t.Fatalf("new server(1): %v", err)
	}
	driver1 := &serverTestConnectorDriver{
		serverTestDriver: serverTestDriver{backend: "firecracker"},
		writeSignal:      make(chan struct{}, 2),
		resizeSignal:     make(chan struct{}, 1),
	}
	srv1.SetRuntimeFactory(runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{"firecracker": driver1},
	))

	ws := createWorkspaceForPTYTest(t, srv1.workspaceMgr, "firecracker")
	if err := srv1.workspaceMgr.Start(ws.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	conn1 := &Connection{send: make(chan []byte, 16), clientID: "test-1", pty: map[string]*pty.Session{}}
	openPayload, _ := json.Marshal(map[string]any{
		"workspaceId": ws.ID,
		"cols":        80,
		"rows":        24,
		"useTmux":     true,
	})
	openResult, rpcErr := pty.HandleOpen(srv1.ptyDeps(), conn1, openPayload, srv1.ws)
	if rpcErr != nil {
		t.Fatalf("pty.open rpc error: %+v", rpcErr)
	}
	open := openResult.(*pty.OpenResult)
	if open.SessionID == "" {
		t.Fatal("expected session id")
	}

	srv2, err := NewServer(0, workspaceDir, "secret-token")
	if err != nil {
		t.Fatalf("new server(2): %v", err)
	}
	driver2 := &serverTestConnectorDriver{
		serverTestDriver: serverTestDriver{backend: "firecracker"},
		writeSignal:      make(chan struct{}, 2),
		resizeSignal:     make(chan struct{}, 1),
	}
	srv2.SetRuntimeFactory(runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{"firecracker": driver2},
	))

	listPayload, _ := json.Marshal(map[string]any{"workspaceId": ws.ID})
	listResult, rpcErr := pty.HandleList(srv2.ptyDeps(), listPayload)
	if rpcErr != nil {
		t.Fatalf("pty.list rpc error: %+v", rpcErr)
	}
	list := listResult.(*pty.ListResult)
	if len(list.Sessions) != 1 {
		t.Fatalf("expected recovered session list length=1, got %d", len(list.Sessions))
	}
	if list.Sessions[0].ID != open.SessionID {
		t.Fatalf("expected recovered session id %s, got %s", open.SessionID, list.Sessions[0].ID)
	}
	if !list.Sessions[0].IsTmux {
		t.Fatal("expected recovered session to be tmux-backed")
	}

	conn2 := &Connection{send: make(chan []byte, 16), clientID: "test-2", pty: map[string]*pty.Session{}}
	attachPayload, _ := json.Marshal(map[string]any{"sessionId": open.SessionID})
	attachResult, rpcErr := pty.HandleAttach(srv2.ptyDeps(), attachPayload, conn2)
	if rpcErr != nil {
		t.Fatalf("pty.attach rpc error: %+v", rpcErr)
	}
	if result, ok := attachResult.(*pty.AttachResult); !ok || !result.Attached {
		t.Fatalf("expected attached=true, got %#v", attachResult)
	}

	writePayload, _ := json.Marshal(map[string]any{"sessionId": open.SessionID, "data": "echo recovered\n"})
	if _, rpcErr := pty.HandleWrite(srv2.ptyDeps(), writePayload, conn2); rpcErr != nil {
		t.Fatalf("pty.write via recovered session failed: %+v", rpcErr)
	}
	select {
	case <-driver2.writeSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("expected write to reach recovered remote shell")
	}
}

func TestPrunePersistedPTYSessionsRemovesMissingWorkspaceEntries(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := srv.ptyStore.Upsert(pty.SessionInfo{
		ID:          "pty-stale",
		WorkspaceID: "ws-missing",
		Name:        "stale",
		WorkDir:     "/workspace",
		IsTmux:      true,
		TmuxSession: "nexus_ws_missing_pty_stale",
	}); err != nil {
		t.Fatalf("seed pty store: %v", err)
	}

	removed := srv.PrunePersistedPTYSessions(context.Background())
	if removed != 1 {
		t.Fatalf("expected removed=1, got %d", removed)
	}
	entries, err := srv.ptyStore.List()
	if err != nil {
		t.Fatalf("list pty store: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty persisted pty store, got %d entries", len(entries))
	}
}

func TestPTYOpenRejectsWorkspaceNotStarted(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "")
	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*pty.Session{}}

	payload, _ := json.Marshal(map[string]any{"workspaceId": ws.ID})
	_, rpcErr := pty.HandleOpen(srv.ptyDeps(), conn, payload, srv.ws)
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

	resp := srv.processRPC(msg, &Connection{send: make(chan []byte, 1), clientID: "test", pty: map[string]*pty.Session{}})
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

	resp := srv.processRPC(msg, &Connection{send: make(chan []byte, 1), clientID: "test", pty: map[string]*pty.Session{}})
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

	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*pty.Session{}}
	payload, _ := json.Marshal(map[string]any{"workspaceId": ws.ID, "shell": "sh", "rows": 12, "cols": 40})
	result, rpcErr := pty.HandleOpen(srv.ptyDeps(), conn, payload, srv.ws)
	if rpcErr != nil {
		t.Fatalf("expected started workspace to open PTY, got %+v", rpcErr)
	}
	if result == nil {
		t.Fatal("expected pty.open result")
	}

	open := result.(*pty.OpenResult)
	closeParams, _ := json.Marshal(map[string]any{"sessionId": open.SessionID})
	if _, rpcErr := pty.HandleClose(srv.ptyDeps(), closeParams, conn); rpcErr != nil {
		t.Fatalf("pty.close rpc error: %+v", rpcErr)
	}
}

func TestPTYOpenRejectsMissingWorkspaceID(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*pty.Session{}}
	payload, _ := json.Marshal(map[string]any{"shell": "sh", "rows": 12, "cols": 40})
	_, rpcErr := pty.HandleOpen(srv.ptyDeps(), conn, payload, srv.ws)
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

	resp := srv.processRPC(msg, &Connection{send: make(chan []byte, 1), clientID: "test", pty: map[string]*pty.Session{}})
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
	if len(forwards) != 0 {
		t.Fatalf("expected no active tunnels persisted across restart, got %d", len(forwards))
	}
}
