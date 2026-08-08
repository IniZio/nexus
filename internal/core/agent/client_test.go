package agent_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// testDialer is an in-process GuestDialer. The control port (1024) is backed
// by a bufconn.Listener so gRPC can make multiple dials. The data port (1025)
// is backed by a channel of net.Pipe host-side ends pre-pushed by the test.
type testDialer struct {
	controlLis *bufconn.Listener
	dataConns  chan net.Conn // push host-side conn before each Client call
}

func newTestDialer() *testDialer {
	return &testDialer{
		controlLis: bufconn.Listen(1 << 20), // 1 MiB in-memory buffer
		dataConns:  make(chan net.Conn, 8),
	}
}

// DialGuest implements driver.GuestDialer.
func (d *testDialer) DialGuest(ctx context.Context, _ domain.SandboxID, port uint32) (net.Conn, error) {
	switch port {
	case driver.AgentControlPort:
		return d.controlLis.DialContext(ctx)
	case wire.DataPort:
		select {
		case conn := <-d.dataConns:
			return conn, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	default:
		return nil, fmt.Errorf("testDialer: unknown port %d", port)
	}
}

// pushDataPipe creates a net.Pipe, enqueues the host-side end for the next
// data DialGuest call, and returns the guest-side end.
func (d *testDialer) pushDataPipe() net.Conn {
	hostConn, guestConn := net.Pipe()
	d.dataConns <- hostConn
	return guestConn
}

// startGRPCServer registers svc on a new grpc.Server backed by the dialer's
// control listener. It stops the server when the test ends.
func startGRPCServer(t *testing.T, lis *bufconn.Listener, svc agentpb.AgentServiceServer) {
	t.Helper()
	srv := grpc.NewServer()
	agentpb.RegisterAgentServiceServer(srv, svc)
	go func() {
		if err := srv.Serve(lis); err != nil {
			// ErrServerStopped is expected during t.Cleanup.
			t.Logf("grpc server exited: %v", err)
		}
	}()
	t.Cleanup(func() { srv.GracefulStop() })
}

// testAgentServer is a minimal AgentServiceServer for testing. Methods that
// are not set fall back to the embedded Unimplemented stub.
type testAgentServer struct {
	agentpb.UnimplementedAgentServiceServer
	execFn func(*agentpb.ExecRequest) (*agentpb.ExecResponse, error)
	copyFn func(*agentpb.CopyRequest) (*agentpb.CopyResponse, error)
}

func (s *testAgentServer) Exec(_ context.Context, req *agentpb.ExecRequest) (*agentpb.ExecResponse, error) {
	if s.execFn != nil {
		return s.execFn(req)
	}
	return &agentpb.ExecResponse{Pid: 1234}, nil
}

func (s *testAgentServer) Copy(_ context.Context, req *agentpb.CopyRequest) (*agentpb.CopyResponse, error) {
	if s.copyFn != nil {
		return s.copyFn(req)
	}
	return &agentpb.CopyResponse{TransferId: "test-transfer-id"}, nil
}

// runGuestDataServer starts a goroutine that acts as the guest data-plane side.
// It reads the Handshake, calls fn(sessionID, resumeFromOffset) which populates
// the connection, then closes the guest-side conn.
func runGuestDataServer(guestConn net.Conn, fn func(sessionID string, offset uint64, rd *wire.Reader, wr *wire.Writer)) {
	go func() {
		defer guestConn.Close()
		rd := wire.NewReader(guestConn)
		wr := wire.NewWriter(guestConn)

		frame, err := rd.ReadFrame()
		if err != nil || frame.Handshake == nil {
			return
		}
		fn(frame.Handshake.SessionID, frame.Handshake.ResumeFromOffset, rd, wr)
	}()
}

// TestExec_StreamsStdoutAndReturnsExitCode verifies that Exec:
//   - sends the Exec RPC with the minted session_id
//   - opens the data plane and performs the wire handshake
//   - forwards guest stdout Data frames to the provided writer
//   - returns the exit code from the guest Exit frame
func TestExec_StreamsStdoutAndReturnsExitCode(t *testing.T) {
	const (
		wantOutput = "hello from guest\n"
		wantExit   = int32(42)
	)

	td := newTestDialer()
	startGRPCServer(t, td.controlLis, &testAgentServer{})

	guestConn := td.pushDataPipe()
	runGuestDataServer(guestConn, func(_ string, _ uint64, _ *wire.Reader, wr *wire.Writer) {
		wr.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive}) //nolint:errcheck
		wr.WriteData(wire.StreamStdout, []byte(wantOutput))            //nolint:errcheck
		wr.WriteExit(wire.Exit{Code: wantExit})                        //nolint:errcheck
	})

	id := domain.NewSandboxID()
	c := agent.NewClient(td, id)

	var stdout bytes.Buffer
	code, err := c.Exec(context.Background(), agent.ExecOptions{
		Argv:   []string{"/bin/echo", "hello"},
		Stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != wantExit {
		t.Errorf("exit code = %d, want %d", code, wantExit)
	}
	if stdout.String() != wantOutput {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantOutput)
	}
}

