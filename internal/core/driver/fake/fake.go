// Package fake provides an in-memory [driver.Driver] implementation for use
// in tests. It is safe for concurrent use and passes go test -race.
//
// The fake can simulate every scenario the lifecycle machine must handle:
//   - a live running VM
//   - a paused VM
//   - an absent VM (never started, cleanly stopped)
//   - memory destroyed by a host reboot: a paused VM becomes absent
//   - a crashed VMM: a running VM becomes absent
//   - an observation failure returning [driver.Unknown] + a non-nil error
//   - a genuinely indeterminate observation: [driver.Unknown] + nil error
//     (driver queried the substrate successfully but cannot determine state)
package fake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// CallKind identifies which Driver method was called.
type CallKind string

const (
	CallObserve      CallKind = "Observe"
	CallStart        CallKind = "Start"
	CallStop         CallKind = "Stop"
	CallPause        CallKind = "Pause"
	CallResume       CallKind = "Resume"
	CallDialGuest    CallKind = "DialGuest"
	CallTakeSnapshot CallKind = "TakeSnapshot"
	CallForkFrom     CallKind = "ForkFrom"
)

// Call records a single invocation of a Driver method.
type Call struct {
	Kind CallKind
	// ID is the SandboxID argument, if any.
	ID domain.SandboxID
}

// vmRecord holds the in-memory state of a single fake VM.
type vmRecord struct {
	state      driver.RunState
	instanceID string
}

// FakeDriver is a fully in-memory driver.Driver implementation safe for
// concurrent use. Construct with [New] rather than a struct literal so the
// internal maps are initialised.
type FakeDriver struct {
	mu sync.RWMutex

	vms map[domain.SandboxID]*vmRecord

	// observeErr, when non-nil, causes every Observe call to return
	// driver.Unknown plus this error. Set with [FakeDriver.SetObserveError].
	observeErr error

	// indeterminate, when true, causes Observe to return State: driver.Unknown
	// with a nil error. This deliberately violates the driver.Driver contract
	// (which requires Unknown + non-nil error) to simulate a driver that
	// successfully queried the substrate but genuinely cannot determine VM
	// state. It is the exact condition the || obs.State == driver.Unknown
	// guard in recoverByID defends against.
	indeterminate bool

	// per-method injected errors
	startErr     error
	stopErr      error
	pauseErr     error
	resumeErr    error
	dialGuestErr error
	snapshotErr  error
	forkErr      error

	// snapshots holds in-memory snapshots created by TakeSnapshot.
	snapshots map[artifact.SnapshotID]artifact.Snapshot

	// snapshotDir, when non-empty, is used by RemoveSnapshot to delete the
	// simulated CH memory-image directory at <snapshotDir>/<snapID>/.
	// Set via SetSnapshotDir. Mirrors the production SnapshotDir layout.
	snapshotDir string

	// guestConns holds the "guest" side of each net.Pipe created by DialGuest.
	// Tests retrieve the other end via [FakeDriver.GuestConn].
	guestConns map[domain.SandboxID]net.Conn

	calls []Call
}

// New constructs an initialised FakeDriver. The returned value satisfies
// [driver.Driver], [driver.PauseResumer], [driver.GuestDialer],
// [driver.Snapshotter], and [driver.Forker].
func New() *FakeDriver {
	return &FakeDriver{
		vms:        make(map[domain.SandboxID]*vmRecord),
		snapshots:  make(map[artifact.SnapshotID]artifact.Snapshot),
		guestConns: make(map[domain.SandboxID]net.Conn),
	}
}

// --- Simulation helpers ---

// SetRunning puts the given sandbox into the Running state with a fresh
// instance ID, as if a successful Start had occurred.
func (f *FakeDriver) SetRunning(id domain.SandboxID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vms[id] = &vmRecord{state: driver.Running, instanceID: newInstanceID()}
}

