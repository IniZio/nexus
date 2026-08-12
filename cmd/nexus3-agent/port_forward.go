package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/mdlayher/vsock"
)

// portForwardMuxVsockPort is the vsock port the agent listens on to multiplex
// arbitrary host→guest TCP port forwards.  On each accepted connection the host
// sends a 4-byte big-endian uint32 (the guest-local TCP port to forward to);
// the agent then dials 127.0.0.1:<guestPort> and splices the two streams.
//
// Port 3001 is chosen to be well clear of the control plane (1024), data plane
// (1025), and sshd forward (22) while remaining below the ephemeral range.
// Must match service.PortForwardMuxVsockPort.
const portForwardMuxVsockPort uint32 = 3001

// startPortForwardMux binds vsock port 3001 and, for each incoming connection,
// reads a 4-byte guest TCP port number then splices the connection to that
// guest-local TCP port.  It runs until ctx is cancelled.
func startPortForwardMux(ctx context.Context, con *os.File) {
	lis, err := vsock.Listen(portForwardMuxVsockPort, nil)
	if err != nil {
		consoleLog(con, "nexus3-agent: port-forward-mux: vsock.Listen %d: %v\n", portForwardMuxVsockPort, err)
		return
	}

	go func() {
		<-ctx.Done()
		lis.Close()
	}()

	consoleLog(con, "nexus3-agent: port-forward-mux: listening on vsock port %d\n", portForwardMuxVsockPort)

	for {
		vsockConn, err := lis.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			consoleLog(con, "nexus3-agent: port-forward-mux: accept: %v\n", err)
			return
		}
		go handlePortForward(con, vsockConn)
	}
}

// handlePortForward reads the 4-byte guest TCP port, dials it, and splices
// vsockConn ↔ the local TCP connection.
func handlePortForward(con *os.File, vsockConn net.Conn) {
	defer vsockConn.Close()

	// Read the 4-byte big-endian guest TCP port number.
	var portBuf [4]byte
	if _, err := io.ReadFull(vsockConn, portBuf[:]); err != nil {
		consoleLog(con, "nexus3-agent: port-forward-mux: read port: %v\n", err)
		return
	}
	guestPort := binary.BigEndian.Uint32(portBuf[:])

	tcpConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", guestPort))
	if err != nil {
		consoleLog(con, "nexus3-agent: port-forward-mux: dial :%d: %v\n", guestPort, err)
		return
	}
	defer tcpConn.Close()

	done := make(chan struct{}, 2)
	splice := func(dst, src net.Conn) {
		io.Copy(dst, src) //nolint:errcheck
		// Half-close so the other direction sees EOF.
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite() //nolint:errcheck
		}
		done <- struct{}{}
	}

	go splice(tcpConn, vsockConn)
	go splice(vsockConn, tcpConn)

	<-done
}
