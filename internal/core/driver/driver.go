// Package driver defines the substrate seam: the boundary between nexus3's
// core and whatever actually runs a VM.
//
// Two real substrates are planned — Cloud Hypervisor on Linux and Apple's
// Virtualization.framework via a Swift daemon on macOS — but neither is
// implemented here. This package defines the interface and a fake driver so
// every other package can be tested without a hypervisor.
//
// # Authority
//
// The driver is authoritative over the stored record. Where a live VM
// disagrees with the durable state persisted by the store, the VM wins.
// Recovery logic must always call Observe before consulting any cached state.
//
// # Optional capabilities
//
// Optional capabilities — [PauseResumer] — are separate interfaces discovered
// via type assertion. A driver that does not implement an optional interface
// simply does not provide that capability; callers must guard with a comma-ok
// assertion before use.
package driver

import (
	"context"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// RunState is the actual execution state of a VM as reported by the
// substrate. It is a four-valued type.
//
// The zero value is [Unknown], not [Absent]. This is deliberate: a
// zero-value RunState is unambiguously a failure to observe, never a
// statement that no VM exists. Callers must never treat Unknown as Absent;
// conflating them is how a live VM gets destroyed.
type RunState int

const (
	// Unknown means the driver could not determine the VM's state. This is a
	// failure to observe and must never be treated as [Absent]. It is the zero
	// value so that an uninitialized Observation is safe to inspect — the zero
	// state can never be misread as "the VM is gone".
	Unknown RunState = iota

	// Running means the VM exists and is executing.
	Running

	// Paused means the VM exists, is not executing, and its memory is intact
	// in host RAM.
	Paused

	// Absent means there is no VM: it was never started, has been cleanly
	// stopped, or was destroyed when the host rebooted.
	Absent
)

// String returns the human-readable label for the run state.
func (s RunState) String() string {
	switch s {
	case Unknown:
		return "unknown"
	case Running:
		return "running"
	case Paused:
		return "paused"
	case Absent:
		return "absent"
	default:
		return fmt.Sprintf("RunState(%d)", int(s))
	}
}

// Observation is the result of a [Driver.Observe] call. It describes what
// the substrate actually observed, which may differ from the durable record.
type Observation struct {
	// State is the VM's actual run state as seen by the substrate. If the
	// driver could not determine the state, State is [Unknown] and the caller
	// should treat the observation as unreliable.
	State RunState

	// InstanceID identifies the running instantiation. Empty when State is
	// [Absent] or [Unknown].
	InstanceID string

	// Detail is a free-form string suitable for a log line. Drivers should
	// populate it with enough context to diagnose unexpected states.
	Detail string
}

// StartRequest carries the parameters needed to start a new VM instance.
type StartRequest struct {
	// SandboxID is the sandbox being started.
	SandboxID domain.SandboxID

	// ImageDigest is the content-addressable digest of the rootfs image.
	ImageDigest string
}

// Driver is the substrate seam: the boundary between nexus3's core and
// whatever actually runs a VM.
//
// The driver is authoritative over the stored record. Where a live VM
// disagrees with the durable state persisted by the store, the VM wins.
// Recovery logic must always call Observe before consulting any cached state.
//
// Optional capabilities are separate interfaces ([PauseResumer]) discovered
// via type assertion. A driver that does not implement an optional interface
// simply does not provide that capability; callers must guard with a
// comma-ok assertion before use.
//
// # Reentrancy prohibition — self-deadlock hazard
//
// Driver methods are called while the nexus3 core holds the per-sandbox
// exclusive flock (store.Update acquires LOCK_EX and calls the substrate
// method inside the callback). Implementations MUST NOT call back into any
// nexus3 store method — store.Update, store.Delete, store.SetRemovalMarker,
// store.ClearRemovalMarker, or any other method that acquires that flock —
// because the flock is non-recursive: a second attempt from the same process
// to acquire a lock it already holds will deadlock, spinning forever while
// the goroutine waits for itself.
//
// Consequence: a re-entrant driver call deadlocks the sandbox permanently.
// The store.Lock.acquire loop only re-checks ctx on EINTR, so context
// cancellation does not reliably unblock the deadlocked goroutine.
//
// This is safe today only because no production code constructs a Recoverer
// and no real driver exists — cmd_recover.go refuses with "no substrate
// configured". Wiring a real Cloud Hypervisor or nexus3-vzd driver arms this
// hazard immediately. See motive.md for the recorded deferral.
type Driver interface {
	// Name returns a human-readable identifier for the substrate, used in
	// diagnostic output (e.g. "cloud-hypervisor", "vz", "fake").
	Name() string

	// Observe queries the substrate for the actual run state of the VM
	// identified by id. Recovery logic depends on this method being correct.
	//
	// If the driver cannot determine the state it returns an Observation
	// with State == [Unknown] and a non-nil error. Callers must treat Unknown
	// as a failure to observe, never as [Absent]; conflating them is how a
	// live VM gets destroyed.
	//
	// If no VM has ever been started for id, Observe returns Absent without
	// an error.
	Observe(ctx context.Context, id domain.SandboxID) (Observation, error)

	// Start launches a new VM instance for the given request. On success it
	// returns an opaque InstanceID that identifies this instantiation.
	Start(ctx context.Context, req StartRequest) (instanceID string, err error)

	// Stop terminates the VM identified by id. Stop is idempotent: stopping
	// an already-absent VM is not an error.
	Stop(ctx context.Context, id domain.SandboxID) error
}
