package driver

import (
	"context"
	"io"
	"net"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// AgentControlPort is the fixed vsock port number the guest agent listens on
// for its gRPC control plane. The host always dials the guest (never the
// reverse). The spec does not assign a specific number (ticket 16), so
// nexus3 uses 1024.
const AgentControlPort uint32 = 1024

// PauseResumer is an optional capability for drivers that can pause and
// resume a running VM without destroying its memory state.
//
// Discovered via type assertion: if drv, ok := d.(PauseResumer); ok { ... }
type PauseResumer interface {
	// Pause suspends execution of the VM identified by id, leaving its memory
	// intact in host RAM.
	Pause(ctx context.Context, id domain.SandboxID) error

	// Resume restarts execution of a previously paused VM.
	Resume(ctx context.Context, id domain.SandboxID) error
}

// GuestDialer is an optional capability for drivers that can open a raw
// byte-stream connection to a port inside a running guest VM via the vsock
// transport. The returned [net.Conn] is a bidirectional stream; the caller
// is responsible for closing it when done.
//
// The host always dials the guest (never the reverse). Well-known ports:
// [AgentControlPort] (1024) for the gRPC control plane and
// [wire.DataPort] (1025) for the data plane.
//
// Discovered via type assertion: if d, ok := drv.(GuestDialer); ok { ... }
type GuestDialer interface {
	// DialGuest connects to the given port inside the VM identified by id
	// and returns a [net.Conn] backed by the substrate's vsock transport.
	// The connection is raw bytes; callers layer their own protocol on top.
	// Use [AgentControlPort] for gRPC control traffic and the wire package's
	// DataPort for data-plane traffic.
	DialGuest(ctx context.Context, id domain.SandboxID, port uint32) (net.Conn, error)
}

// Snapshotter is an optional capability for drivers that can capture a
// point-in-time snapshot of a sandbox. The sandbox state is unchanged after
// the operation (self-edge: running→running or stopped→stopped, held under a
// lease). The resulting [artifact.Snapshot] can be used as a fork source.
//
// Discovered via type assertion: if s, ok := drv.(Snapshotter); ok { ... }
type Snapshotter interface {
	// TakeSnapshot captures the current state of the sandbox identified by id.
	// kind controls the durability contract of the resulting artifact.
	// The sandbox is not stopped; it continues running (or remains stopped)
	// while the snapshot is taken.
	TakeSnapshot(ctx context.Context, id domain.SandboxID, kind artifact.SnapshotKind) (artifact.Snapshot, error)
}

// NetworkHook is an optional capability for drivers that expose the host-side
// network end for a running sandbox VM. The returned [io.ReadWriteCloser]
// carries raw Ethernet frames (IEEE 802.3, layer 2). Each Read returns exactly
// one complete frame; each Write injects one frame into the VM's virtual NIC.
//
// The concrete dynamic type is net.Conn (backed by an AF_UNIX SOCK_DGRAM
// socketpair). The perimeter layer may type-assert the result to net.Conn to
// hand it to gvproxy's AcceptVfkit. The driver-side pump goroutine bridges
// the socketpair to a host-side TAP interface that is L2-bridged to the
// guest-facing TAP owned by CH.
//
// The connection is created at Start time and held until either
// GuestNetworkFD transfers ownership or the sandbox is stopped.
//
// The caller is responsible for closing the returned value when done.
// After GuestNetworkFD returns, the driver no longer holds a reference to
// the value; subsequent calls for the same sandbox return an error.
//
// The dependency direction is strictly one-way: the driver (transport layer)
// creates and hands off the connection; the perimeter package (policy layer)
// consumes it. The driver package MUST NOT import the perimeter package.
//
// Discovered via type assertion: if h, ok := drv.(NetworkHook); ok { ... }
type NetworkHook interface {
	// GuestNetworkFD returns the host-side network end for the sandbox
	// identified by id. The caller owns the returned value and must close it
	// when done. Ownership is transferred: calling GuestNetworkFD twice for
	// the same sandbox returns an error on the second call.
	GuestNetworkFD(ctx context.Context, id domain.SandboxID) (io.ReadWriteCloser, error)
}

// Forker is an optional capability for drivers that can spawn N child sandbox
// VMs from an existing snapshot. The parent sandbox (snap.SandboxID) is
// unaffected — fork is pure child-creation (spec 06, edge 5: ∅→running).
//
// Discovered via type assertion: if f, ok := drv.(Forker); ok { ... }
type Forker interface {
	// ForkFrom spawns one new VM per entry in childIDs, initialising each from
	// snap. Returns per-child instance IDs in the same order as childIDs.
	// The parent sandbox is not stopped or modified.
	ForkFrom(ctx context.Context, snap artifact.Snapshot, childIDs []domain.SandboxID) (instanceIDs []string, err error)
}

// SnapshotRemover is an optional capability for drivers that manage files
// beyond the artifact-store record for a snapshot (e.g. Cloud Hypervisor
// writes a memory-image directory alongside the artifact payload).
// RemoveSnapshot removes BOTH the artifact-store record AND any
// driver-managed files, mirroring the transient-reap logic used internally
// after a ForkFrom.
//
// The service layer calls RemoveSnapshot (if the driver implements it) and
// then calls artifact.Store.Remove as an idempotent second pass — safe
// because artifact.Store.Remove is a no-op for a non-existent snapshot.
//
// Discovered via type assertion: if r, ok := drv.(SnapshotRemover); ok { ... }
type SnapshotRemover interface {
	// RemoveSnapshot removes the artifact-store record and any driver-managed
	// files for snapID. It is idempotent if snapID does not exist.
	RemoveSnapshot(id artifact.SnapshotID) error
}

// Capabilities returns the names of optional capability interfaces that drv
// satisfies. The result is suitable for doctor-style diagnostic output.
func Capabilities(drv Driver) []string {
	var caps []string
	if _, ok := drv.(PauseResumer); ok {
		caps = append(caps, "PauseResumer")
	}
	if _, ok := drv.(GuestDialer); ok {
		caps = append(caps, "GuestDialer")
	}
	if _, ok := drv.(Snapshotter); ok {
		caps = append(caps, "Snapshotter")
	}
	if _, ok := drv.(Forker); ok {
		caps = append(caps, "Forker")
	}
	if _, ok := drv.(SnapshotRemover); ok {
		caps = append(caps, "SnapshotRemover")
	}
	if _, ok := drv.(NetworkHook); ok {
		caps = append(caps, "NetworkHook")
	}
	return caps
}
