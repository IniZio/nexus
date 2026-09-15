package driver

import (
	"context"
	"io"
	"net"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
)

const AgentControlPort uint32 = 1024 // guest agent gRPC control plane

const GitSSHRelayPort uint32 = 1026 // host relay for guest git-ssh sessions

type PauseResumer interface { // optional: pause/resume VM without destroying memory state
	Pause(ctx context.Context, id domain.SandboxID) error  // suspend execution, keep memory
	Resume(ctx context.Context, id domain.SandboxID) error // restart paused VM
}

type GuestDialer interface { // optional: open raw byte-stream to guest port via vsock
	DialGuest(ctx context.Context, id domain.SandboxID, port uint32) (net.Conn, error) // connect to port inside VM
}

type Snapshotter interface { // optional: capture point-in-time snapshot
	TakeSnapshot(ctx context.Context, id domain.SandboxID, kind artifact.SnapshotKind) (artifact.Snapshot, error) // state unchanged after operation
}

type NetworkHook interface { // optional: host-side network end for sandbox VM
	GuestNetworkFD(ctx context.Context, id domain.SandboxID) (io.ReadWriteCloser, error) // transfer ownership; driver→perimeter direction only
}

type Forker interface { // optional: spawn child VMs from snapshot
	ForkFrom(ctx context.Context, snap artifact.Snapshot, childIDs []domain.SandboxID) (instanceIDs []string, err error) // parent unaffected; pure child-creation
}

type SnapshotRemover interface { // optional: remove driver-managed files beyond artifact-store
	RemoveSnapshot(id artifact.SnapshotID) error // removes record and driver files; idempotent
}

func Capabilities(drv Driver) []string { // names of optional capability interfaces satisfied by drv
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
	if _, ok := drv.(NetnsStateProvider); ok {
		caps = append(caps, "NetnsStateProvider")
	}
	return caps
}

type NetnsStateProvider interface { // optional: netns adoption identity
	NetnsState(id domain.SandboxID) (st NetnsIdentity, ok bool) // from most recent Start; safe to call from store.Update callback
}

type NetnsIdentity struct { // everything to re-acquire running VM without rebooting
	// PLANNED path (outgoing supervisor alive, passes perimeter fd):
	// Child/API fields verify child hasn't been pid-recycled.
	// CRASH path (no live sender): Control fields get fresh perimeter end.
	ChildPID       int
	ChildPGID      int
	ChildStartTime uint64
	GuestTap       string
	APISocket      string
	ControlSocket  string
	ControlToken   string
}
