package main

import (
	"context"
	"net"
	"os"

	"github.com/mdlayher/vsock"
)

// sshdVsockPort is the vsock port the agent listens on to bridge incoming
// host connections to the guest sshd running on localhost:22.
const sshdVsockPort uint32 = 22

// startSSHForward binds vsock port 22 and, for each incoming connection,
// dials the guest-local sshd on 127.0.0.1:22 and splices the two streams.
// It runs until ctx is cancelled. All diagnostic messages go to con (the
// guest console); the function never writes to stdout.
func startSSHForward(ctx context.Context, con *os.File) {
	lis, err := vsock.Listen(sshdVsockPort, nil)
	if err != nil {
		consoleLog(con, "nexus-agent: sshd-forward: vsock.Listen %d: %v\n", sshdVsockPort, err)
		return
	}

	go func() {
		<-ctx.Done()
		lis.Close()
	}()

	consoleLog(con, "nexus-agent: sshd-forward: listening on vsock port %d\n", sshdVsockPort)

	for {
		vsockConn, err := lis.Accept()
		if err != nil {
			// Listener was closed on ctx cancellation; exit cleanly.
			select {
			case <-ctx.Done():
				return
			default:
			}
			consoleLog(con, "nexus-agent: sshd-forward: accept: %v\n", err)
			return
		}
		go handleSSHForward(con, vsockConn)
	}
}

// handleSSHForward dials the guest-local sshd and splices vsockConn ↔ sshd.
func handleSSHForward(con *os.File, vsockConn net.Conn) {
	defer vsockConn.Close()

	sshdConn, err := net.Dial("tcp", "127.0.0.1:22")
	if err != nil {
		consoleLog(con, "nexus-agent: sshd-forward: dial sshd: %v\n", err)
		return
	}
	defer sshdConn.Close()

	done := make(chan struct{}, 2)
	splice := func(dst, src net.Conn) {
		spliceHalfClose(con, "sshd-forward", dst, src)
		done <- struct{}{}
	}

	go splice(sshdConn, vsockConn)
	go splice(vsockConn, sshdConn)

	<-done
}
