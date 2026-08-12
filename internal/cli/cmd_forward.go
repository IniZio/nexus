package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

func init() {
	Register(Command{
		Name:    "forward",
		Summary: "Forward a host TCP port to a guest TCP port via vsock (runs until Ctrl-C)",
		Run:     runForward,
	})
}

// runForward is the registered Run function for
// "nexus3 forward <ref> <hostPort>:<guestPort>".
// It listens on 127.0.0.1:<hostPort> and for each accepted connection dials
// the guest-side port-forward multiplexer, asking it to splice to the
// guest-local TCP service on <guestPort>.  Runs until the context is cancelled
// (SIGINT / SIGTERM).
func runForward(ctx context.Context, args []string, _ *Output) error {
	if len(args) != 2 {
		return &UsageError{Msg: "forward: usage: forward <ref> <hostPort>:<guestPort>"}
	}
	ref := args[0]

	hostPort, guestPort, err := parsePortPair(args[1])
	if err != nil {
		return &UsageError{Msg: "forward: " + err.Error()}
	}

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "forward: " + err.Error(), Err: err}
	}

	lis, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", hostPort))
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("forward: listen 127.0.0.1:%d: %v", hostPort, err),
			Err:  err,
		}
	}
	defer lis.Close()

	fmt.Fprintf(os.Stderr, "forwarding 127.0.0.1:%d -> %s:%d (Ctrl-C to stop)\n",
		hostPort, ref, guestPort)

	// Close the listener when the context is cancelled so the accept loop exits.
	go func() {
		<-ctx.Done()
		lis.Close()
	}()

	for {
		hostConn, err := lis.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return &CodedError{
					Code: ErrCodeInternalError,
					Msg:  "forward: accept: " + err.Error(),
					Err:  err,
				}
			}
		}
		go forwardConn(ctx, hostConn, ref, guestPort, svc)
	}
}

// forwardConn dials the guest port-forward mux and splices hostConn to the
// guest-local TCP port.  Runs in its own goroutine per accepted connection.
func forwardConn(ctx context.Context, hostConn net.Conn, ref string, guestPort uint32, svc interface {
	DialGuestPortForward(context.Context, string, uint32) (net.Conn, error)
}) {
	defer hostConn.Close()

	guestConn, err := svc.DialGuestPortForward(ctx, ref, guestPort)
	if err != nil {
		// Dial errors are transient (guest not running, port not open); log and drop.
		fmt.Fprintf(os.Stderr, "forward: dial guest :%d: %v\n", guestPort, err)
		return
	}
	defer guestConn.Close()

	done := make(chan struct{}, 2)
	splice := func(dst, src net.Conn) {
		io.Copy(dst, src) //nolint:errcheck
		// Half-close so the other direction sees EOF rather than a reset.
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite() //nolint:errcheck
		}
		done <- struct{}{}
	}

	go splice(hostConn, guestConn)
	go splice(guestConn, hostConn)

	<-done
}

// parsePortPair parses a "<hostPort>:<guestPort>" string into two validated
// port numbers.  Both ports must be in the range [1, 65535].
func parsePortPair(s string) (hostPort, guestPort uint32, err error) {
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return 0, 0, fmt.Errorf("invalid port pair %q: expected <hostPort>:<guestPort>", s)
	}
	h, err := parsePort(s[:idx])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid host port: %w", err)
	}
	g, err := parsePort(s[idx+1:])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid guest port: %w", err)
	}
	return h, g, nil
}

// parsePort converts a decimal port string to a uint32 in [1, 65535].
func parsePort(s string) (uint32, error) {
	if s == "" {
		return 0, fmt.Errorf("port must not be empty")
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("port %q is not a valid integer", s)
	}
	if n == 0 || n > 65535 {
		return 0, fmt.Errorf("port %d out of range [1, 65535]", n)
	}
	return uint32(n), nil
}
