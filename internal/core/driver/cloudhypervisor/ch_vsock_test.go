package cloudhypervisor

// ch_vsock_test.go contains unit tests for the CH vsock multiplexer handshake.
//
// TestDialGuest_HandshakeSuccess and TestDialGuest_HandshakeError run without
// KVM or a real CH binary — they stand up a throwaway AF_UNIX listener that
// mimics CH's multiplexer protocol.
//
// TestDialGuest_Integration (build tag: integration) is KVM-gated; it skips
// cleanly when /dev/kvm or boot artifacts are absent. The test only exercises
// the handshake against a real booted VM — the guest agent is a later run.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// fakeMultiplexer starts a throwaway AF_UNIX listener that mimics CH's vsock
// multiplexer for a single connection. It reads the "CONNECT <port>\n" line,
// calls replyFn(port) to obtain the reply line (e.g. "OK 0"), writes the
// reply, then runs onConnected(conn) for the stream phase.
//
// The listener is cleaned up when tb.Cleanup runs.
func fakeMultiplexer(
	tb testing.TB,
	socketPath string,
	replyFn func(port uint32) string,
	onConnected func(conn net.Conn),
) {
	tb.Helper()
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		tb.Fatalf("fakeMultiplexer: listen: %v", err)
	}
	tb.Cleanup(func() {
		l.Close()
		os.Remove(socketPath)
	})

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return // listener was closed
		}
		defer conn.Close()

		// Read the CONNECT line.
		br := bufio.NewReader(conn)
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		// Parse "CONNECT <port>".
		var port uint32
		if _, err := fmt.Sscanf(line, "CONNECT %d", &port); err != nil {
			fmt.Fprintf(conn, "NACK bad handshake line: %q\n", line)
			return
		}

		reply := replyFn(port)
		fmt.Fprintf(conn, "%s\n", reply)

		if strings.HasPrefix(reply, "OK") && onConnected != nil {
			onConnected(conn)
		}
	}()
}

// TestDialGuest_HandshakeSuccess verifies that DialGuest:
//   - sends "CONNECT <AgentControlPort>\n"
//   - returns a working net.Conn on "OK" reply
//   - the returned conn round-trips bytes in both directions
func TestDialGuest_HandshakeSuccess(t *testing.T) {
	dir := testSocketDir(t)
	id := domain.NewSandboxID()

	drv := &CHDriver{
		cfg: Config{SocketDir: dir},
	}

	echoDone := make(chan struct{})
	fakeMultiplexer(t, drv.vsockPath(id),
		func(port uint32) string {
			if port != driver.AgentControlPort {
				return fmt.Sprintf("NACK wrong port %d (want %d)", port, driver.AgentControlPort)
			}
			return "OK 0"
		},
		func(conn net.Conn) {
			defer close(echoDone)
			// Echo server: read one message and write it back.
			buf := make([]byte, 64)
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			conn.Write(buf[:n])
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hostConn, err := drv.DialGuest(ctx, id, driver.AgentControlPort)
	if err != nil {
		t.Fatalf("DialGuest: %v", err)
	}
	defer hostConn.Close()

	// Send a message and expect it echoed back.
	msg := "hello-vsock"
	if _, err := io.WriteString(hostConn, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(hostConn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("echo mismatch: got %q, want %q", string(buf), msg)
	}

	<-echoDone
}

// TestDialGuest_HandshakeError verifies that DialGuest returns an error (and
// does not hand back a half-open conn) when the multiplexer replies with a
// non-OK line.
func TestDialGuest_HandshakeError(t *testing.T) {
	dir := testSocketDir(t)
	id := domain.NewSandboxID()

	drv := &CHDriver{
		cfg: Config{SocketDir: dir},
	}

	fakeMultiplexer(t, drv.vsockPath(id),
		func(_ uint32) string {
			return "NACK no listener on requested port"
		},
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := drv.DialGuest(ctx, id, driver.AgentControlPort)
	if err == nil {
		conn.Close()
		t.Fatal("expected error on NACK reply, got nil")
	}
	if conn != nil {
		conn.Close()
		t.Fatal("expected nil conn on NACK reply")
	}
	if !strings.Contains(err.Error(), "NACK") {
		t.Fatalf("error should mention NACK, got: %v", err)
	}
}

// TestDialGuest_CorrectPort verifies that DialGuest sends exactly
// "CONNECT <AgentControlPort>\n" — not some other port.
func TestDialGuest_CorrectPort(t *testing.T) {
	dir := testSocketDir(t)
	id := domain.NewSandboxID()

	drv := &CHDriver{
		cfg: Config{SocketDir: dir},
	}

	var dialedPort uint32
	portSeen := make(chan struct{})
	fakeMultiplexer(t, drv.vsockPath(id),
		func(port uint32) string {
			dialedPort = port
			close(portSeen)
			return "OK 0"
		},
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := drv.DialGuest(ctx, id, driver.AgentControlPort)
	if err != nil {
		t.Fatalf("DialGuest: %v", err)
	}
	conn.Close()

	select {
	case <-portSeen:
	case <-ctx.Done():
		t.Fatal("multiplexer never received CONNECT")
	}

	if dialedPort != driver.AgentControlPort {
		t.Fatalf("dialed port %d, want AgentControlPort (%d)", dialedPort, driver.AgentControlPort)
	}
}