// SetPaused puts the given sandbox into the Paused state, as if a successful
// Pause had occurred.
func (f *FakeDriver) SetPaused(id domain.SandboxID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.vms[id]
	if rec == nil {
		rec = &vmRecord{instanceID: newInstanceID()}
		f.vms[id] = rec
	}
	rec.state = driver.Paused
}

// SetAbsent removes the sandbox from the fake's in-memory table, as if it
// had been cleanly stopped or never started.
func (f *FakeDriver) SetAbsent(id domain.SandboxID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.vms, id)
}

// SimulateHostReboot transitions all Paused sandboxes to Absent, as if the
// host rebooted and memory was lost. Running sandboxes are unaffected because
// ACPI shutdown events are delivered before power is cut in a clean reboot;
// only paused (frozen) VMs lose their state. This is the exact scenario that
// the lifecycle machine's substrate_lost edge handles.
func (f *FakeDriver) SimulateHostReboot() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, rec := range f.vms {
		if rec.state == driver.Paused {
			delete(f.vms, id)
		}
	}
}

// SimulateCrash marks the given sandbox as Absent, as if the VMM process
// crashed while the VM was Running.
func (f *FakeDriver) SimulateCrash(id domain.SandboxID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.vms, id)
}

// SetObserveError causes all subsequent Observe calls to return
// driver.Unknown plus err. Pass nil to clear the injected error.
func (f *FakeDriver) SetObserveError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observeErr = err
}

// SetIndeterminate causes all subsequent Observe calls to return
// driver.Unknown with a nil error. Pass false to clear.
//
// This deliberately violates the driver.Driver contract (which requires a
// non-nil error when returning Unknown) so that tests can exercise the
// defensive || obs.State == driver.Unknown guard in recoverByID. It is
// distinct from [FakeDriver.SetObserveError], which returns Unknown + a
// non-nil error (the contract-compliant failure mode).
func (f *FakeDriver) SetIndeterminate(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.indeterminate = on
}

// SetStartError injects an error to be returned by the next Start call.
// Pass nil to clear.
func (f *FakeDriver) SetStartError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startErr = err
}

// SetStopError injects an error to be returned by the next Stop call.
// Pass nil to clear.
func (f *FakeDriver) SetStopError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopErr = err
}

// SetPauseError injects an error to be returned by the next Pause call.
func (f *FakeDriver) SetPauseError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauseErr = err
}

// SetResumeError injects an error to be returned by the next Resume call.
func (f *FakeDriver) SetResumeError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeErr = err
}

// SetDialGuestError injects an error to be returned by the next DialGuest
// call. Pass nil to clear.
func (f *FakeDriver) SetDialGuestError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dialGuestErr = err
}

// SetSnapshotError injects an error to be returned by the next TakeSnapshot
// call. Pass nil to clear.
func (f *FakeDriver) SetSnapshotError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotErr = err
}

// SetForkError injects an error to be returned by the next ForkFrom call.
// Pass nil to clear.
func (f *FakeDriver) SetForkError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forkErr = err
}

// SetSnapshotDir configures the directory that RemoveSnapshot uses to remove
// simulated CH memory-image files. When set, RemoveSnapshot removes
// <dir>/<snapID>/ in addition to the in-memory snapshot record. Pass an
// empty string to disable directory removal (the default).
func (f *FakeDriver) SetSnapshotDir(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotDir = dir
}

// Snapshot returns the in-memory snapshot with the given ID, or the zero value
// if no such snapshot exists. Tests use this to inspect snapshots created by
// TakeSnapshot without going through disk.
func (f *FakeDriver) Snapshot(id artifact.SnapshotID) (artifact.Snapshot, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	snap, ok := f.snapshots[id]
	return snap, ok
}

