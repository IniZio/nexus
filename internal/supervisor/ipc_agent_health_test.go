package supervisor

// Tests for the /supervisor/agent-health endpoint and the pure classification
// function it is built on. See the doc comment on [classifyAgentHealth] for
// why the classification rules are tested as a pure function rather than
// through a real vsock dialer: every input a live probe can produce (nil,
// a definite-refusal error, or an ambiguous timeout-shaped error) is
// representable as a plain Go error value, so the fail-closed rules can be
// mutation-tested directly.

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// ── classifyAgentHealth: pure fail-closed classification ──────────────────

// errRefused/errTimeout stand in for the two shapes DialGuest can produce:
// a definite "nothing is there" refusal, and an ambiguous timeout that must
// NOT be promoted to "gone".
var (
	errRefused = errors.New("cloudhypervisor: dial guest sb-1: connect vsock socket: dial unix /run/nexus3/sb-1.vsock: connect: connection refused")
	errNoFile  = errors.New("cloudhypervisor: dial guest sb-1: connect vsock socket: dial unix /run/nexus3/sb-1.vsock: connect: no such file or directory")
	errTimeout = errors.New("govern: decode sample response: resize/wire: decode envelope: read unix @->/run/nexus3/sb-1.vsock: i/o timeout")
	errEOF     = errors.New("cloudhypervisor: dial guest sb-1: read handshake reply: EOF (guest agent not yet listening on vsock port 1025 — VM may still be starting up)")
)

// TestClassifyAgentHealth_DataPlaneAlive_MeansChannelDownGuestAlive is the
// primary scenario this whole feature exists for (the 2026-08-31 incident):
// control plane fails, data plane succeeds. Must classify as
// AgentChannelDownGuestAlive, never Healthy and never GuestGone.
func TestClassifyAgentHealth_DataPlaneAlive_MeansChannelDownGuestAlive(t *testing.T) {
	got := classifyAgentHealth(errTimeout, nil)
	if got.State != AgentChannelDownGuestAlive {
		t.Fatalf("state = %q, want %q", got.State, AgentChannelDownGuestAlive)
	}
	if got.ControlErr == "" {
		t.Error("ControlErr must be populated so the operator can see WHY the control plane is considered down")
	}
}

// TestClassifyAgentHealth_BothRefused_MeansGuestGone proves the ONLY path to
// GuestGone: both probes show a definite host-side refusal.
func TestClassifyAgentHealth_BothRefused_MeansGuestGone(t *testing.T) {
	cases := []struct {
		name       string
		controlErr error
		dataErr    error
	}{
		{"both connection refused", errRefused, errRefused},
		{"both no such file", errNoFile, errNoFile},
		{"mixed refusal shapes", errRefused, errNoFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAgentHealth(tc.controlErr, tc.dataErr)
			if got.State != AgentChannelGuestGone {
				t.Fatalf("state = %q, want %q", got.State, AgentChannelGuestGone)
			}
		})
	}
}

// TestClassifyAgentHealth_AmbiguousFailure_NeverClaimsGuestGoneOrHealthy is
// the fail-closed mutation target: when the evidence does not definitively
// prove either "reachable" or "gone", the verdict MUST be Unknown — never
// upgraded to Healthy (obviously wrong: the control probe failed) and never
// downgraded to GuestGone (a timeout does not prove absence — declaring the
// guest gone here would make a caller give up on a channel that might still
// recover, exactly the "reconnect loop must not mask a genuinely dead guest,
// but must also not mask a genuinely alive one" requirement).
func TestClassifyAgentHealth_AmbiguousFailure_NeverClaimsGuestGoneOrHealthy(t *testing.T) {
	cases := []struct {
		name       string
		controlErr error
		dataErr    error
	}{
		{"both timeout", errTimeout, errTimeout},
		{"timeout + EOF", errTimeout, errEOF},
		{"refused + timeout (only ONE side definite)", errRefused, errTimeout},
		{"timeout + refused (only ONE side definite)", errTimeout, errRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAgentHealth(tc.controlErr, tc.dataErr)
			if got.State != AgentChannelUnknown {
				t.Fatalf("state = %q, want %q (ambiguous evidence must never resolve to Healthy or GuestGone)", got.State, AgentChannelUnknown)
			}
			if got.Healthy() {
				t.Fatal("Healthy() returned true for an ambiguous/failed probe — fail-closed rail violated")
			}
		})
	}
}

