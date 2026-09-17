package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func logFixturePath(t *testing.T, srv *fakeHerdrServer) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(srv.SocketPath), "herdr-server.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create fixture log: %v", err)
	}
	f.Close()
	return p
}

func appendLogLine(t *testing.T, logPath, workspaceID string) {
	t.Helper()
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open log for append: %v", err)
	}
	defer f.Close()
	fmt.Fprintf(f, `2026-09-17T04:00:19.593634Z  INFO herdr::logging: workspace focused event="workspace.focus" subsystem="workspace" outcome="ok" workspace_id=%q`+"\n", workspaceID)
}

func waitFocusState(t *testing.T, statePath, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s, ok, err := portfwd.ReadFocusState(statePath)
		if err == nil && ok && s.WorkspaceID == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestFocusWatch_LogSource_AC1(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "focus.state")
	storeRoot := filepath.Join(dir, "store")
	fwdStateDir := filepath.Join(dir, "fwd")

	srv := newFakeHerdrServer(t)
	srv.snapshotID.Store("")
	srv.serve(t)
	logPath := logFixturePath(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go runHerdrFocusWatch(ctx, srv.SocketPath, "", storeRoot, statePath, fwdStateDir, 30*time.Second, io.Discard)

	time.Sleep(100 * time.Millisecond)
	appendLogLine(t, logPath, "W")

	if !waitFocusState(t, statePath, "W", time.Second) {
		s, ok, _ := portfwd.ReadFocusState(statePath)
		t.Fatalf("AC1: focus.state not updated within 1s: ok=%v workspace_id=%q (want W)", ok, s.WorkspaceID)
	}
}

func TestFocusWatch_LogSource_AC2(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rotate func(logPath string)
	}{
		{
			name: "truncate",
			rotate: func(logPath string) {
				if err := os.Truncate(logPath, 0); err != nil {
					t.Errorf("truncate: %v", err)
				}
			},
		},
		{
			name: "rename_create",
			rotate: func(logPath string) {
				if err := os.Rename(logPath, logPath+".old"); err != nil {
					t.Errorf("rename: %v", err)
				}
				f, err := os.Create(logPath)
				if err != nil {
					t.Errorf("create new log: %v", err)
					return
				}
				f.Close()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			statePath := filepath.Join(dir, "focus.state")
			storeRoot := filepath.Join(dir, "store")
			fwdStateDir := filepath.Join(dir, "fwd")

			srv := newFakeHerdrServer(t)
			srv.snapshotID.Store("")
			srv.serve(t)
			logPath := logFixturePath(t, srv)

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()

			go runHerdrFocusWatch(ctx, srv.SocketPath, "", storeRoot, statePath, fwdStateDir, 30*time.Second, io.Discard)

			time.Sleep(150 * time.Millisecond)
			appendLogLine(t, logPath, "W")
			if !waitFocusState(t, statePath, "W", 2*time.Second) {
				t.Fatalf("AC2 %s: initial W not applied", tc.name)
			}

			time.Sleep(100 * time.Millisecond)
			tc.rotate(logPath)
			time.Sleep(100 * time.Millisecond)
			appendLogLine(t, logPath, "V")

			if !waitFocusState(t, statePath, "V", 2*time.Second) {
				s, ok, _ := portfwd.ReadFocusState(statePath)
				t.Fatalf("AC2 %s: V not applied after rotation: ok=%v workspace_id=%q", tc.name, ok, s.WorkspaceID)
			}
		})
	}
}