// TestExec_ForwardsStdinToGuest verifies that Exec forwards bytes read from
// the Stdin reader to the guest as Data(Stdin) frames.
func TestExec_ForwardsStdinToGuest(t *testing.T) {
	const stdinPayload = "type this"

	td := newTestDialer()
	startGRPCServer(t, td.controlLis, &testAgentServer{})

	receivedCh := make(chan []byte, 1)
	guestConn := td.pushDataPipe()
	runGuestDataServer(guestConn, func(_ string, _ uint64, rd *wire.Reader, wr *wire.Writer) {
		wr.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive}) //nolint:errcheck
		// Read one stdin frame.
		frame, err := rd.ReadFrame()
		if err == nil && frame.Data != nil && frame.Data.Tag == wire.StreamStdin {
			receivedCh <- frame.Data.Payload
		} else {
			close(receivedCh)
		}
		wr.WriteExit(wire.Exit{Code: 0}) //nolint:errcheck
	})

	id := domain.NewSandboxID()
	c := agent.NewClient(td, id)

	_, err := c.Exec(context.Background(), agent.ExecOptions{
		Argv:  []string{"/bin/cat"},
		Stdin: bytes.NewBufferString(stdinPayload),
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	got, ok := <-receivedCh
	if !ok {
		t.Fatal("guest never received a stdin Data frame")
	}
	if string(got) != stdinPayload {
		t.Errorf("stdin payload = %q, want %q", string(got), stdinPayload)
	}
}

// TestAttach_ReplaysFromOffset verifies that Attach sends the correct
// ResumeFromOffset in the Handshake and receives only the replayed portion.
func TestAttach_ReplaysFromOffset(t *testing.T) {
	const fullOutput = "ABCDEFGHIJ"
	const resumeOffset = uint64(5)
	wantOutput := fullOutput[resumeOffset:]
	const wantExit = int32(0)

	td := newTestDialer()
	startGRPCServer(t, td.controlLis, &testAgentServer{})

	guestConn := td.pushDataPipe()
	runGuestDataServer(guestConn, func(_ string, offset uint64, _ *wire.Reader, wr *wire.Writer) {
		// Simulate the guest replaying only bytes from offset onward.
		replay := fullOutput[offset:]
		wr.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive}) //nolint:errcheck
		wr.WriteData(wire.StreamStdout, []byte(replay))                //nolint:errcheck
		wr.WriteExit(wire.Exit{Code: wantExit})                        //nolint:errcheck
	})

	id := domain.NewSandboxID()
	c := agent.NewClient(td, id)

	var stdout bytes.Buffer
	code, err := c.Attach(context.Background(), agent.AttachOptions{
		SessionID:        "existing-session",
		ResumeFromOffset: resumeOffset,
		Stdout:           &stdout,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if code != wantExit {
		t.Errorf("exit code = %d, want %d", code, wantExit)
	}
	if stdout.String() != wantOutput {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantOutput)
	}
}

