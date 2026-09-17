package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/portfwd"
)

type fakeHerdrServer struct {
	ln             net.Listener
	SocketPath     string
	snapshotID     atomic.Value
	subscribeCount atomic.Int32
	eventLines     []string
	closeAfterAck  bool
}

func newFakeHerdrServer(t *testing.T) *fakeHerdrServer {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "herdr.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &fakeHerdrServer{ln: ln, SocketPath: sockPath}
	s.snapshotID.Store("")
	return s
}

func (s *fakeHerdrServer) serve(t *testing.T) {
	t.Helper()
	go func() {
		for {
			conn, err := s.ln.Accept()
			if err != nil {
				return
			}
			go s.handleConn(t, conn)
		}
	}()
}

func (s *fakeHerdrServer) handleConn(t *testing.T, conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}
	var req struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		return
	}
	switch req.Method {
	case "session.snapshot":
		id := s.snapshotID.Load().(string)
		var resp string
		if id == "" {
			resp = fmt.Sprintf(`{"id":%q,"result":{"focused_workspace_id":null}}`, req.ID)
		} else {
			resp = fmt.Sprintf(`{"id":%q,"result":{"focused_workspace_id":%q}}`, req.ID, id)
		}
		fmt.Fprintln(conn, resp)
	case "events.subscribe":
		s.subscribeCount.Add(1)
		fmt.Fprintf(conn, `{"id":%q,"result":{}}`, req.ID)
		fmt.Fprintln(conn)
		if s.closeAfterAck {
			return
		}
		for _, line := range s.eventLines {
			fmt.Fprintln(conn, line)
		}
		io.Copy(io.Discard, conn) //nolint:errcheck
	case "workspace.report_metadata":
		fmt.Fprintf(conn, `{"id":%q,"result":{"type":"ok"}}`, req.ID)
		fmt.Fprintln(conn)
	}
}

func (s *fakeHerdrServer) waitSubscribeCount(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if int(s.subscribeCount.Load()) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d subscribe accepts; got %d", n, s.subscribeCount.Load())
}

func TestFocusWatch_EventUpdatesState(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "focus.state")
	storeRoot := filepath.Join(dir, "store")
	fwdStateDir := filepath.Join(dir, "fwd")

	srv := newFakeHerdrServer(t)
	srv.snapshotID.Store("")
	srv.eventLines = []string{
		`{"event":"workspace_focused","data":{"type":"workspace_focused","workspace_id":"W"}}`,
	}
	srv.serve(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go runHerdrFocusWatch(ctx, srv.SocketPath, "", storeRoot, statePath, fwdStateDir, 30*time.Second, io.Discard)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, ok, err := portfwd.ReadFocusState(statePath)
		if err == nil && ok && s.WorkspaceID == "W" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s, ok, _ := portfwd.ReadFocusState(statePath)
	t.Fatalf("focus.state not updated: ok=%v workspace_id=%q (want W)", ok, s.WorkspaceID)
}

func TestFocusWatch_PollCatchesDivergence(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "focus.state")
	storeRoot := filepath.Join(dir, "store")
	fwdStateDir := filepath.Join(dir, "fwd")
	pollEvery := 100 * time.Millisecond

	srv := newFakeHerdrServer(t)
	srv.snapshotID.Store("W")
	srv.eventLines = nil
	srv.serve(t)

	initial := portfwd.FocusState{WorkspaceID: "V", SandboxID: "", UpdatedAt: time.Now().UTC()}
	if err := portfwd.WriteFocusState(statePath, initial); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go runHerdrFocusWatch(ctx, srv.SocketPath, "", storeRoot, statePath, fwdStateDir, pollEvery, io.Discard)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, ok, err := portfwd.ReadFocusState(statePath)
		if err == nil && ok && s.WorkspaceID == "W" {
			goto partB
		}
		_ = s
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("part A: focus.state not updated to W within timeout")

partB:
	time.Sleep(2 * pollEvery)
	info1, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mtime1 := info1.ModTime()

	time.Sleep(4 * pollEvery)

	info2, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info2.ModTime().Equal(mtime1) {
		t.Errorf("mtime changed on identical poll: before=%v after=%v (mutation: always-write detected)", mtime1, info2.ModTime())
	}
}

func TestFocusWatch_Reconnect(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "focus.state")
	storeRoot := filepath.Join(dir, "store")
	fwdStateDir := filepath.Join(dir, "fwd")

	srv := newFakeHerdrServer(t)
	srv.snapshotID.Store("")
	srv.closeAfterAck = true
	srv.serve(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go runHerdrFocusWatch(ctx, srv.SocketPath, "", storeRoot, statePath, fwdStateDir, 30*time.Second, io.Discard)

	srv.waitSubscribeCount(t, 2, 8*time.Second)
}
