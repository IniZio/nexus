package agent

import (
	"context"
	"github.com/IniZio/nexus/internal/core/driver"
)

// AttachOptions is the port type; see [driver.AttachOptions].
type AttachOptions = driver.AttachOptions

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
