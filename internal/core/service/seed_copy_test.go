package service

// TestNewAgentCopySeeder_RawBytesNotTar and TestNewAgentCACopySeeder_RawBytesNotTar
// guard against a regression where the seeders tar-wrapped single-file payloads
// before handing them to agent.Copy. With IsDirectory=false, the guest's pushFile
// writes whatever bytes arrive verbatim, so a tar wrapper would corrupt the
// written file. These tests confirm the seeder passes the raw input bytes through
// without any archive wrapping.

import (
	"archive/tar"
	"bytes"
	"context"
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

// copyTestDialer implements driver.GuestDialer for the seed copy tests.
// The control plane is a bufconn; the data plane is a single net.Pipe fed
// through a channel so each dial gets the pre-created host-side connection.
type copyTestDialer struct {
	lis      *bufconn.Listener
	dataCh   chan net.Conn // host-side ends of net.Pipe, one per expected Copy call
}

func newCopyTestDialer() *copyTestDialer {
	return &copyTestDialer{
		lis:    bufconn.Listen(1 << 20),
		dataCh: make(chan net.Conn, 4),
	}
}

func (d *copyTestDialer) DialGuest(ctx context.Context, _ domain.SandboxID, port uint32) (net.Conn, error) {
	switch port {
	case driver.AgentControlPort:
		return d.lis.DialContext(ctx)
	case wire.DataPort:
		select {
		case conn := <-d.dataCh:
			return conn, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	default:
		return nil, nil
	}
}

// pushDataPipe enqueues the host-side end of a net.Pipe for the next data dial
// and returns the guest-side end for the test goroutine to drive.
func (d *copyTestDialer) pushDataPipe() net.Conn {
	host, guest := net.Pipe()
	d.dataCh <- host
	return guest
}

// copyCapServer is a minimal AgentServiceServer whose Copy handler returns a
// fixed transfer ID, letting the test control the data-plane handshake.
// lastReq captures the most recent CopyRequest for call-site assertions.
type copyCapServer struct {
	agentpb.UnimplementedAgentServiceServer
	transferID string
	lastReq    *agentpb.CopyRequest
}

func (s *copyCapServer) Copy(_ context.Context, req *agentpb.CopyRequest) (*agentpb.CopyResponse, error) {
	s.lastReq = req
	return &agentpb.CopyResponse{TransferId: s.transferID}, nil
}

// runGuestCapture acts as the "guest" side of a PUSH transfer: it reads the
// wire Handshake, sends a HandshakeAck, drains all Data frames into buf, and
// signals done when an Exit frame arrives (or on any error).
func runGuestCapture(guestConn net.Conn, buf *bytes.Buffer, done chan<- struct{}) {
	go func() {
		defer close(done)
		defer guestConn.Close()
		rd := wire.NewReader(guestConn)
		wr := wire.NewWriter(guestConn)

		// Read handshake frame.
		if _, err := rd.ReadFrame(); err != nil {
			return
		}
		// Acknowledge.
		if err := wr.WriteHandshakeAck(wire.HandshakeAck{Status: wire.AckAlive}); err != nil {
			return
		}
		// Drain data frames until Exit.
		for {
			f, err := rd.ReadFrame()
			if err != nil {
				return
			}
			switch f.Type {
			case wire.FrameData:
				buf.Write(f.Data.Payload)
			case wire.FrameExit:
				return
			}
		}
	}()
}

// startCopyMockServer registers srv on a new gRPC server backed by the
// dialer's control listener and stops it when the test ends.
func startCopyMockServer(t *testing.T, lis *bufconn.Listener, srv agentpb.AgentServiceServer) {
	t.Helper()
	gs := grpc.NewServer()
	agentpb.RegisterAgentServiceServer(gs, srv)
	go func() { gs.Serve(lis) }() //nolint:errcheck
	t.Cleanup(func() { gs.GracefulStop() })
}

// assertRawNotTar fails the test if got does not equal want or if got is a
// valid tar archive (indicating the seeder erroneously wrapped the payload).
func assertRawNotTar(t *testing.T, label string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Errorf("%s: received %d bytes, want %d\ngot:  %q\nwant: %q",
			label, len(got), len(want), got, want)
	}
	if _, err := tar.NewReader(bytes.NewReader(got)).Next(); err == nil {
		t.Errorf("%s: payload is a valid tar archive; seeder must send raw bytes", label)
	}
}

// TestNewAgentCopySeeder_RawBytesNotTar asserts that NewAgentCopySeeder delivers
// the exact raw payload bytes through agent.Copy without any tar wrapping.
func TestNewAgentCopySeeder_RawBytesNotTar(t *testing.T) {
	t.Parallel()

	input := []byte("CLAUDE_CODE_OAUTH_TOKEN=placeholder-abc\nNODE_EXTRA_CA_CERTS=/path\n")

	td := newCopyTestDialer()
	startCopyMockServer(t, td.lis, &copyCapServer{transferID: "xfer-1"})

	var received bytes.Buffer
	done := make(chan struct{})
	guestConn := td.pushDataPipe()
	runGuestCapture(guestConn, &received, done)

	c := agent.NewClient(td, seedTestID(0xd0))
	seeder := NewAgentCopySeeder(c)
	if err := seeder(context.Background(), seedTestID(0xd0), input); err != nil {
		t.Fatalf("NewAgentCopySeeder: %v", err)
	}
	<-done

	assertRawNotTar(t, "NewAgentCopySeeder", received.Bytes(), input)
}

// TestNewAgentCACopySeeder_RawBytesNotTar asserts that NewAgentCACopySeeder
// delivers the exact raw PEM bytes through agent.Copy without any tar wrapping.
func TestNewAgentCACopySeeder_RawBytesNotTar(t *testing.T) {
	t.Parallel()

	// Minimal PEM block — enough to confirm the seeder doesn't corrupt it.
	input := []byte("-----BEGIN CERTIFICATE-----\nZmFrZWNlcnQ=\n-----END CERTIFICATE-----\n")

	td := newCopyTestDialer()
	startCopyMockServer(t, td.lis, &copyCapServer{transferID: "xfer-2"})

	var received bytes.Buffer
	done := make(chan struct{})
	guestConn := td.pushDataPipe()
	runGuestCapture(guestConn, &received, done)

	c := agent.NewClient(td, seedTestID(0xd1))
	seeder := NewAgentCACopySeeder(c)
	if err := seeder(context.Background(), seedTestID(0xd1), input); err != nil {
		t.Fatalf("NewAgentCACopySeeder: %v", err)
	}
	<-done

	assertRawNotTar(t, "NewAgentCACopySeeder", received.Bytes(), input)
}

// TestNewAgentCopySeeder_SetsExpectedBytes is the call-site mutation proof for
// Defect 2: it asserts that NewAgentCopySeeder sets ExpectedBytes == len(payload)
// on the CopyRequest it sends to the guest.
//
// Without this wiring the guard in pushFile is dead code: expectedBytes == 0
// causes pushFile to immediately return an error (fail-closed), so any transfer
// that reaches pushFile with a zero expectedBytes is already caught. But the
// proof here is at the call-site level: removing `ExpectedBytes: int64(len(payload))`
// from NewAgentCopySeeder makes this test fail because lastReq.ExpectedBytes == 0
// while we assert it equals len(payload).
func TestNewAgentCopySeeder_SetsExpectedBytes(t *testing.T) {
	t.Parallel()

	payload := []byte("CLAUDE_CODE_OAUTH_TOKEN=placeholder-abc\nNODE_EXTRA_CA_CERTS=/path\n")

	srv := &copyCapServer{transferID: "xfer-eb1"}
	td := newCopyTestDialer()
	startCopyMockServer(t, td.lis, srv)

	var received bytes.Buffer
	done := make(chan struct{})
	guestConn := td.pushDataPipe()
	runGuestCapture(guestConn, &received, done)

	c := agent.NewClient(td, seedTestID(0xd2))
	seeder := NewAgentCopySeeder(c)
	if err := seeder(context.Background(), seedTestID(0xd2), payload); err != nil {
		t.Fatalf("NewAgentCopySeeder: %v", err)
	}
	<-done

	if srv.lastReq == nil {
		t.Fatal("Copy RPC was never called")
	}
	if srv.lastReq.ExpectedBytes == nil {
		t.Fatal("ExpectedBytes is nil — host must declare authoritative size for single-file PUSH (even for empty files)")
	}
	want := int64(len(payload))
	if *srv.lastReq.ExpectedBytes != want {
		t.Fatalf("*ExpectedBytes = %d, want %d — wiring removed at call site leaves guard dead",
			*srv.lastReq.ExpectedBytes, want)
	}
}

// TestNewAgentCACopySeeder_SetsExpectedBytes is the call-site mutation proof for
// NewAgentCACopySeeder: removing ExpectedBytes wiring makes this test fail.
func TestNewAgentCACopySeeder_SetsExpectedBytes(t *testing.T) {
	t.Parallel()

	payload := []byte("-----BEGIN CERTIFICATE-----\nZmFrZWNlcnQ=\n-----END CERTIFICATE-----\n")

	srv := &copyCapServer{transferID: "xfer-eb2"}
	td := newCopyTestDialer()
	startCopyMockServer(t, td.lis, srv)

	var received bytes.Buffer
	done := make(chan struct{})
	guestConn := td.pushDataPipe()
	runGuestCapture(guestConn, &received, done)

	c := agent.NewClient(td, seedTestID(0xd3))
	seeder := NewAgentCACopySeeder(c)
	if err := seeder(context.Background(), seedTestID(0xd3), payload); err != nil {
		t.Fatalf("NewAgentCACopySeeder: %v", err)
	}
	<-done

	if srv.lastReq == nil {
		t.Fatal("Copy RPC was never called")
	}
	if srv.lastReq.ExpectedBytes == nil {
		t.Fatal("ExpectedBytes is nil — host must declare authoritative size for single-file PUSH (even for empty files)")
	}
	want := int64(len(payload))
	if *srv.lastReq.ExpectedBytes != want {
		t.Fatalf("*ExpectedBytes = %d, want %d — wiring removed at call site leaves guard dead",
			*srv.lastReq.ExpectedBytes, want)
	}
}

// TestNewAgentCopySeeder_EmptyPayload_SetsExpectedBytesZero proves that an
// empty payload (len=0) still sets ExpectedBytes to a non-nil pointer with value
// 0. Before the optional-field fix, &0 and nil were collapsed and empty payloads
// caused a false-positive failure in pushFile.
func TestNewAgentCopySeeder_EmptyPayload_SetsExpectedBytesZero(t *testing.T) {
	t.Parallel()

	// Empty payload — legitimate use case (e.g. zero-byte credential file).
	payload := []byte{}

	srv := &copyCapServer{transferID: "xfer-eb3"}
	td := newCopyTestDialer()
	startCopyMockServer(t, td.lis, srv)

	var received bytes.Buffer
	done := make(chan struct{})
	guestConn := td.pushDataPipe()
	runGuestCapture(guestConn, &received, done)

	c := agent.NewClient(td, seedTestID(0xd4))
	seeder := NewAgentCopySeeder(c)
	if err := seeder(context.Background(), seedTestID(0xd4), payload); err != nil {
		t.Fatalf("NewAgentCopySeeder (empty payload): %v", err)
	}
	<-done

	if srv.lastReq == nil {
		t.Fatal("Copy RPC was never called")
	}
	if srv.lastReq.ExpectedBytes == nil {
		t.Fatal("ExpectedBytes is nil for empty payload — must be &0, not nil (nil = undeclared = fail closed)")
	}
	if *srv.lastReq.ExpectedBytes != 0 {
		t.Fatalf("*ExpectedBytes = %d, want 0 for empty payload", *srv.lastReq.ExpectedBytes)
	}
}