func TestFocusWatch_LogSource_NoReplayOnStart(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "focus.state")
	storeRoot := filepath.Join(dir, "store")
	fwdStateDir := filepath.Join(dir, "fwd")

	srv := newFakeHerdrServer(t)
	srv.snapshotID.Store("")
	srv.serve(t)
	logPath := filepath.Join(filepath.Dir(srv.SocketPath), "herdr-server.log")

	lf, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	for i := range 50 {
		fmt.Fprintf(lf, `2026-09-17T04:00:%02dZ  INFO herdr::logging: workspace focused event="workspace.focus" subsystem="workspace" outcome="ok" workspace_id="hist%d"`+"\n", i%60, i)
	}
	lf.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go runHerdrFocusWatch(ctx, srv.SocketPath, "", storeRoot, statePath, fwdStateDir, 30*time.Second, io.Discard)

	time.Sleep(1500 * time.Millisecond)

	if _, err := os.Stat(statePath); err == nil {
		s, _, _ := portfwd.ReadFocusState(statePath)
		t.Fatalf("NoReplay: focus.state was written during history replay: workspace_id=%q", s.WorkspaceID)
	}

	appendLogLine(t, logPath, "NEW")
	if !waitFocusState(t, statePath, "NEW", 2*time.Second) {
		s, ok, _ := portfwd.ReadFocusState(statePath)
		t.Fatalf("NoReplay: NEW not applied after append: ok=%v workspace_id=%q", ok, s.WorkspaceID)
	}
}

func TestFocusWatch_LogSource_SameSizeTruncation(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "focus.state")
	storeRoot := filepath.Join(dir, "store")
	fwdStateDir := filepath.Join(dir, "fwd")

	srv := newFakeHerdrServer(t)
	srv.snapshotID.Store("")
	srv.serve(t)
	logPath := logFixturePath(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	go runHerdrFocusWatch(ctx, srv.SocketPath, "", storeRoot, statePath, fwdStateDir, 30*time.Second, io.Discard)

	time.Sleep(150 * time.Millisecond)
	appendLogLine(t, logPath, "W")
	if !waitFocusState(t, statePath, "W", 2*time.Second) {
		t.Fatal("SameSizeTrunc: initial W not applied")
	}

	time.Sleep(50 * time.Millisecond)
	wLine := fmt.Sprintf(`2026-09-17T04:00:19.593634Z  INFO herdr::logging: workspace focused event="workspace.focus" subsystem="workspace" outcome="ok" workspace_id=%q`+"\n", "W")
	vLine := fmt.Sprintf(`2026-09-17T04:00:19.593634Z  INFO herdr::logging: workspace focused event="workspace.focus" subsystem="workspace" outcome="ok" workspace_id=%q`+"\n", "V")
	if len(wLine) != len(vLine) {
		t.Fatalf("SameSizeTrunc: precondition failed: W line len %d != V line len %d", len(wLine), len(vLine))
	}

	rf, err := os.OpenFile(logPath, os.O_RDWR|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open for trunc+rewrite: %v", err)
	}
	fmt.Fprint(rf, vLine)
	rf.Close()

	if !waitFocusState(t, statePath, "V", 2*time.Second) {
		s, ok, _ := portfwd.ReadFocusState(statePath)
		t.Fatalf("SameSizeTrunc: V not applied: ok=%v workspace_id=%q", ok, s.WorkspaceID)
	}
}