// GuestConn returns the "guest" side of the in-memory pipe created by the
// most recent [FakeDriver.DialGuest] call for id. Returns nil if DialGuest
// has not been called for id, or if the call failed.
//
// Tests use this to send data from the simulated guest and read replies:
//
//	hostConn, _ := drv.DialGuest(ctx, id)
//	guestConn   := drv.GuestConn(id)
//	fmt.Fprint(guestConn, "hello")
//	buf := make([]byte, 5)
//	io.ReadFull(hostConn, buf)
func (f *FakeDriver) GuestConn(id domain.SandboxID) net.Conn {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.guestConns[id]
}

// Calls returns a copy of the ordered call log. Each entry records which
// method was invoked and against which SandboxID. Tests can use this to
// assert ordering — for example, that Observe was called before any stored
// state was consulted.
func (f *FakeDriver) Calls() []Call {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// ResetCalls clears the call log.
func (f *FakeDriver) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = f.calls[:0]
}

func (f *FakeDriver) recordCall(kind CallKind, id domain.SandboxID) {
	// Must be called with f.mu held (write).
	f.calls = append(f.calls, Call{Kind: kind, ID: id})
}

// --- driver.Driver ---

// Name returns "fake".
func (f *FakeDriver) Name() string { return "fake" }

// Observe returns the current run state of the sandbox.
//
// If an observation error was injected via [FakeDriver.SetObserveError], it
// returns driver.Unknown plus that error regardless of actual state.
//
// If indeterminate mode was set via [FakeDriver.SetIndeterminate], it returns
// driver.Unknown with a nil error — simulating a driver that successfully
// queried the substrate but cannot determine VM state.
func (f *FakeDriver) Observe(_ context.Context, id domain.SandboxID) (driver.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCall(CallObserve, id)

	if f.observeErr != nil {
		return driver.Observation{
			State:  driver.Unknown,
			Detail: fmt.Sprintf("injected observe error: %v", f.observeErr),
		}, f.observeErr
	}

	if f.indeterminate {
		return driver.Observation{
			State:  driver.Unknown,
			Detail: "indeterminate: driver queried substrate but cannot determine VM state",
		}, nil
	}

	rec, ok := f.vms[id]
	if !ok {
		return driver.Observation{
			State:  driver.Absent,
			Detail: "no VM record",
		}, nil
	}
	return driver.Observation{
		State:      rec.state,
		InstanceID: rec.instanceID,
		Detail:     fmt.Sprintf("state=%s", rec.state),
	}, nil
}

// Start launches a new VM for the given request. If a start error was
// injected via [FakeDriver.SetStartError], it returns that error without
// changing state.
func (f *FakeDriver) Start(_ context.Context, req driver.StartRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCall(CallStart, req.SandboxID)

	if f.startErr != nil {
		return "", f.startErr
	}

	iid := newInstanceID()
	f.vms[req.SandboxID] = &vmRecord{state: driver.Running, instanceID: iid}
	return iid, nil
}

// Stop terminates the VM identified by id. Stop is idempotent: stopping a
// VM that is already absent is not an error. If a stop error was injected via
// [FakeDriver.SetStopError], it returns that error without changing state.
func (f *FakeDriver) Stop(_ context.Context, id domain.SandboxID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCall(CallStop, id)

	if f.stopErr != nil {
		return f.stopErr
	}

	delete(f.vms, id)
	return nil
}

// --- driver.PauseResumer ---

// Pause suspends the VM identified by id, leaving its memory intact.
func (f *FakeDriver) Pause(_ context.Context, id domain.SandboxID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCall(CallPause, id)

	if f.pauseErr != nil {
		return f.pauseErr
	}

	rec := f.vms[id]
	if rec == nil {
		return fmt.Errorf("fake: pause: no VM for %s", id)
	}
	rec.state = driver.Paused
	return nil
}

// Resume restarts execution of a paused VM.
func (f *FakeDriver) Resume(_ context.Context, id domain.SandboxID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCall(CallResume, id)

	if f.resumeErr != nil {
		return f.resumeErr
	}

	rec := f.vms[id]
	if rec == nil {
		return fmt.Errorf("fake: resume: no VM for %s", id)
	}
	rec.state = driver.Running
	return nil
}