// TestCheckAgentHealth_NilDialer_RefusesAsUnknown is the mutation-bearing
// proof for the "absent input must never mean skip-the-check" rail at the
// checkAgentHealth entry point (as opposed to classifyAgentHealth's internal
// rules, covered above). A nil GuestDialer is exactly the shape a caller
// gets when the driver in use does not implement guest dialing; the ONLY
// correct response is AgentChannelUnknown, never Healthy.
func TestCheckAgentHealth_NilDialer_RefusesAsUnknown(t *testing.T) {
	got := checkAgentHealth(context.Background(), nil, domain.SandboxID{})
	if got.State != AgentChannelUnknown {
		t.Fatalf("state = %q, want %q", got.State, AgentChannelUnknown)
	}
	if got.Healthy() {
		t.Fatal("Healthy() returned true for a nil dialer — fail-closed rail violated")
	}
	if got.ControlErr == "" {
		t.Error("ControlErr must explain why the probe could not run")
	}
}

// TestAgentHealth_Healthy_OnlyTrueForHealthyState pins Healthy()'s full
// truth table so a future edit cannot quietly widen it to cover Unknown.
func TestAgentHealth_Healthy_OnlyTrueForHealthyState(t *testing.T) {
	states := []AgentChannelState{AgentChannelHealthy, AgentChannelDownGuestAlive, AgentChannelGuestGone, AgentChannelUnknown}
	for _, s := range states {
		h := AgentHealth{State: s}
		want := s == AgentChannelHealthy
		if got := h.Healthy(); got != want {
			t.Errorf("AgentHealth{State: %q}.Healthy() = %v, want %v", s, got, want)
		}
	}
}

// ── /supervisor/agent-health HTTP handler ──────────────────────────────────

