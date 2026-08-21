// Package agent provides the host-side agent client for nexus3.
//
// The client dials a guest VM through the [driver.GuestDialer] interface and
// speaks two independent protocols:
//
//   - Control plane: gRPC over [driver.AgentControlPort] (1024). Carries
//     request/response RPCs — Exec, Signal, SessionStatus, Copy, etc.
//     Implemented by the generated [agentpb] package.
//
//   - Data plane: clawk-framed byte stream over [wire.DataPort] (1025).
//     Carries interactive stdio (Data frames) plus Handshake/Exit/Winsize
//     frames. Implemented by the [wire] package.
//
// The host-side buffer maintained by this package is only a transient
// repaint/fan-out cache; the authoritative output history lives in the
// in-guest ring (cmd/nexus3-agent, a later slice).
package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// Client is the host-side agent client. It dials the guest agent over a
// [driver.GuestDialer] and exposes exec, attach, and copy operations.
//
// The host-side buffer in this client is only a transient repaint/fan-out
// cache; the authoritative output history lives in the guest ring.
type Client struct {
	dialer driver.GuestDialer
	id     domain.SandboxID
}

// NewClient returns a Client that dials the sandbox identified by id using d
// as the vsock transport.
func NewClient(d driver.GuestDialer, id domain.SandboxID) *Client {
	return &Client{dialer: d, id: id}
}

// controlClient dials the gRPC control plane and returns an
// [agentpb.AgentServiceClient]. The caller must close the returned
// [*grpc.ClientConn] when done.
func (c *Client) controlClient(ctx context.Context) (agentpb.AgentServiceClient, *grpc.ClientConn, error) {
	// "passthrough:///nexus3-agent" bypasses the DNS resolver and makes grpc
	// use the WithContextDialer exclusively, which is what we need for vsock.
	cc, err := grpc.NewClient(
		"passthrough:///nexus3-agent",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return c.dialer.DialGuest(ctx, c.id, driver.AgentControlPort)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("agent: dial control: %w", err)
	}
	return agentpb.NewAgentServiceClient(cc), cc, nil
}

// Ping proves the guest agent is listening by issuing a ListSessions RPC and
// discarding the response. It is the cheapest available call: read-only, no
// side effects, fails immediately if the vsock control port is not open.
// Used by the supervisor's unconditional liveness probe before seeding.
func (c *Client) Ping(ctx context.Context) error {
	stub, cc, err := c.controlClient(ctx)
	if err != nil {
		return fmt.Errorf("agent: ping: dial: %w", err)
	}
	defer cc.Close()
	if _, err := stub.ListSessions(ctx, &agentpb.ListSessionsRequest{}); err != nil {
		return fmt.Errorf("agent: ping: %w", err)
	}
	return nil
}

// dialData opens a raw data-plane connection to the guest.
func (c *Client) dialData(ctx context.Context) (net.Conn, error) {
	conn, err := c.dialer.DialGuest(ctx, c.id, wire.DataPort)
	if err != nil {
		return nil, fmt.Errorf("agent: dial data: %w", err)
	}
	return conn, nil
}

// newSessionID mints a cryptographically random session identifier.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("agent: crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
