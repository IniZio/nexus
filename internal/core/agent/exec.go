package agent

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
)

// ExecOptions configures an [Client.Exec] call.
type ExecOptions struct {
	// SessionID, when non-empty, uses the given string as the guest session
	// identifier. When empty a cryptographically random ID is minted. Setting
	// it explicitly allows the caller to later reattach via [Client.Attach].
	SessionID string
	// Argv is the command and arguments to run in the guest.
	Argv []string
	// Env sets additional environment variables for the process.
	Env map[string]string
	// Cwd is the working directory for the process.
	Cwd string
	// Pty, when non-nil, requests a PTY for the session.
	Pty *agentpb.PtyOptions
	// Stdin is read and forwarded to the guest as Data(Stdin) frames.
	// May be nil.
	Stdin io.Reader
	// Stdout receives Data(Stdout) frames from the guest.
	// May be nil.
	Stdout io.Writer
	// Stderr receives Data(Stderr) frames from the guest.
	// May be nil.
	Stderr io.Writer
	// WinsizeCh delivers terminal resize events to the guest as Winsize
	// frames. May be nil.
	//
	// CONTRACT: when non-nil, the caller MUST close WinsizeCh after the
	// [Client.Exec] call returns to allow the winsize-forwarding goroutine
	// inside runDataPump to exit. The pump also provides a defensive exit
	// when the data connection closes (see runDataPump), but closing the
	// channel remains the caller's responsibility for a clean shutdown.
	WinsizeCh <-chan wire.Winsize
}

// Exec starts a process in the guest, opens the data plane, and pumps
// stdio until the process exits. It returns the exit code reported by the
// guest Exit frame.
func (c *Client) Exec(ctx context.Context, opts ExecOptions) (int32, error) {
	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = newSessionID()
	}

	// Dial the gRPC control plane and issue the Exec RPC.
	stub, cc, err := c.controlClient(ctx)
	if err != nil {
		return 0, err
	}
	defer cc.Close()

	if _, err := stub.Exec(ctx, &agentpb.ExecRequest{
		SessionId: sessionID,
		Argv:      opts.Argv,
		Env:       opts.Env,
		Cwd:       opts.Cwd,
		Pty:       opts.Pty,
	}); err != nil {
		return 0, fmt.Errorf("agent: exec rpc: %w", err)
	}

	return runDataPump(ctx, c, pumpOpts{
		sessionID:        sessionID,
		resumeFromOffset: 0,
		stdin:            opts.Stdin,
		stdout:           opts.Stdout,
		stderr:           opts.Stderr,
		winsizeCh:        opts.WinsizeCh,
	})
}

// pumpOpts configures the shared data-plane pump.
type pumpOpts struct {
	sessionID        string
	resumeFromOffset uint64
	stdin            io.Reader
	stdout           io.Writer
	stderr           io.Writer
	winsizeCh        <-chan wire.Winsize
}

// runDataPump opens a data-plane connection, performs the wire handshake,
// and pumps frames until the guest sends an Exit frame.
//
// wire.Writer is not concurrent-safe, so stdin forwarding and window-resize
// events are both serialised through wrMu. Closing dataConn unblocks the
// stdin reader goroutine; goroutines do not outlive the function return.
func runDataPump(ctx context.Context, c *Client, opts pumpOpts) (int32, error) {
	dataConn, err := c.dialData(ctx)
	if err != nil {
		return 0, err
	}
	// Closing the connection unblocks the stdin reader goroutine when the
	// pump exits (on Exit frame or error).
	defer dataConn.Close()

	wr := wire.NewWriter(dataConn)
	if err := wr.WriteHandshake(wire.Handshake{
		SessionID:        opts.sessionID,
		ResumeFromOffset: opts.resumeFromOffset,
	}); err != nil {
		return 0, fmt.Errorf("agent: pump: write handshake: %w", err)
	}

	rd := wire.NewReader(dataConn)
	ackFrame, err := rd.ReadFrame()
	if err != nil {
		return 0, fmt.Errorf("agent: pump: read handshake ack: %w", err)
	}
	if ackFrame.HandshakeAck == nil {
		return 0, fmt.Errorf("agent: pump: expected HandshakeAck, got frame type %d", ackFrame.Type)
	}
	// If AckExited, the guest may still replay ring bytes before the Exit
	// frame. The drain loop below handles both the alive and exited cases.

	var wrMu sync.Mutex // wire.Writer is not safe for concurrent use.

	// done is closed when runDataPump returns, providing a defensive exit
	// for any background goroutines that outlive the caller closing their
	// input channel (e.g. a winsizeCh that is never closed).
	done := make(chan struct{})
	defer close(done)

	// Forward stdin → guest in a background goroutine. When the source
	// reaches EOF (or is absent), send FrameStdinClose so the guest closes
	// the process stdin pipe and EOF-reading programs (cat, tar, sort, …)
	// can proceed. The goroutine does NOT close the whole connection;
	// stdout/stderr keep flowing until the Exit frame.
	go func() {
		if opts.stdin != nil {
			buf := make([]byte, wire.MaxDataPayload)
			for {
				n, err := opts.stdin.Read(buf)
				if n > 0 {
					wrMu.Lock()
					_ = wr.WriteData(wire.StreamStdin, buf[:n])
					wrMu.Unlock()
				}
				if err != nil {
					if err != io.EOF {
						// Unexpected error (context cancel, broken pipe):
						// connection is already broken; skip the signal.
						return
					}
					break // clean EOF → fall through to send StdinClose
				}
			}
		}
		// stdin exhausted (or was nil): signal EOF to the guest.
		wrMu.Lock()
		_ = wr.WriteStdinClose()
		wrMu.Unlock()
	}()

	// Forward window-resize events → guest. The goroutine exits when
	// winsizeCh is closed by the caller (per the WinsizeCh contract above)
	// or defensively when done is closed on pump return, whichever comes first.
	if opts.winsizeCh != nil {
		go func() {
			for {
				select {
				case ws, ok := <-opts.winsizeCh:
					if !ok {
						return
					}
					wrMu.Lock()
					_ = wr.WriteWinsize(ws)
					wrMu.Unlock()
				case <-done:
					return
				}
			}
		}()
	}

	// Main read loop: dispatch incoming frames until the guest sends Exit.
	for {
		frame, err := rd.ReadFrame()
		if err != nil {
			return 0, fmt.Errorf("agent: pump: read frame: %w", err)
		}
		switch frame.Type {
		case wire.FrameData:
			switch frame.Data.Tag {
			case wire.StreamStdout:
				if opts.stdout != nil {
					_, _ = opts.stdout.Write(frame.Data.Payload)
				}
			case wire.StreamStderr:
				if opts.stderr != nil {
					_, _ = opts.stderr.Write(frame.Data.Payload)
				}
			}
		case wire.FrameExit:
			return frame.Exit.Code, nil
			// FrameWinsize received from the guest is ignored on the host receive path.
		}
	}
}
