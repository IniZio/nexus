package selfhost_test

// TBD-PD-32: SeedLoop must write the agent's credential placeholder only into
// sandboxes that actually run an agent.
//
// It previously seeded on every non-human-secret sandbox, so a plain sandbox
// received an agent credential env file it never reads — and, once a second
// agent exists, would have received the wrong agent's variables. The CA cert is
// seeded either way: any sandbox behind the proxy needs it to speak HTTPS.
//
// D-J14: SeedLoop must distinguish "guest never reachable" (guestEverResponded=false)
// from "guest alive, seeding incomplete" (guestEverResponded=true, ok=false).
// The former must NOT write READY; the latter is a deliberate degradation.
//
// Unit test: no KVM, no live VM.

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// TestSeedLoop_GuestReachability proves the two-direction contract introduced
// by D-J14: SeedLoop must carry one extra bit distinguishing "dead guest" from
// "live guest with incomplete seeding."
//
// Direction (a): CA seeder always fails → guest never reachable →
//
//	ok=false, guestEverResponded=false. No pidfile must be written.
//
// Direction (b): CA seeder succeeds once but agent seeder always fails →
//
//	ok=false, guestEverResponded=true. Degradation is intentional; READY is
//	written anyway (tested here by asserting guestEverResponded=true so the
//	caller can take the degraded path instead of the hard-fail path).
func TestSeedLoop_GuestReachability(t *testing.T) {
	t.Parallel()

	cert := &x509.Certificate{}
	broker := cred.NewBroker()
	id := domain.NewSandboxID()

	// ── (a) guest never reachable: CA seeder always returns error ──────────
	t.Run("guest_never_reachable", func(t *testing.T) {
		t.Parallel()
		failCA := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
			return errors.New("vsock: EOF")
		})
		okAgent := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
			return nil
		})
		c := cert
		ok, guestEverResponded := supervisor.SeedLoop(context.Background(), id, &c,
			failCA, okAgent, broker, nil, 3, 0, nil, true, cred.ClaudeCodeProfile, nil)
		if ok {
			t.Fatal("ok=true but CA seeder always failed")
		}
		if guestEverResponded {
			// MUTATION TARGET: if the fix is reverted (always returning false,false
			// or always returning false,true), one of these assertions catches it.
			t.Fatal("guestEverResponded=true but CA seeder never succeeded — supervisor would falsely write READY")
		}
	})

	// ── (b) guest reachable, seeding incomplete: CA ok, agent seeder fails ─
	t.Run("guest_alive_seed_incomplete", func(t *testing.T) {
		t.Parallel()
		okCA := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
			return nil // CA seed succeeds → guest is alive
		})
		failAgent := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
			return errors.New("agent: write failed")
		})
		c := cert
		ok, guestEverResponded := supervisor.SeedLoop(context.Background(), id, &c,
			okCA, failAgent, broker, nil, 3, 0, nil, true, cred.ClaudeCodeProfile, nil)
		if ok {
			t.Fatal("ok=true but agent seeder always failed")
		}
		if !guestEverResponded {
			// MUTATION TARGET: if guestEverResponded is never set true even when CA
			// succeeds, the supervisor would refuse READY on a live guest —
			// blocking sandboxes that work for everything except credential seeding.
			t.Fatal("guestEverResponded=false but CA seeder always succeeded — supervisor would hard-fail a live guest")
		}
	})
}

// TestProbeGuestAgent_GuestReachability proves the unconditional liveness gate
// introduced by D-J14 fix round 2. ProbeGuestAgent is the gate that fires
// BEFORE the noProxy check, so a dead guest is caught regardless of whether a
// MITM proxy is present.
//
// Direction (a): prober always fails → ProbeGuestAgent returns error.
//
//	This is the noProxy + dead-guest case that escaped the first fix.
//
// Direction (b): prober eventually succeeds → ProbeGuestAgent returns nil.
//
//	The sandbox is live; proceed to seeding.
func TestProbeGuestAgent_GuestReachability(t *testing.T) {
	t.Parallel()

	// ── (a) guest never reachable: Ping always fails → probe returns error ─
	// This is the path that used to produce a false rc=0 for noProxy sandboxes.
	t.Run("guest_never_reachable", func(t *testing.T) {
		t.Parallel()
		dead := &fakeProber{err: errors.New("vsock: connection refused")}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := supervisor.ProbeGuestAgent(ctx, dead, 5*time.Millisecond)
		if err == nil {
			// MUTATION TARGET: if ProbeGuestAgent is removed or returns nil
			// unconditionally, this assertion catches it — and RunDetached would
			// write READY for a sandbox whose guest is dead.
			t.Fatal("ProbeGuestAgent returned nil but prober always fails — supervisor would falsely write READY for noProxy sandboxes")
		}
	})

	// ── (b) guest alive: Ping succeeds on first try → probe returns nil ────
	t.Run("guest_alive", func(t *testing.T) {
		t.Parallel()
		alive := &fakeProber{err: nil}
		ctx := context.Background()
		err := supervisor.ProbeGuestAgent(ctx, alive, 5*time.Millisecond)
		if err != nil {
			// MUTATION TARGET: if ProbeGuestAgent always returns an error,
			// this catches it — RunDetached would refuse READY for live guests.
			t.Fatalf("ProbeGuestAgent returned error for an always-responsive prober: %v", err)
		}
	})
}

// fakeProber implements supervisor.GuestProber for unit tests.
type fakeProber struct {
	err error
}

func (f *fakeProber) Ping(_ context.Context) error { return f.err }

func TestSeedLoop_AgentCredsOnlyForAgentSandboxes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		seedAgentCreds bool
		wantAgentSeed  bool
	}{
		{name: "agent sandbox", seedAgentCreds: true, wantAgentSeed: true},
		{name: "plain sandbox", seedAgentCreds: false, wantAgentSeed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var caSeeded, agentSeeded bool
			caSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
				caSeeded = true
				return nil
			})
			agentSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
				agentSeeded = true
				return nil
			})

			cert := &x509.Certificate{}
			// nil svc is safe: cert is non-nil, so GetPerimeterCACert is never called.
			done, _ := supervisor.SeedLoop(context.Background(), domain.NewSandboxID(), &cert,
				caSeeder, agentSeeder, cred.NewBroker(), nil, 3, 0, nil, tc.seedAgentCreds, cred.ClaudeCodeProfile, nil)

			if !done {
				t.Fatal("SeedLoop returned false although every seeder succeeded")
			}
			if !caSeeded {
				t.Error("CA cert was not seeded; every sandbox behind the proxy needs it")
			}
			if agentSeeded != tc.wantAgentSeed {
				t.Errorf("agent credential seeded = %v, want %v", agentSeeded, tc.wantAgentSeed)
			}
		})
	}
}
