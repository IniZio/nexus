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
	"os"
	"time"

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

// AgentInfo returns the in-guest agent's build tag.
func (c *Client) AgentInfo(ctx context.Context) (string, error) {
	stub, cc, err := c.controlClient(ctx)
	if err != nil {
		return "", fmt.Errorf("agent: info: dial: %w", err)
	}
	defer cc.Close()
	resp, err := stub.AgentInfo(ctx, &agentpb.AgentInfoRequest{})
	if err != nil {
		return "", fmt.Errorf("agent: info: %w", err)
	}
	return resp.GetBuildTag(), nil
}

// AgentUpgradeOptions configures [Client.AgentUpgrade].
type AgentUpgradeOptions struct {
	// LocalBinaryPath is the path on the host to the replacement nexus3-agent
	// binary.  It is pushed to the guest via Copy, then RestartAgent is called.
	LocalBinaryPath string
	// Force bypasses the active-sessions guard in the guest.
	Force bool
}

// AgentUpgrade hot-swaps the in-guest agent binary without stopping the
// sandbox.  It:
//  1. Reads the local binary and records its byte count.
//  2. Pushes it to a staging path on the SAME filesystem as the install path
//     (/sbin/.nexus3-agent.upgrade) to avoid EXDEV on os.Rename.
//  3. Calls RestartAgent{staged_path, expected_bytes, force}.
//  4. Waits (via Ping) until the new agent is reachable.
//  5. Calls AgentInfo to return the new build tag.
//
// The RestartAgent RPC typically does not return a response (the guest
// process image is replaced by syscall.Exec); a connection-reset error is
// treated as "restart initiated".
//
// If ctx has no deadline, AgentUpgrade applies a default 60-second deadline
// so callers that pass context.Background() do not hang indefinitely.
func (c *Client) AgentUpgrade(ctx context.Context, opts AgentUpgradeOptions) (newTag string, err error) {
	// Apply a default deadline if the caller did not set one.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, agentUpgradeDefaultTimeout)
		defer cancel()
	}

	// ── Step 1: stat the local binary ──────────────────────────────────────
	fi, err := os.Stat(opts.LocalBinaryPath)
	if err != nil {
		return "", fmt.Errorf("agent: upgrade: stat local binary %q: %w", opts.LocalBinaryPath, err)
	}
	expectedBytes := fi.Size()

	// ── Step 2: push to staging path on same filesystem as install path ────
	// The staging path MUST be on the rootfs ext4 partition (same device as
	// /sbin/nexus3-agent).  Using /tmp would cross the tmpfs→ext4 boundary
	// and cause EXDEV on os.Rename inside the guest.
	f, err := os.Open(opts.LocalBinaryPath)
	if err != nil {
		return "", fmt.Errorf("agent: upgrade: open local binary %q: %w", opts.LocalBinaryPath, err)
	}
	defer f.Close()

	// agentStagingPath must match agentStagingPath in cmd/nexus3-agent/swap_linux.go.
	// It is hardcoded here (not exported from the agent package) because it is a
	// guest-internal path that the host specifies in the RPC request.
	const stagedPath = "/sbin/.nexus3-agent.upgrade"
	if err := c.Copy(ctx, CopyOptions{
		Direction:     agentpb.CopyDirection_COPY_DIRECTION_PUSH,
		GuestPath:     stagedPath,
		IsDirectory:   false,
		Src:           f,
		ExpectedBytes: &expectedBytes,
	}); err != nil {
		return "", fmt.Errorf("agent: upgrade: push binary: %w", err)
	}

	// ── Step 3: trigger RestartAgent ───────────────────────────────────────
	stub, cc, err := c.controlClient(ctx)
	if err != nil {
		return "", fmt.Errorf("agent: upgrade: dial for restart: %w", err)
	}
	defer cc.Close()

	_, restartErr := stub.RestartAgent(ctx, &agentpb.RestartAgentRequest{
		StagedPath:    stagedPath,
		ExpectedBytes: expectedBytes,
		Force:         opts.Force,
	})
	// A connection-reset or transport error is expected: the guest called
	// syscall.Exec and the gRPC connection died.  Any other error (e.g.
	// codes.FailedPrecondition for active sessions, codes.InvalidArgument,
	// codes.Internal with a pre-exec error) is a real failure.
	if restartErr != nil && !isTransportReset(restartErr) {
		return "", fmt.Errorf("agent: upgrade: restart: %w", restartErr)
	}
	cc.Close() // close explicitly before reconnecting

	// ── Step 4: wait for the new agent to answer Ping ──────────────────────
	if err := c.waitReady(ctx); err != nil {
		return "", fmt.Errorf("agent: upgrade: wait for new agent: %w", err)
	}

	// ── Step 5: confirm the new version ────────────────────────────────────
	newTag, err = c.AgentInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("agent: upgrade: read new version: %w", err)
	}
	return newTag, nil
}

// waitReady polls Ping until the agent answers or ctx is cancelled.
func (c *Client) waitReady(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := c.Ping(ctx); err == nil {
			return nil
		}
		// Brief sleep between probes — avoid hammering vsock.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timerAfter(pingRetryInterval):
		}
	}
}

// pingRetryInterval is the delay between Ping probes in waitReady.
// Package-level variable so tests can set it to 0.
var pingRetryInterval = 100 * time.Millisecond

// agentUpgradeDefaultTimeout is the fallback deadline applied by AgentUpgrade
// when the caller's context has no deadline.
const agentUpgradeDefaultTimeout = 60 * time.Second

// isTransportReset reports whether err is a low-level transport failure
// (connection reset, EOF) that is expected when the guest execs itself.
func isTransportReset(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// These substrings cover:
	//   - grpc transport: "connection reset by peer"
	//   - net: "EOF"
	//   - grpc: "Unavailable" when the server disappears mid-RPC
	return contains(s, "connection reset by peer") ||
		contains(s, "EOF") ||
		contains(s, "Unavailable") ||
		contains(s, "transport is closing")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// timerAfter is time.After, replaceable in tests.
var timerAfter = func(d time.Duration) <-chan time.Time {
	return time.After(d)
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
