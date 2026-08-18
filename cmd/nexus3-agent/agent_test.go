package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
)

const testBufSize = 1 << 20 // 1 MiB

// testHarness starts an in-process agent and returns:
//   - a gRPC client connected to the control plane
//   - the data-plane bufconn listener (for direct dial)
//   - a cancel function to stop the agent
func testHarness(t *testing.T) (agentpb.AgentServiceClient, *bufconn.Listener, context.CancelFunc) {
	t.Helper()

	ctrlLis := bufconn.Listen(testBufSize)
	dataLis := bufconn.Listen(testBufSize)

	a := New(ctrlLis, dataLis)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		_ = a.Run(ctx)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///nexus3-agent-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return ctrlLis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		cancel()
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return agentpb.NewAgentServiceClient(conn), dataLis, cancel
}

// dialData opens a raw data-plane connection and returns conn+writer+reader.
func dialData(t *testing.T, lis *bufconn.Listener) (net.Conn, *wire.Writer, *wire.Reader) {
	t.Helper()
	conn, err := lis.Dial()
	if err != nil {
		t.Fatalf("data Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, wire.NewWriter(conn), wire.NewReader(conn)
}

// collectFrames reads frames until an Exit frame or the deadline passes.
// Returns all frames received.
func collectFrames(t *testing.T, conn net.Conn, r *wire.Reader, timeout time.Duration) []wire.Frame {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	var frames []wire.Frame
	for {
		f, err := r.ReadFrame()
		if err != nil {
			break
		}
		frames = append(frames, f)
		if f.Type == wire.FrameExit {
			break
		}
	}
	return frames
}

// dataBytes concatenates the payloads of all Data frames.
func dataBytes(frames []wire.Frame) []byte {
	var buf bytes.Buffer
	for _, f := range frames {
		if f.Type == wire.FrameData && f.Data != nil {
			buf.Write(f.Data.Payload)
		}
	}
	return buf.Bytes()
}

// hasExitFrame reports whether frames contains an Exit frame.
func hasExitFrame(frames []wire.Frame) (bool, int32) {
	for _, f := range frames {
		if f.Type == wire.FrameExit && f.Exit != nil {
			return true, f.Exit.Code
		}
	}
	return false, 0
}

// Test: Exec → data-plane streams output → reattach from offset

func TestExecStreamAndReattach(t *testing.T) {
	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()

	// Exec a command that writes a known string and exits.
	_, err := client.Exec(ctx, &agentpb.ExecRequest{
		SessionId: "s-reattach",
		Argv:      []string{"sh", "-c", "printf NEXUS3"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// First connection at offset 0
	conn1, w1, r1 := dialData(t, dataLis)

	if err := w1.WriteHandshake(wire.Handshake{SessionID: "s-reattach", ResumeFromOffset: 0}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}

	ack1, err := r1.ReadFrame()
	if err != nil || ack1.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck, got type=%v err=%v", ack1.Type, err)
	}

	frames1 := collectFrames(t, conn1, r1, 5*time.Second)

	out1 := dataBytes(frames1)
	if !bytes.Contains(out1, []byte("NEXUS3")) {
		t.Fatalf("expected 'NEXUS3' in output, got %q", out1)
	}
	gotExit, code := hasExitFrame(frames1)
	if !gotExit {
		t.Fatal("expected Exit frame, not received")
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}

	// "NEXUS3" is 6 bytes. Reconnect at offset 3 → expect "US3".
	// Second connection at offset 3
	conn2, w2, r2 := dialData(t, dataLis)

	if err := w2.WriteHandshake(wire.Handshake{SessionID: "s-reattach", ResumeFromOffset: 3}); err != nil {
		t.Fatalf("WriteHandshake2: %v", err)
	}

	ack2, err := r2.ReadFrame()
	if err != nil || ack2.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck2, got type=%v err=%v", ack2.Type, err)
	}
	// Status may be Alive or Exited depending on timing — both are valid.

	frames2 := collectFrames(t, conn2, r2, 5*time.Second)

	out2 := dataBytes(frames2)
	if string(out2) != "US3" {
		t.Fatalf("reattach offset 3: got %q, want \"US3\"", out2)
	}
	if gotExit2, _ := hasExitFrame(frames2); !gotExit2 {
		t.Fatal("expected Exit frame on reattach, not received")
	}
}

// Test: stdin forwarding – cat echoes what we write

func TestStdinForwarding(t *testing.T) {
	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()

	_, err := client.Exec(ctx, &agentpb.ExecRequest{
		SessionId: "s-stdin",
		Argv:      []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	conn, w, r := dialData(t, dataLis)

	if err := w.WriteHandshake(wire.Handshake{SessionID: "s-stdin", ResumeFromOffset: 0}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: got type=%v err=%v", ack.Type, err)
	}
	if ack.HandshakeAck.Status != wire.AckAlive {
		t.Fatalf("expected AckAlive, got %v", ack.HandshakeAck.Status)
	}

	// Send text to stdin and expect it echoed back.
	payload := []byte("hello nexus3\n")
	if err := w.WriteData(wire.StreamStdin, payload); err != nil {
		t.Fatalf("WriteData stdin: %v", err)
	}

	// Read frames until we see the echo or time out.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got []byte
	for !bytes.Contains(got, []byte("hello nexus3")) {
		f, err := r.ReadFrame()
		if err != nil {
			break
		}
		if f.Type == wire.FrameData {
			got = append(got, f.Data.Payload...)
		}
	}
	_ = conn.SetReadDeadline(time.Time{})

	if !bytes.Contains(got, []byte("hello nexus3")) {
		t.Fatalf("expected stdin echo, got %q", got)
	}
}

// Test: Signal terminates the session → Exit frame delivered

func TestSignalTerminates(t *testing.T) {
	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()

	_, err := client.Exec(ctx, &agentpb.ExecRequest{
		SessionId: "s-signal",
		Argv:      []string{"sleep", "100"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	conn, w, r := dialData(t, dataLis)
	if err := w.WriteHandshake(wire.Handshake{SessionID: "s-signal", ResumeFromOffset: 0}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: type=%v err=%v", ack.Type, err)
	}

	// Give the agent a moment to start the listener goroutines.
	time.Sleep(50 * time.Millisecond)

	// Send SIGKILL via the control plane.
	_, err = client.Signal(ctx, &agentpb.SignalRequest{
		SessionId: "s-signal",
		Signum:    int32(syscall.SIGKILL),
	})
	if err != nil {
		t.Fatalf("Signal: %v", err)
	}

	// Expect Exit frame on the data plane.
	frames := collectFrames(t, conn, r, 5*time.Second)
	if gotExit, _ := hasExitFrame(frames); !gotExit {
		t.Fatal("expected Exit frame after SIGKILL, not received")
	}
}

// Test: SessionStatus and ListSessions

func TestSessionStatus(t *testing.T) {
	client, _, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()

	_, err := client.Exec(ctx, &agentpb.ExecRequest{
		SessionId: "s-status",
		Argv:      []string{"sleep", "100"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// SessionStatus should report Running.
	resp, err := client.SessionStatus(ctx, &agentpb.SessionStatusRequest{SessionId: "s-status"})
	if err != nil {
		t.Fatalf("SessionStatus: %v", err)
	}
	if resp.Info.State != agentpb.SessionState_SESSION_STATE_RUNNING {
		t.Fatalf("expected RUNNING, got %v", resp.Info.State)
	}

	// ListSessions should include it.
	list, err := client.ListSessions(ctx, &agentpb.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, info := range list.Sessions {
		if info.SessionId == "s-status" {
			found = true
		}
	}
	if !found {
		t.Fatalf("session 's-status' not in ListSessions result: %v",
			sessionIDs(list.Sessions))
	}

	// Kill it.
	_, err = client.Signal(ctx, &agentpb.SignalRequest{
		SessionId: "s-status",
		Signum:    int32(syscall.SIGKILL),
	})
	if err != nil {
		t.Fatalf("Signal: %v", err)
	}

	// Poll until Exited (up to 3 seconds).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = client.SessionStatus(ctx, &agentpb.SessionStatusRequest{SessionId: "s-status"})
		if err != nil {
			t.Fatalf("SessionStatus after kill: %v", err)
		}
		if resp.Info.State == agentpb.SessionState_SESSION_STATE_EXITED {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if resp.Info.State != agentpb.SessionState_SESSION_STATE_EXITED {
		t.Fatalf("expected EXITED after SIGKILL, got %v", resp.Info.State)
	}
}

func sessionIDs(infos []*agentpb.SessionInfo) []string {
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.SessionId
	}
	return ids
}

// Test: FrameStdinClose causes an EOF-reading process to complete

// TestStdinCloseTerminatesReader reproduces the bug: a guest process that
// reads stdin to EOF hangs forever without the fix because the host never
// signals end-of-stdin.  With FrameStdinClose the guest closes the process
// stdin pipe on receipt and the process exits normally.
func TestStdinCloseTerminatesReader(t *testing.T) {
	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()

	// "cat" reads stdin to EOF and exits — exactly the class of command
	// that hung before the fix.
	_, err := client.Exec(ctx, &agentpb.ExecRequest{
		SessionId: "s-stdinclose",
		Argv:      []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	conn, w, r := dialData(t, dataLis)

	if err := w.WriteHandshake(wire.Handshake{SessionID: "s-stdinclose", ResumeFromOffset: 0}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: got type=%v err=%v", ack.Type, err)
	}
	if ack.HandshakeAck.Status != wire.AckAlive {
		t.Fatalf("expected AckAlive, got %v", ack.HandshakeAck.Status)
	}

	// Send a chunk of data followed by the stdin-close signal.
	payload := []byte("hello stdin-close\n")
	if err := w.WriteData(wire.StreamStdin, payload); err != nil {
		t.Fatalf("WriteData stdin: %v", err)
	}
	// Signal EOF — this is the fix.  Without it cat blocks forever.
	if err := w.WriteStdinClose(); err != nil {
		t.Fatalf("WriteStdinClose: %v", err)
	}

	// cat should echo the input and then exit.  A 5-second deadline is
	// generous; in practice it completes in milliseconds.
	frames := collectFrames(t, conn, r, 5*time.Second)

	gotExit, code := hasExitFrame(frames)
	if !gotExit {
		t.Fatal("process did not exit after FrameStdinClose — stdin EOF was not delivered (bug)")
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if out := dataBytes(frames); !bytes.Contains(out, payload) {
		t.Fatalf("expected echoed payload in output, got %q", out)
	}
}

// Test: PTY exec – basic smoke

func TestExecPTY(t *testing.T) {
	client, dataLis, cancel := testHarness(t)
	defer cancel()

	ctx := context.Background()

	_, err := client.Exec(ctx, &agentpb.ExecRequest{
		SessionId: "s-pty",
		Argv:      []string{"sh", "-c", "echo pty-ok"},
		Pty: &agentpb.PtyOptions{
			Term:        "xterm-256color",
			InitialSize: &agentpb.WinSize{Rows: 24, Cols: 80},
		},
	})
	if err != nil {
		t.Fatalf("Exec (PTY): %v", err)
	}

	conn, w, r := dialData(t, dataLis)
	if err := w.WriteHandshake(wire.Handshake{SessionID: "s-pty", ResumeFromOffset: 0}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	ack, err := r.ReadFrame()
	if err != nil || ack.Type != wire.FrameHandshakeAck {
		t.Fatalf("expected HandshakeAck: type=%v err=%v", ack.Type, err)
	}

	frames := collectFrames(t, conn, r, 5*time.Second)
	out := string(dataBytes(frames))
	if !strings.Contains(out, "pty-ok") {
		t.Fatalf("expected 'pty-ok' in PTY output, got %q", out)
	}
	if gotExit, _ := hasExitFrame(frames); !gotExit {
		t.Fatal("expected Exit frame, not received")
	}
}