func TestFocusWatch_LogSource_AC3(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "focus.state")
	storeRoot := filepath.Join(dir, "store")
	fwdStateDir := filepath.Join(dir, "fwd")

	srv := newFakeHerdrServer(t)
	srv.snapshotID.Store("")
	srv.serve(t)
	logPath := logFixturePath(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	go runHerdrFocusWatch(ctx, srv.SocketPath, "", storeRoot, statePath, fwdStateDir, 30*time.Second, io.Discard)

	time.Sleep(150 * time.Millisecond)
	appendLogLine(t, logPath, "W")
	if !waitFocusState(t, statePath, "W", 2*time.Second) {
		t.Fatal("AC3: initial W not applied")
	}

	time.Sleep(50 * time.Millisecond)
	info1, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mtime1 := info1.ModTime()

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	fmt.Fprintln(f, `2026-09-17T04:00:20Z  INFO herdr::logging: workspace focused event="workspace.focus" subsystem="workspace" outcome="error" workspace_id="X"`)
	fmt.Fprintln(f, `2026-09-17T04:00:20Z  INFO herdr::logging: unrelated line subsystem="other"`)
	fmt.Fprintf(f, `2026-09-17T04:00:20Z  INFO herdr::logging: workspace focused event="workspace.focus" subsystem="workspace" outcome="ok" workspace_id=%q`+"\n", "W")
	f.Close()

	time.Sleep(600 * time.Millisecond)

	info2, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat after noise: %v", err)
	}
	if !info2.ModTime().Equal(mtime1) {
		t.Errorf("AC3: mtime changed on noise/duplicate lines: before=%v after=%v", mtime1, info2.ModTime())
	}
	s, _, _ := portfwd.ReadFocusState(statePath)
	if s.WorkspaceID != "W" {
		t.Errorf("AC3: workspace_id changed: got %q want W", s.WorkspaceID)
	}
}

func TestFocusWatch_LogCoalesce(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "focus.state")
	storeRoot := filepath.Join(dir, "store")
	fwdStateDir := filepath.Join(dir, "fwd")

	srv := newFakeHerdrServer(t)
	srv.snapshotID.Store("")
	srv.serve(t)
	logPath := logFixturePath(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	go runHerdrFocusWatch(ctx, srv.SocketPath, "", storeRoot, statePath, fwdStateDir, 30*time.Second, io.Discard)

	time.Sleep(150 * time.Millisecond)

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	for i := range 20 {
		id := fmt.Sprintf("id%02d", i)
		fmt.Fprintf(f, `2026-09-17T04:00:%02dZ  INFO herdr::logging: workspace focused event="workspace.focus" subsystem="workspace" outcome="ok" workspace_id=%q`+"\n", i%60, id)
	}
	f.Close()

	if !waitFocusState(t, statePath, "id19", 3*time.Second) {
		s, ok, _ := portfwd.ReadFocusState(statePath)
		t.Fatalf("LogCoalesce: last id not applied: ok=%v workspace_id=%q (want id19)", ok, s.WorkspaceID)
	}
}

func TestFocusWatch_PidAlive(t *testing.T) {
	cases := []struct {
		name    string
		cmdline []byte
		err     error
		want    bool
	}{
		{"nexus3 focus-watch", []byte("nexus3\x00herdr\x00focus-watch"), nil, true},
		{"full path local-agent-startup", []byte("/home/x/.local/bin/nexus3\x00herdr\x00local-agent-startup"), nil, true},
		{"unrelated bash", []byte("bash\x00-c\x00sleep"), nil, false},
		{"read error", nil, errors.New("no such process"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := focusWatchReadCmdline
			focusWatchReadCmdline = func(pid int) ([]byte, error) { return tc.cmdline, tc.err }
			defer func() { focusWatchReadCmdline = old }()
			got := focusWatchPidAlive(42)
			if got != tc.want {
				t.Errorf("focusWatchPidAlive = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFocusWatch_PidfileDedup(t *testing.T) {
	dir := t.TempDir()
	pidfilePath := filepath.Join(dir, "focus-watch.pid")

	fakePid := os.Getpid()
	old := focusWatchPidAlive
	focusWatchPidAlive = func(pid int) bool { return pid == fakePid }
	defer func() { focusWatchPidAlive = old }()

	if err := os.WriteFile(pidfilePath, []byte(fmt.Sprintf("%d\n", fakePid)), 0o600); err != nil {
		t.Fatal(err)
	}

	acquired, cleanup, err := acquireFocusWatchPidfile(pidfilePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if acquired {
		t.Fatal("must not acquire pidfile when a live holder exists")
	}

	data, _ := os.ReadFile(pidfilePath)
	if !strings.Contains(string(data), fmt.Sprintf("%d", fakePid)) {
		t.Error("pidfile must not be removed when live holder exists")
	}
}
