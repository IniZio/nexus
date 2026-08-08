package agent

import (
	"context"
	"io"

	"github.com/IniZio/nexus3/internal/core/agent/wire"
)

// AttachOptions configures a [Client.Attach] call.
type AttachOptions struct {
	// SessionID identifies the existing guest session to reattach to.
	SessionID string
	// ResumeFromOffset is the byte offset in the guest-authoritative output
	// ring from which the guest should begin replaying Data frames.
	// Zero means "from the beginning" (equivalent to a fresh attach).
	ResumeFromOffset uint64
	// Stdin, Stdout, Stderr, WinsizeCh mirror [ExecOptions].
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	WinsizeCh <-chan wire.Winsize
}

// Attach reattaches to an existing guest session identified by SessionID.
// It opens a fresh data-plane connection, sends the reattach Handshake
// (with ResumeFromOffset set), reads the HandshakeAck, then pumps frames
// until the session exits.
//
// Note: raw-terminal mode and screen repaint are the CLI surface's concern
// (a later slice); this method works on the supplied io streams directly.
func (c *Client) Attach(ctx context.Context, opts AttachOptions) (int32, error) {
	return runDataPump(ctx, c, pumpOpts{
		sessionID:        opts.SessionID,
		resumeFromOffset: opts.ResumeFromOffset,
		stdin:            opts.Stdin,
		stdout:           opts.Stdout,
		stderr:           opts.Stderr,
		winsizeCh:        opts.WinsizeCh,
	})
}
