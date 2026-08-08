// Package perimeter defines the shared types for nexus3's egress-filtering
// subsystem. It is the policy layer: it consumes a raw TAP fd produced by the
// driver transport layer and enforces per-sandbox egress policy.
//
// Dependency direction (hard constraint):
//
//	driver (transport) → fd → perimeter (policy)
//
// The perimeter package MUST NOT import internal/core/driver. The driver may
// not import perimeter. The fd flows one way: driver creates it, perimeter
// consumes it via the driver.NetworkHook capability (discovered by the caller
// via type assertion — the caller imports both packages).
package perimeter

import (
	"context"
	"io"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// Policy is the per-sandbox egress allowlist. It describes which destinations
// a sandbox is permitted to reach. Policy is resolved at sandbox-creation time
// and is immutable for the sandbox's lifetime.
//
// Hostnames are matched case-insensitively. DNS resolution (hostname → IP) is
// the perimeter implementation's responsibility; Policy deliberately does not
// embed IP/CIDR rules so that the allowlist is human-readable and stable
// across IP address changes.
type Policy struct {
	// AllowedHosts is the set of hostnames the sandbox is permitted to reach.
	// An empty slice means deny-all. Wildcard support (if any) is
	// implementation-defined and MUST be documented in the implementation.
	AllowedHosts []string
}

// EgressDecision is the result of evaluating one outbound connection attempt.
type EgressDecision int

const (
	// Allow means the connection attempt is permitted under the active policy.
	Allow EgressDecision = iota

	// Deny means the connection attempt is blocked.
	Deny
)

// String returns the human-readable name of the decision.
func (d EgressDecision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// AuditEvent records one egress attempt and its outcome. A stream of
// AuditEvents is the audit log for a running perimeter.
type AuditEvent struct {
	// Timestamp is when the egress attempt was observed on the wire.
	Timestamp time.Time

	// SandboxID identifies the sandbox that made the attempt.
	SandboxID domain.SandboxID

	// DestHost is the destination as seen in the raw Ethernet frame:
	// a hostname (from SNI/CONNECT) or an IP:port string when no hostname
	// is available (e.g. raw TCP to an IP address).
	DestHost string

	// Decision is the outcome of the policy evaluation.
	Decision EgressDecision

	// Reason is a short human-readable explanation of the decision (e.g.
	// "host allowed by policy", "host not in allowlist", "non-TCP dropped").
	Reason string
}

// Perimeter is the interface a perimeter implementation satisfies. A Perimeter
// receives the raw TAP fd for a sandbox (via [driver.NetworkHook.GuestNetworkFD]),
// reads raw Ethernet frames off it, and enforces the sandbox's [Policy].
//
// Run is blocking: it reads frames and applies policy until ctx is cancelled
// or rw returns an error. The caller is responsible for closing rw after Run
// returns.
//
// Implementations MUST NOT import internal/core/driver. The fd arrives as an
// io.ReadWriteCloser; the driver package is never in the import chain.
type Perimeter interface {
	// Run reads raw Ethernet frames from rw and enforces egress policy until
	// ctx is cancelled or rw returns an error. Each Read on rw returns exactly
	// one complete Ethernet frame (IFF_NO_PI mode — no packet-info header).
	// Run blocks until done; it does not close rw on return.
	Run(ctx context.Context, id domain.SandboxID, rw io.ReadWriteCloser) error
}

// NoOpPerimeter is a [Perimeter] that reads frames and discards them without
// applying any policy. It is used as a tracer-bullet stub and in unit tests
// that only care about fd plumbing, not policy enforcement.
type NoOpPerimeter struct{}

// Run reads raw Ethernet frames from rw until ctx is cancelled or rw errors.
// All frames are discarded. Implements [Perimeter].
func (n *NoOpPerimeter) Run(ctx context.Context, id domain.SandboxID, rw io.ReadWriteCloser) error {
	// maxFrameSize is 65536 bytes: large enough for any Ethernet frame including
	// jumbo frames (9000 B) and datagram-socket datagrams up to 65535 B.
	// One Read call returns exactly one frame; no length framing.
	const maxFrameSize = 65536
	buf := make([]byte, maxFrameSize)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if _, err := rw.Read(buf); err != nil {
			return err
		}
	}
}

// Compile-time assertion: NoOpPerimeter satisfies Perimeter.
var _ Perimeter = (*NoOpPerimeter)(nil)