// TestAttach_AckExited verifies that Attach handles a HandshakeAck with
// AckExited: the guest replays ring bytes and sends an Exit frame.
func TestAttach_AckExited(t *testing.T) {
	const ringBytes = "previous output"
	const exitCode = int32(1)

	td := newTestDialer()
	startGRPCServer(t, td.controlLis, &testAgentServer{})

	guestConn := td.pushDataPipe()
	runGuestDataServer(guestConn, func(_ string, _ uint64, _ *wire.Reader, wr *wire.Writer) {
		// The process already exited; guest replays ring then sends Exit.
		wr.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckExited, ExitCode: exitCode}) //nolint:errcheck
		wr.WriteData(wire.StreamStdout, []byte(ringBytes))                                  //nolint:errcheck
		wr.WriteExit(wire.Exit{Code: exitCode})                                             //nolint:errcheck
	})

	id := domain.NewSandboxID()
	c := agent.NewClient(td, id)

	var stdout bytes.Buffer
	code, err := c.Attach(context.Background(), agent.AttachOptions{
		SessionID: "done-session",
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if code != exitCode {
		t.Errorf("exit code = %d, want %d", code, exitCode)
	}
	if stdout.String() != ringBytes {
		t.Errorf("stdout = %q, want %q", stdout.String(), ringBytes)
	}
}

// TestCopy_PullRoundTripsArchive verifies that Copy(PULL) receives Data frames
// from the guest and writes them to Dst, returning on the Exit frame.
func TestCopy_PullRoundTripsArchive(t *testing.T) {
	archiveData := []byte("fake-tar-archive-bytes-here")
	transferID := "transfer-abc123"

	td := newTestDialer()
	startGRPCServer(t, td.controlLis, &testAgentServer{
		copyFn: func(_ *agentpb.CopyRequest) (*agentpb.CopyResponse, error) {
			return &agentpb.CopyResponse{TransferId: transferID}, nil
		},
	})

	guestConn := td.pushDataPipe()
	runGuestDataServer(guestConn, func(sid string, _ uint64, _ *wire.Reader, wr *wire.Writer) {
		// Verify the guest sees the correct transfer_id as the session ID.
		if sid != transferID {
			return // test will fail on empty dst
		}
		wr.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive}) //nolint:errcheck
		wr.WriteData(wire.StreamStdout, archiveData)                   //nolint:errcheck
		wr.WriteExit(wire.Exit{Code: 0})                               //nolint:errcheck
	})

	id := domain.NewSandboxID()
	c := agent.NewClient(td, id)

	var dst bytes.Buffer
	err := c.Copy(context.Background(), agent.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PULL,
		GuestPath: "/workspace/output",
		Dst:       &dst,
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if !bytes.Equal(dst.Bytes(), archiveData) {
		t.Errorf("archive = %q, want %q", dst.Bytes(), archiveData)
	}
}

// TestCopy_PushSendsArchiveToGuest verifies that Copy(PUSH) sends the src
// bytes as Data frames and signals completion with an Exit frame.
func TestCopy_PushSendsArchiveToGuest(t *testing.T) {
	archiveData := []byte("archive-to-push")
	transferID := "transfer-push-xyz"

	td := newTestDialer()
	startGRPCServer(t, td.controlLis, &testAgentServer{
		copyFn: func(_ *agentpb.CopyRequest) (*agentpb.CopyResponse, error) {
			return &agentpb.CopyResponse{TransferId: transferID}, nil
		},
	})

	receivedCh := make(chan []byte, 1)
	guestConn := td.pushDataPipe()
	runGuestDataServer(guestConn, func(_ string, _ uint64, rd *wire.Reader, wr *wire.Writer) {
		wr.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive}) //nolint:errcheck
		// Collect all Data frames until Exit.
		var buf []byte
		for {
			frame, err := rd.ReadFrame()
			if err != nil {
				break
			}
			if frame.Type == wire.FrameData {
				buf = append(buf, frame.Data.Payload...)
			}
			if frame.Type == wire.FrameExit {
				break
			}
		}
		receivedCh <- buf
	})

	id := domain.NewSandboxID()
	c := agent.NewClient(td, id)

	err := c.Copy(context.Background(), agent.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		GuestPath: "/workspace/input",
		Src:       bytes.NewReader(archiveData),
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}

	received := <-receivedCh
	if !bytes.Equal(received, archiveData) {
		t.Errorf("received = %q, want %q", received, archiveData)
	}
}