// --- driver.Snapshotter ---

// TakeSnapshot creates an in-memory snapshot of the sandbox identified by id.
// The sandbox state is unchanged (pure snapshot, no VM modification).
// Returns a synthetic Snapshot with a fake payload size of 8 bytes.
func (f *FakeDriver) TakeSnapshot(_ context.Context, id domain.SandboxID, kind artifact.SnapshotKind) (artifact.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCall(CallTakeSnapshot, id)

	if f.snapshotErr != nil {
		return artifact.Snapshot{}, f.snapshotErr
	}

	snap := artifact.Snapshot{
		ID:           artifact.SnapshotID(newInstanceID()),
		SandboxID:    id,
		Kind:         kind,
		Size:         8, // synthetic 8-byte payload for in-memory fakes
		CommitMarker: "committed",
		CreatedAt:    time.Now(),
	}
	f.snapshots[snap.ID] = snap
	return snap, nil
}

// --- driver.Forker ---

// ForkFrom creates child VMs from snap, one per entry in childIDs. Each child
// is placed in Running state with a fresh instance ID. The parent sandbox
// (snap.SandboxID) is not modified — this is pure child-creation (edge 5:
// ∅→running, parent unaffected).
func (f *FakeDriver) ForkFrom(_ context.Context, snap artifact.Snapshot, childIDs []domain.SandboxID) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCall(CallForkFrom, snap.SandboxID)

	if f.forkErr != nil {
		return nil, f.forkErr
	}

	instanceIDs := make([]string, len(childIDs))
	for i, id := range childIDs {
		iid := newInstanceID()
		// Pure child-creation: write only the children, never touch the parent.
		f.vms[id] = &vmRecord{state: driver.Running, instanceID: iid}
		instanceIDs[i] = iid
	}
	return instanceIDs, nil
}

// --- driver.SnapshotRemover ---

// RemoveSnapshot deletes the in-memory snapshot record for id and, when
// SetSnapshotDir has been called, removes the simulated CH memory-image
// directory at <snapshotDir>/<id>/. It is idempotent.
//
// Implements driver.SnapshotRemover.
func (f *FakeDriver) RemoveSnapshot(id artifact.SnapshotID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.snapshots, id)
	if f.snapshotDir != "" {
		if err := os.RemoveAll(filepath.Join(f.snapshotDir, string(id))); err != nil {
			return fmt.Errorf("fake: remove snapshot dir %s: %w", id, err)
		}
	}
	return nil
}

// --- driver.GuestDialer ---

// DialGuest implements [driver.GuestDialer]. It creates an in-memory
// [net.Pipe] and returns the host-side end. The guest-side end is stored
// internally and retrievable via [FakeDriver.GuestConn], enabling tests to
// send data from the simulated guest without a real VM.
//
// If a dial error was injected via [FakeDriver.SetDialGuestError], that
// error is returned and no pipe is created.
func (f *FakeDriver) DialGuest(_ context.Context, id domain.SandboxID, _ uint32) (net.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCall(CallDialGuest, id)

	if f.dialGuestErr != nil {
		return nil, f.dialGuestErr
	}

	hostConn, guestConn := net.Pipe()
	// Replace any previous guest conn for this sandbox (old conn is not
	// closed here — the test is responsible for managing lifetimes).
	f.guestConns[id] = guestConn
	return hostConn, nil
}

// --- helpers ---

// newInstanceID generates a random hex string suitable for use as an
// InstanceID.
func newInstanceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("fake: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Compile-time interface assertions.
var (
	_ driver.Driver          = (*FakeDriver)(nil)
	_ driver.PauseResumer    = (*FakeDriver)(nil)
	_ driver.GuestDialer     = (*FakeDriver)(nil)
	_ driver.Snapshotter     = (*FakeDriver)(nil)
	_ driver.Forker          = (*FakeDriver)(nil)
	_ driver.SnapshotRemover = (*FakeDriver)(nil)
)