// serveTestIPCWithHealth mirrors serveTestIPC (ipc_egress_test.go) but wires
// an agentHealthFunc instead of allowEgress, since this suite only exercises
// the agent-health path.
func serveTestIPCWithHealth(t *testing.T, health agentHealthFunc) string {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, err := serveIPC(ctx, sockPath, nil, "test-sandbox", nil, nil, health, "test-hash")
	if err != nil {
		t.Fatalf("serveIPC: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	return sockPath
}

// TestIPCAgentHealth_NilCallback_ReturnsUnknownNot200 proves the handler-level
// half of the fail-closed rail: a supervisor with no wired health probe
// (e.g. IPC started before the driver/sandbox-id needed to build one exist)
// answers with AgentChannelUnknown and a non-200 status, not a silent
// "assume healthy". RequestAgentHealth on the client side must surface that
// same Unknown verdict rather than converting the 503 into a plain transport
// error the caller could mistake for "could not ask, so who knows" — which
// is exactly the ambiguity this endpoint exists to remove.
func TestIPCAgentHealth_NilCallback_ReturnsUnknownNot200(t *testing.T) {
	sockPath := serveTestIPCWithHealth(t, nil)

	health, err := RequestAgentHealth(context.Background(), sockPath)
	if err != nil {
		t.Fatalf("RequestAgentHealth: unexpected transport error: %v", err)
	}
	if health.State != AgentChannelUnknown {
		t.Fatalf("state = %q, want %q", health.State, AgentChannelUnknown)
	}
	if health.Healthy() {
		t.Fatal("nil callback must never report Healthy")
	}
}

// TestIPCAgentHealth_Healthy_RoundTrips proves the happy path end to end
// through the real HTTP handler and RequestAgentHealth client, not a
// reimplementation of either.
func TestIPCAgentHealth_Healthy_RoundTrips(t *testing.T) {
	sockPath := serveTestIPCWithHealth(t, func(ctx context.Context) AgentHealth {
		return AgentHealth{State: AgentChannelHealthy}
	})

	health, err := RequestAgentHealth(context.Background(), sockPath)
	if err != nil {
		t.Fatalf("RequestAgentHealth: %v", err)
	}
	if !health.Healthy() {
		t.Fatalf("got %+v, want Healthy", health)
	}
}

// TestIPCAgentHealth_DownGuestAlive_RoundTrips proves the specific incident
// shape (control down, guest alive) survives the HTTP round trip intact,
// including the diagnostic error text.
func TestIPCAgentHealth_DownGuestAlive_RoundTrips(t *testing.T) {
	sockPath := serveTestIPCWithHealth(t, func(ctx context.Context) AgentHealth {
		return AgentHealth{State: AgentChannelDownGuestAlive, ControlErr: "i/o timeout"}
	})

	health, err := RequestAgentHealth(context.Background(), sockPath)
	if err != nil {
		t.Fatalf("RequestAgentHealth: %v", err)
	}
	if health.State != AgentChannelDownGuestAlive {
		t.Fatalf("state = %q, want %q", health.State, AgentChannelDownGuestAlive)
	}
	if health.ControlErr != "i/o timeout" {
		t.Errorf("ControlErr = %q, want %q", health.ControlErr, "i/o timeout")
	}
}

// ── checkAgentHealth: split probe budget ───────────────────────────────────

// silentControlDialer is a fake driver.GuestDialer for the split-probe-budget
// test. For AgentControlPort it returns a net.Pipe whose server end never
// sends any data — gRPC blocks on the HTTP/2 handshake until controlCtx
// expires, producing a DeadlineExceeded error without hanging in DialGuest
// itself (which would leak gRPC's background connection-management goroutines).
// For DataPort it returns a live net.Pipe immediately so dataErr == nil.
type silentControlDialer struct{ t *testing.T }

func (d silentControlDialer) DialGuest(ctx context.Context, _ domain.SandboxID, port uint32) (net.Conn, error) {
	host, guest := net.Pipe()
	d.t.Cleanup(func() { host.Close(); guest.Close() })
	if port == driver.AgentControlPort {
		// Server end (guest) holds open but never writes — gRPC waits for
		// HTTP/2 SETTINGS frame that never arrives, blocking until controlCtx
		// deadline fires.
		return host, nil
	}
	// DataPort: honour the caller's context deadline. If the context is
	// already expired (shared-budget regression: dataCtx := controlCtx),
	// the deadline has been consumed by the hung control probe and the data
	// dial sees an expired context — return the error instead of a live pipe
	// so the test goes RED when the shared-budget bug is reinstalled.
	select {
	case <-ctx.Done():
		host.Close()
		guest.Close()
		return nil, ctx.Err()
	default:
		return host, nil
	}
}

// TestCheckAgentHealth_HungControlProbe_DataPlaneAlive_GivesDownGuestAlive
// proves the split-budget fix: when the control-plane Ping hangs for its full
// per-probe budget and the data plane gets its own independent budget, the
// result is AgentChannelDownGuestAlive, not Unknown.
//
// With the OLD shared budget (one context.WithTimeout for both probes), a
// hung control probe exhausts the whole deadline; the data probe then sees an
// already-expired context and fails with a timeout error. Both probes produce
// ambiguous errors → classifyAgentHealth returns Unknown, and the real-world
// incident shape (wedged gRPC channel over a live vsock) is invisible.
//
// Mutation proof for the fail-closed rule: changing classifyAgentHealth so
// that a single definite refusal (or dataErr == nil) alone yields GuestGone
// makes TestClassifyAgentHealth_AmbiguousFailure_NeverClaimsGuestGoneOrHealthy
// RED — those tests cover the exact combination "refused + timeout = Unknown".
func TestCheckAgentHealth_HungControlProbe_DataPlaneAlive_GivesDownGuestAlive(t *testing.T) {
	orig := agentHealthProbeTimeout
	agentHealthProbeTimeout = 50 * time.Millisecond
	defer func() { agentHealthProbeTimeout = orig }()

	got := checkAgentHealth(context.Background(), silentControlDialer{t: t}, domain.SandboxID{})
	if got.State != AgentChannelDownGuestAlive {
		t.Fatalf("state = %q, want %q\n"+
			"with a shared probe budget, a hung control probe would exhaust the\n"+
			"whole timeout and the data probe would see an expired context,\n"+
			"yielding Unknown instead of AgentChannelDownGuestAlive",
			got.State, AgentChannelDownGuestAlive)
	}
	if got.ControlErr == "" {
		t.Error("ControlErr must be populated so the operator can see why the control plane is considered down")
	}
}
