package pty

import (
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	creackpty "github.com/creack/pty"
)

type testConn struct {
	mu       sync.Mutex
	sessions map[string]*Session
	queued   [][]byte
}

func newTestConn() *testConn {
	return &testConn{
		sessions: make(map[string]*Session),
	}
}

func (c *testConn) Enqueue(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copyData := make([]byte, len(data))
	copy(copyData, data)
	c.queued = append(c.queued, copyData)
}

func (c *testConn) GetPTY(id string) *Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[id]
}

func (c *testConn) SetPTY(id string, s *Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[id] = s
}

func (c *testConn) RemovePTY(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, id)
}

func (c *testConn) ClosePTY(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.sessions[id]
	delete(c.sessions, id)
	return ok
}

func (c *testConn) CloseAllPTY() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = make(map[string]*Session)
}

func (c *testConn) hasMethod(method string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, raw := range c.queued {
		var msg struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Method == method {
			return true
		}
	}
	return false
}

func (c *testConn) exitCodeForSession(sessionID string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, raw := range c.queued {
		var msg struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil || msg.Method != "pty.exit" {
			continue
		}
		var params struct {
			SessionID string `json:"sessionId"`
			ExitCode  int    `json:"exitCode"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			continue
		}
		if params.SessionID == sessionID {
			return params.ExitCode, true
		}
	}
	return 0, false
}

func TestStreamPTYOutput_BroadcastsExitBeforeUnregister(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	conn := newTestConn()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer reader.Close()

	session := &Session{
		ID:          "session-1",
		WorkspaceID: "ws-1",
		Cmd:         exec.Command("sh"),
		File:        reader,
	}
	registry.Register(session)
	if ok := registry.Subscribe(session.ID, conn); !ok {
		t.Fatalf("subscribe failed")
	}

	done := make(chan struct{})
	go func() {
		streamPTYOutput(conn, session, registry, nil)
		close(done)
	}()

	if _, err := writer.Write([]byte("hello\n")); err != nil {
		t.Fatalf("writer write: %v", err)
	}
	_ = writer.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamPTYOutput did not terminate")
	}

	if !conn.hasMethod("pty.data") {
		t.Fatal("expected pty.data notification")
	}
	if !conn.hasMethod("pty.exit") {
		t.Fatal("expected pty.exit notification")
	}
}

func TestStreamPTYOutput_ReportsRealProcessExitCode(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	conn := newTestConn()

	cmd := exec.Command("sh", "-c", "exit 0")
	ptmx, err := creackpty.Start(cmd)
	if err != nil {
		t.Fatalf("start pty command: %v", err)
	}
	defer ptmx.Close()

	session := &Session{
		ID:          "session-exit-code",
		WorkspaceID: "ws-1",
		Cmd:         cmd,
		File:        ptmx,
	}
	registry.Register(session)
	if ok := registry.Subscribe(session.ID, conn); !ok {
		t.Fatalf("subscribe failed")
	}

	done := make(chan struct{})
	go func() {
		streamPTYOutput(conn, session, registry, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("streamPTYOutput did not terminate")
	}

	exitCode, ok := conn.exitCodeForSession(session.ID)
	if !ok {
		t.Fatal("expected pty.exit notification")
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
}
