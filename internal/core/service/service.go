// Package service provides the sandbox management service layer for nexus3.
//
// It coordinates the store, the driver, and the lifecycle machine without
// exposing any presentation concerns. It is usable as a library by the CLI,
// tests, or any future caller.
//
// # State is a cache
//
// The State field in a sandbox record is a cache. The substrate (VMM) is
// authoritative. All state changes go through the lifecycle Machine to
// enforce the transition table. Transitions are validated twice:
//
//  1. Against the resolved record before touching the driver (fast-path
//     rejection before any I/O).
//  2. Inside the Update callback against the freshly-locked record to guard
//     against concurrent CLI invocations that changed state between resolve
//     and update.
//
// # Envelope immutability
//
// The Envelope is frozen at Create time and must never be mutated
// afterwards. Create writes it once; no other method touches it. The
// substrate reads it (for image digest, etc.) but never writes back.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netstack"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ErrNoSubstrate is returned when an operation requires a hypervisor driver
// that is not compiled into this build or available in the current environment.
// Callers should use errors.Is to detect it and surface a "no_substrate" code
// to the machine contract layer.
var ErrNoSubstrate = errors.New("service: no substrate configured")

// ErrNoArtifactStore is returned when an operation requires an artifact store
// but none is attached via WithArtifacts.
var ErrNoArtifactStore = errors.New("service: no artifact store attached")

// sandboxDeregistrar is the interface type-asserted from AgentCredSource in
// create.go when a credential source supports sandbox lifecycle events.
// Defined here (same package as create.go) so both files share the type without
// exporting it.
type sandboxDeregistrar interface {
	Deregister(domain.SandboxID)
}

// Service coordinates sandbox operations across the store, driver, and
// lifecycle machine. It has no CLI or presentation concerns and is safe to
// call from any context.
type Service struct {
	store     store.Store
	driver    driver.Driver
	machine   lifecycle.Machine
	artifacts *artifact.Store // optional; nil means no artifact persistence
	diskDir   string          // durable dir for per-sandbox ext4 copies (S-COW); empty = defaultDiskDir()

	// perimeter fields — all optional; nil means no egress enforcement.
	broker   *cred.Broker // host-side credential store for MITM token swap
	caSeeder GuestSeeder  // delivers the MITM CA cert into the guest trust store

	supervisorsMu sync.Mutex
	supervisors   map[domain.SandboxID]*perimeter.PerimeterSupervisor

	// deregistrars holds per-sandbox credential deregistration hooks enrolled
	// via RegisterCredDeregistrar or automatically by create.go when the
	// AgentCredSource satisfies sandboxDeregistrar. Remove drains this map via
	// dropDeregistrar so the hook's Deregister is called and broker scopes are
	// revoked; a cleaned-up Refresher will not accumulate PushErrors for a
	// dead sandbox.
	deregistrarsMu sync.Mutex
	deregistrars   map[domain.SandboxID]sandboxDeregistrar
}

// New returns a Service backed by the given store, driver, and machine.
func New(st store.Store, drv driver.Driver, m lifecycle.Machine) *Service {
	return &Service{store: st, driver: drv, machine: m}
}

// WithArtifacts attaches an artifact store and returns the receiver so calls
// can be chained. The store is used by snapshot list and remove operations
// (S1). Service.Snapshot no longer writes to this store — the driver's
// canonical store (rooted at its durable SnapshotDir) is the single writer.
// WithArtifacts does not alter any existing method and does not affect the
// New constructor signature.
func (s *Service) WithArtifacts(a *artifact.Store) *Service {
	s.artifacts = a
	return s
}

// WithBroker attaches a credential broker for MITM token-swap and enables
// the perimeter supervisor on sandbox Start. When set alongside a driver that
// implements driver.NetworkHook, every sandbox Start call assembles a
// [perimeter.PerimeterSupervisor] and stops it on Stop/Remove.
//
// WithBroker does not alter any existing method or the New constructor.
func (s *Service) WithBroker(b *cred.Broker) *Service {
	s.broker = b
	return s
}

// WithCASeeder attaches a [GuestSeeder] that delivers the MITM proxy CA
// certificate into the guest trust store at sandbox start time. The seeder is
// called only when a supervisor was assembled (driver implements NetworkHook
// and a broker is attached). Typically constructed via [NewAgentCACopySeeder].
//
// Trust-store gap: writing the cert file alone is not enough; the guest must
// also run update-ca-certificates (or equivalent). That step is deferred to a
// future slice.
func (s *Service) WithCASeeder(seeder GuestSeeder) *Service {
	s.caSeeder = seeder
	return s
}

// WithDiskDir sets the directory where per-sandbox ext4 disk copies are
// created by CreateAndBoot and reaped by Remove. When not set, Remove falls
// back to defaultDiskDir() which mirrors the store's durable root
// (store.DefaultRoot()/disks). Tests set this to t.TempDir() so copies stay
// inside the test filesystem tree and are cleaned up automatically.
func (s *Service) WithDiskDir(dir string) *Service {
	s.diskDir = dir
	return s
}

// CreateOptions carries options for Create.
type CreateOptions struct {
	// RemoveOnExit records the --rm intent durably at creation time.
	// When true, the sandbox is removed when its primary command exits.
	RemoveOnExit bool
}

// Create mints a new sandbox record in state Created.
//
// The Envelope is frozen at creation time. No method on Service ever mutates
// it after Create returns; callers must treat it as immutable.
//
// Returns an error wrapping store.ErrAlreadyExists if a sandbox with the same
// project/name handle already exists.
func (s *Service) Create(ctx context.Context, project, name string, opts CreateOptions) (domain.Sandbox, error) {
	handle := project + "/" + name

	// Guard against duplicate handles before minting a new ID.
	// Note: there is a TOCTOU window between this check and the Create call in
	// concurrent invocations. A future slice can close it by having the store
	// enforce a unique-handle constraint. For now, sequential callers get a
	// clean error.
	_, err := s.store.ResolveByHandle(ctx, handle)
	if err == nil {
		return domain.Sandbox{}, fmt.Errorf("sandbox %q already exists: %w", handle, store.ErrAlreadyExists)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return domain.Sandbox{}, fmt.Errorf("service: create: check handle %q: %w", handle, err)
	}

	sb := domain.Sandbox{
		ID:           domain.NewSandboxID(),
		Name:         name,
		Project:      project,
		State:        domain.Created,
		Envelope:     domain.Envelope{}, // frozen at creation; future slices populate fields
		RemoveOnExit: opts.RemoveOnExit,
	}
	if err := s.store.Create(ctx, sb); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: create: %w", err)
	}
	return sb, nil
}

// List returns all sandboxes from the store. The returned slice is always
// non-nil (an empty store returns []domain.Sandbox{}, never nil), so callers
// and JSON marshallers see [] rather than null.
func (s *Service) List(ctx context.Context) ([]domain.Sandbox, error) {
	all, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: list: %w", err)
	}
	if all == nil {
		all = []domain.Sandbox{}
	}
	return all, nil
}

// GetByMotive returns all sandboxes associated with the given motive ID.
// Delegates directly to the store; an unknown or empty motive returns an
// empty (non-nil) slice and nil error.
func (s *Service) GetByMotive(ctx context.Context, motiveID string) ([]domain.Sandbox, error) {
	return s.store.GetByMotive(ctx, motiveID)
}

// resolve finds a sandbox by handle ("<project>/<name>"), exact ID, or ID
// prefix. It propagates domain.ErrAmbiguous (which names all candidates) when
// the prefix matches more than one sandbox.
func (s *Service) resolve(ctx context.Context, ref string) (domain.Sandbox, error) {
	// Handle form: "<project>/<name>" — must contain a slash.
	if strings.Contains(ref, "/") {
		sb, err := s.store.ResolveByHandle(ctx, ref)
		if err != nil {
			return domain.Sandbox{}, fmt.Errorf("resolve %q: %w", ref, err)
		}
		return sb, nil
	}

	// Exact ID: a full 29-character Crockford string starting with "sb-".
	if id, err := domain.ParseSandboxID(ref); err == nil {
		sb, err := s.store.Get(ctx, id)
		if err != nil {
			return domain.Sandbox{}, fmt.Errorf("resolve %q: %w", ref, err)
		}
		return sb, nil
	}

	// Prefix: fall through to the store's prefix resolver.
	sb, err := s.store.ResolveByPrefix(ctx, ref)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("resolve %q: %w", ref, err)
	}
	return sb, nil
}

// Start starts the sandbox identified by ref. ref may be an exact ID, an ID
// prefix, or a "<project>/<name>" handle. The sandbox must be in state
// Created or Stopped.
func (s *Service) Start(ctx context.Context, ref string) (domain.Sandbox, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return domain.Sandbox{}, err
	}

	// Fast-path validation against the cached state. This is an optimistic
	// check only; the authoritative validation happens inside Update below.
	if _, err := s.machine.Next(sb.State, lifecycle.TriggerStart); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: start %s: %w", sb.ID, err)
	}

	// driver.Start is called inside store.Update so that the substrate call and
	// the record write are guarded by the same per-sandbox exclusive flock.
	//
	// Without this, two concurrent Start invocations can both pass the fast-path
	// check and both call driver.Start, spawning two VMs. The second Update
	// callback rejects the record write (re-validation sees state already Running),
	// but the second VM is already running as an orphan that nothing can reconcile.
	//
	// Trade-off: the flock is held across a potentially slow substrate call, so a
	// hung VMM blocks other operations on this sandbox only — never the entire
	// system. A blocked operation is recoverable; an orphaned VM is not.
	var updated domain.Sandbox
	if err := s.store.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		// Re-validate inside the lock against the authoritative record.
		// If state moved concurrently, reject before touching the substrate.
		tr, err := s.machine.Next(rec.State, lifecycle.TriggerStart)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		// Guard: a concurrent Remove may have set the removal marker between the
		// fast-path check above and this lock acquisition. A sandbox with the
		// marker set is being deleted; launching a VM now would produce an orphan.
		if rec.RemovalMarker {
			return fmt.Errorf("re-validate: %w: sandbox is marked for removal", store.ErrNotFound)
		}
		instanceID, err := s.driver.Start(ctx, driver.StartRequest{
			SandboxID:   rec.ID,
			ImageDigest: rec.Envelope.ImageDigest,
		})
		if err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		rec.State = tr.NextState
		rec.InstanceID = instanceID
		rec.StopReason = "" // cleared: sandbox is running; StopReason only qualifies stopped
		updated = *rec
		return nil
	}); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: start %s: %w", sb.ID, err)
	}

	// Assemble the perimeter supervisor outside the store lock (I/O must not
	// be performed while the per-sandbox flock is held).
	//
	// Conditions: the driver must implement driver.NetworkHook and a credential
	// broker must be attached. An absent broker means the operator has not
	// enabled egress enforcement; the supervisor is skipped silently.
	if hook, ok := s.driver.(driver.NetworkHook); ok && s.broker != nil {
		if err := s.startSupervisor(ctx, hook, updated); err != nil {
			return domain.Sandbox{}, fmt.Errorf("service: start %s: perimeter: %w", updated.ID, err)
		}
	}

	return updated, nil
}

// Stop stops the running sandbox identified by ref.
func (s *Service) Stop(ctx context.Context, ref string) (domain.Sandbox, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return domain.Sandbox{}, err
	}

	// Fast-path validation against the cached state.
	if _, err := s.machine.Next(sb.State, lifecycle.TriggerStop); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: stop %s: %w", sb.ID, err)
	}

	// driver.Stop is called inside store.Update for the same reason as Start:
	// the substrate call and the record write must be guarded by the same lock.
	var updated domain.Sandbox
	if err := s.store.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		tr, err := s.machine.Next(rec.State, lifecycle.TriggerStop)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		if err := s.driver.Stop(ctx, rec.ID); err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		rec.State = tr.NextState
		rec.InstanceID = ""
		rec.StopReason = domain.StopReasonClean // user-requested clean stop
		updated = *rec
		return nil
	}); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: stop %s: %w", sb.ID, err)
	}

	// Close the perimeter supervisor outside the lock (same reason as Start).
	s.closeSupervisor(updated.ID)

	return updated, nil
}

// Pause pauses the running sandbox identified by ref. Requires the driver to
// implement the driver.PauseResumer optional capability.
func (s *Service) Pause(ctx context.Context, ref string) (domain.Sandbox, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return domain.Sandbox{}, err
	}

	if _, err := s.machine.Next(sb.State, lifecycle.TriggerPause); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: pause %s: %w", sb.ID, err)
	}

	// Capability check is outside the lock: it is a type assertion with no I/O
	// and it fails fast without contending on the per-sandbox flock.
	pr, ok := s.driver.(driver.PauseResumer)
	if !ok {
		return domain.Sandbox{}, fmt.Errorf(
			"service: pause %s: driver %q does not support pause/resume: %w",
			sb.ID, s.driver.Name(), ErrNoSubstrate,
		)
	}

	var updated domain.Sandbox
	if err := s.store.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		tr, err := s.machine.Next(rec.State, lifecycle.TriggerPause)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		if err := pr.Pause(ctx, rec.ID); err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		rec.State = tr.NextState
		updated = *rec
		return nil
	}); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: pause %s: %w", sb.ID, err)
	}
	return updated, nil
}

// Resume resumes the paused sandbox identified by ref. Requires the driver to
// implement the driver.PauseResumer optional capability.
func (s *Service) Resume(ctx context.Context, ref string) (domain.Sandbox, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return domain.Sandbox{}, err
	}

	if _, err := s.machine.Next(sb.State, lifecycle.TriggerResume); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: resume %s: %w", sb.ID, err)
	}

	// Capability check is outside the lock: it is a type assertion with no I/O
	// and it fails fast without contending on the per-sandbox flock.
	pr, ok := s.driver.(driver.PauseResumer)
	if !ok {
		return domain.Sandbox{}, fmt.Errorf(
			"service: resume %s: driver %q does not support pause/resume: %w",
			sb.ID, s.driver.Name(), ErrNoSubstrate,
		)
	}

	var updated domain.Sandbox
	if err := s.store.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		tr, err := s.machine.Next(rec.State, lifecycle.TriggerResume)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		if err := pr.Resume(ctx, rec.ID); err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		rec.State = tr.NextState
		updated = *rec
		return nil
	}); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: resume %s: %w", sb.ID, err)
	}
	return updated, nil
}

// Remove deletes the sandbox identified by ref.
//
// Write-ahead removal protocol (crash-safety and concurrency guarantee):
//
//  1. SetRemovalMarker — durable marker written BEFORE any destructive work.
//     If the process crashes after this step, the marker stays set and
//     recovery treats the sandbox as terminal: removal is never retried.
//
//  2. driver.Stop — terminates the VM, called inside the per-sandbox exclusive
//     flock (via store.Update). Running Stop inside the lock prevents the
//     concurrent-Start orphan race: a Start that acquires the lock after step 1
//     will see RemovalMarker=true in its re-validation and be rejected before
//     launching a VM. Stop is idempotent; an absent VM is not an error.
//     No ClearRemovalMarker on failure — the marker is intentionally left set
//     so recovery can detect interrupted removals.
//
//  3. store.Delete — removes the record. Because Delete removes the whole
//     record (including the embedded marker), no ClearRemovalMarker call is
//     needed; calling it would be wrong — it could resurrect a half-destroyed
//     sandbox and break the terminal-removal guarantee.
func (s *Service) Remove(ctx context.Context, ref string) error {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return err
	}

	// Step 1: write-ahead removal marker. Must precede all destructive work.
	if err := s.store.SetRemovalMarker(ctx, sb.ID); err != nil {
		return fmt.Errorf("service: remove %s: set removal marker: %w", sb.ID, err)
	}

	// Step 2: terminate the VM inside the per-sandbox exclusive lock.
	// The lock prevents a concurrent Start from launching a VM between the
	// marker write (step 1) and the record deletion (step 3): a Start blocked
	// on this lock will read the marker and be rejected at re-validation.
	// Stop is idempotent; an absent VM is not an error.
	if err := s.store.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		if err := s.driver.Stop(ctx, rec.ID); err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("service: remove %s: stop vm: %w", sb.ID, err)
	}

	// Close the perimeter supervisor (if any) after the VM is gone and before
	// the record is deleted. Idempotent — a stopped sandbox has no supervisor.
	s.closeSupervisor(sb.ID)

	// Deregister any credential rotation hook enrolled for this sandbox and
	// revoke broker scopes. Both are terminal-safe: Remove is never retried
	// (write-ahead marker stays set on crash), so revocation is permanent.
	//
	// dropDeregistrar: notifies the hook (e.g. *cred.Refresher) so it stops
	// pushing SetRealToken for a sandbox that no longer exists, avoiding
	// PushErrors accumulation.
	//
	// broker.Revoke: removes the (sandbox, host) scope so that any still-
	// registered Refresher gets a discriminating error on the next rotation
	// push, making the lifecycle fix observable in tests.
	s.dropDeregistrar(sb.ID)
	if s.broker != nil {
		for _, h := range sb.Envelope.AllowedHosts {
			s.broker.Revoke(sb.ID, h)
		}
	}

	// Step 3: delete the record. The marker is destroyed with it; no
	// ClearRemovalMarker call is needed or correct here.
	if err := s.store.Delete(ctx, sb.ID); err != nil {
		return fmt.Errorf("service: remove %s: delete record: %w", sb.ID, err)
	}

	// Step 4: reap the per-sandbox ext4 disk copy created by CreateAndBoot
	// (S-COW). Delegates to the shared helper so Service.Remove and the
	// recovery --rm path cannot drift. Idempotent — missing file is not an
	// error.
	_ = ReapDiskCopy(s.diskDir, sb.ID)

	return nil
}

// startSupervisor assembles a PerimeterSupervisor for the running sandbox and
// stores it. Called by Start after the store lock is released.
func (s *Service) startSupervisor(ctx context.Context, hook driver.NetworkHook, sb domain.Sandbox) error {
	fd, err := hook.GuestNetworkFD(ctx, sb.ID)
	if err != nil {
		return fmt.Errorf("guest network fd: %w", err)
	}

	al, err := netfilter.NewAllowList(nil, nil, sb.Envelope.AllowedHosts)
	if err != nil {
		fd.Close()
		return fmt.Errorf("allow list: %w", err)
	}

	stack := netstack.New(al, nil) // onAudit: nil (discards events; a future slice wires observability)

	proxy, err := mitm.New(mitm.Config{
		SandboxID:    sb.ID,
		AllowedHosts: sb.Envelope.AllowedHosts,
		Broker:       s.broker,
	})
	if err != nil {
		fd.Close()
		al.Stop()
		return fmt.Errorf("mitm proxy: %w", err)
	}

	// Detach from the request context so the supervisor's goroutines survive
	// after the Start RPC returns and its context is cancelled.  The caller's
	// ctx is still used for the request-scoped I/O above (GuestNetworkFD).
	sup, err := perimeter.Start(context.WithoutCancel(ctx), sb.ID, fd, stack, proxy, al)
	if err != nil {
		return fmt.Errorf("supervisor: %w", err)
	}

	s.supervisorsMu.Lock()
	if s.supervisors == nil {
		s.supervisors = make(map[domain.SandboxID]*perimeter.PerimeterSupervisor)
	}
	s.supervisors[sb.ID] = sup
	s.supervisorsMu.Unlock()

	// Seed the CA certificate into the guest trust store when a seeder is
	// attached. Non-fatal: the supervisor still protects egress even when the
	// guest has not imported the CA (HTTPS clients will see validation errors,
	// but plain HTTP and already-trusted HTTPS still flow through the proxy).
	if s.caSeeder != nil {
		if err := SeedCA(ctx, sup.CACert(), sb.ID, s.caSeeder); err != nil {
			// Best-effort: log but do not fail Start. Trust-store seeding is
			// expected to fail until the agent bootstrap slice lands (future).
			_ = err // TODO(S6): surface via structured log
		}
	}

	return nil
}

// closeSupervisor looks up and closes the supervisor for id, removing it from
// the map. Safe to call when no supervisor exists (no-op). Idempotent.
func (s *Service) closeSupervisor(id domain.SandboxID) {
	s.supervisorsMu.Lock()
	sup, ok := s.supervisors[id]
	if ok {
		delete(s.supervisors, id)
	}
	s.supervisorsMu.Unlock()
	if ok {
		_ = sup.Close()
	}
}

// storeDeregistrar records a deregistration hook for id. Lazy map init mirrors
// the supervisors pattern. Called by create.go when AgentCredSource satisfies
// sandboxDeregistrar, and by RegisterCredDeregistrar for manual-seeding callers.
func (s *Service) storeDeregistrar(id domain.SandboxID, d sandboxDeregistrar) {
	s.deregistrarsMu.Lock()
	if s.deregistrars == nil {
		s.deregistrars = make(map[domain.SandboxID]sandboxDeregistrar)
	}
	s.deregistrars[id] = d
	s.deregistrarsMu.Unlock()
}

// dropDeregistrar looks up the hook for id, calls Deregister, and removes the
// entry. Safe when no hook exists (no-op). Idempotent.
func (s *Service) dropDeregistrar(id domain.SandboxID) {
	s.deregistrarsMu.Lock()
	d, ok := s.deregistrars[id]
	if ok {
		delete(s.deregistrars, id)
	}
	s.deregistrarsMu.Unlock()
	if ok {
		d.Deregister(id)
	}
}

// RegisterCredDeregistrar enrolls a credential deregistration hook for id.
// Call this when using manual seeding (SeedGuestAgent + broker.SetRealToken
// outside CreateAndBoot) so that Remove still calls Deregister and prevents the
// hook from accumulating PushErrors for a sandbox that no longer exists.
//
// The hook must implement Deregister(domain.SandboxID); *cred.Refresher
// satisfies this structurally. Passing a nil hook is a no-op.
func (s *Service) RegisterCredDeregistrar(id domain.SandboxID, d interface {
	Deregister(domain.SandboxID)
}) {
	if d == nil {
		return
	}
	s.storeDeregistrar(id, d)
}

// Snapshot takes a point-in-time snapshot of the sandbox identified by ref,
// leaving the sandbox in its current state (self-edge: running→running or
// stopped→stopped). Returns the resulting artifact.Snapshot.
//
// The driver must implement [driver.Snapshotter]. If it does not, an error
// wrapping [ErrNoSubstrate] is returned.
//
// If an artifact store is attached via [WithArtifacts], the snapshot is
// persisted there after the driver succeeds. On-disk integrity is guaranteed
// by the artifact store's commit-marker protocol.
func (s *Service) Snapshot(ctx context.Context, ref string) (artifact.Snapshot, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return artifact.Snapshot{}, err
	}

	// Fast-path validation against the cached state. TriggerSnapshot is valid
	// only from Running and Stopped; all other states are rejected here before
	// any I/O. The authoritative re-validation inside Update guards concurrent
	// state changes between this check and lock acquisition.
	if _, err := s.machine.Next(sb.State, lifecycle.TriggerSnapshot); err != nil {
		return artifact.Snapshot{}, fmt.Errorf("service: snapshot %s: %w", sb.ID, err)
	}

	// Capability check is outside the lock: a type assertion has no I/O and
	// fails fast without contending on the per-sandbox flock.
	snapper, ok := s.driver.(driver.Snapshotter)
	if !ok {
		return artifact.Snapshot{}, fmt.Errorf(
			"service: snapshot %s: driver %q does not support snapshots: %w",
			sb.ID, s.driver.Name(), ErrNoSubstrate,
		)
	}

	// TakeSnapshot is called inside store.Update to hold the per-sandbox
	// exclusive flock for the duration of the operation, mirroring
	// Start/Stop/Pause/Resume. TriggerSnapshot is a self-edge (running→running,
	// stopped→stopped), so rec.State is rewritten with the same value — a
	// no-op for state, but the lock prevents concurrent Remove from racing.
	var snap artifact.Snapshot
	if err := s.store.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		tr, err := s.machine.Next(rec.State, lifecycle.TriggerSnapshot)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		localSnap, err := snapper.TakeSnapshot(ctx, rec.ID, artifact.KindRetained)
		if err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		if err := localSnap.Validate(); err != nil {
			return fmt.Errorf("artifact integrity: %w", err)
		}
		rec.State = tr.NextState // self-edge: same state as before
		snap = localSnap
		return nil
	}); err != nil {
		return artifact.Snapshot{}, fmt.Errorf("service: snapshot %s: %w", sb.ID, err)
	}

	return snap, nil
}

// Fork creates count child sandboxes from the sandbox identified by ref.
// The parent sandbox is unaffected — fork is pure child-creation (spec 06,
// edge 5: ∅→running). Returns the newly created children, all in Running state.
//
// The driver must implement [driver.Snapshotter] and [driver.Forker]. If
// either is absent, an error wrapping [ErrNoSubstrate] is returned.
//
// TriggerFork has no transition-table entry for any parent state (the parent
// is unchanged, not transitioned). Instead, Fork validates that the parent can
// be snapshotted (TriggerSnapshot must be legal) as a prerequisite, giving a
// clear error when the parent is in Created, Paused, or Error state.
func (s *Service) Fork(ctx context.Context, ref string, count int) ([]domain.Sandbox, error) {
	if count < 1 {
		return nil, fmt.Errorf("service: fork: count must be >= 1, got %d", count)
	}

	parent, err := s.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}

	// Snapshot-eligibility check: a parent in Created/Paused/Error cannot be
	// forked because there is no quiescent memory state to snapshot.
	if _, err := s.machine.Next(parent.State, lifecycle.TriggerSnapshot); err != nil {
		return nil, fmt.Errorf("service: fork %s: parent not snapshotable: %w", parent.ID, err)
	}

	// Capability checks outside the lock: type assertions have no I/O.
	snapper, ok := s.driver.(driver.Snapshotter)
	if !ok {
		return nil, fmt.Errorf(
			"service: fork %s: driver %q does not support snapshots: %w",
			parent.ID, s.driver.Name(), ErrNoSubstrate,
		)
	}
	forker, ok := s.driver.(driver.Forker)
	if !ok {
		return nil, fmt.Errorf(
			"service: fork %s: driver %q does not support fork: %w",
			parent.ID, s.driver.Name(), ErrNoSubstrate,
		)
	}

	// Take a transient snapshot of the parent. Fork does not hold the parent
	// lease because the parent state is unchanged (TriggerFork has no table
	// entry; child creation uses edge 5: ∅→running).
	snap, err := snapper.TakeSnapshot(ctx, parent.ID, artifact.KindTransient)
	if err != nil {
		return nil, fmt.Errorf("service: fork %s: snapshot: %w", parent.ID, err)
	}
	if err := snap.Validate(); err != nil {
		return nil, fmt.Errorf("service: fork %s: snapshot integrity: %w", parent.ID, err)
	}

	// Mint child IDs. Each ID is a UUIDv7 and is globally unique; using the
	// full base32 tail for the child name guarantees collision-free names even
	// when many children are minted in the same millisecond.
	childIDs := make([]domain.SandboxID, count)
	for i := range childIDs {
		childIDs[i] = domain.NewSandboxID()
	}

	// Spawn all children from the snapshot in one driver call.
	instanceIDs, err := forker.ForkFrom(ctx, snap, childIDs)
	if err != nil {
		return nil, fmt.Errorf("service: fork %s: driver: %w", parent.ID, err)
	}

	// Persist each child record. Children start directly in Running state
	// (edge 5: ∅→running); there is no ∅ state in the domain model so we
	// use store.Create (not store.Update) with State already set to Running.
	children := make([]domain.Sandbox, 0, count)
	for i, id := range childIDs {
		child := domain.Sandbox{
			ID:         id,
			Name:       fmt.Sprintf("fork-%s", id.String()[3:]),
			Project:    parent.Project,
			State:      domain.Running,
			InstanceID: instanceIDs[i],
			Provenance: &domain.Provenance{
				ParentID:       parent.ID,
				SourceSnapshot: string(snap.ID),
			},
		}
		if err := s.store.Create(ctx, child); err != nil {
			return nil, fmt.Errorf("service: fork %s: persist child %s: %w", parent.ID, id, err)
		}
		children = append(children, child)
	}

	return children, nil
}

// SnapshotList returns all valid snapshots from the attached artifact store,
// sorted by creation time (oldest first). Returns nil if no artifact store
// is attached (WithArtifacts was never called).
func (s *Service) SnapshotList() ([]artifact.Snapshot, error) {
	if s.artifacts == nil {
		return nil, nil
	}
	return s.artifacts.List()
}

// SnapshotRemove removes the snapshot identified by id from the attached
// artifact store. It refuses with an error if any sandbox has
// Provenance.SourceSnapshot == id (the snapshot is still in use as a fork
// source). It is a no-op (idempotent) if the snapshot does not exist.
//
// ErrNoArtifactStore is returned if no artifact store is attached.
func (s *Service) SnapshotRemove(ctx context.Context, id artifact.SnapshotID) error {
	if s.artifacts == nil {
		return fmt.Errorf("service: snapshot rm %s: %w", id, ErrNoArtifactStore)
	}

	// Refuse if any sandbox still references this snapshot as a fork source.
	all, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("service: snapshot rm %s: list sandboxes: %w", id, err)
	}
	for _, sb := range all {
		if sb.Provenance != nil && sb.Provenance.SourceSnapshot == string(id) {
			return fmt.Errorf(
				"service: snapshot rm %s: snapshot is referenced by sandbox %s (%s/%s); remove the sandbox first",
				id, sb.ID, sb.Project, sb.Name,
			)
		}
	}

	// Route through driver.SnapshotRemover (if available) to also remove
	// driver-managed files (e.g. the CH memory-image directory). The driver
	// method removes both its own store record and the CH files directory,
	// mirroring reapTransientSnapshot. The trailing s.artifacts.Remove is an
	// idempotent second pass — artifact.Store.Remove returns nil for a
	// non-existent snapshot — kept so that drivers without SnapshotRemover
	// still remove the artifact record, and so that the two Store objects
	// (which share the same on-disk root in production) converge correctly.
	if remover, ok := s.driver.(driver.SnapshotRemover); ok {
		if err := remover.RemoveSnapshot(id); err != nil {
			return fmt.Errorf("service: snapshot rm %s: driver: %w", id, err)
		}
	}

	return s.artifacts.Remove(id)
}

// RestoreFromSnapshot creates count child sandboxes from a retained snapshot
// identified by snapID. It is the fan-out sibling of Fork: instead of taking
// a fresh transient snapshot of a live sandbox, it reads an existing retained
// snapshot from the artifact store and fans out N children (edge 5: ∅→running,
// per spec doc-06). The origin sandbox is not touched.
//
// The driver must implement [driver.Forker]. If it does not, an error wrapping
// [ErrNoSubstrate] is returned.
//
// An artifact store must be attached via [WithArtifacts]; if not,
// [ErrNoArtifactStore] is returned. The snapshot is validated for integrity
// before any child is created — a bad or torn snapshot yields a clean error
// with zero children created (S2-AC2; no new error state is produced, no new
// lifecycle edge is traversed).
func (s *Service) RestoreFromSnapshot(ctx context.Context, snapID artifact.SnapshotID, count int) ([]domain.Sandbox, error) {
	if count < 1 {
		return nil, fmt.Errorf("service: restore: count must be >= 1, got %d", count)
	}
	if s.artifacts == nil {
		return nil, fmt.Errorf("service: restore %s: %w", snapID, ErrNoArtifactStore)
	}

	// Read and validate the snapshot BEFORE creating any child (S2-AC2: a bad
	// snapshot yields a clean failure; no children are created, no error state).
	snap, err := s.artifacts.Read(snapID)
	if err != nil {
		return nil, fmt.Errorf("service: restore %s: read snapshot: %w", snapID, err)
	}
	if err := snap.Validate(); err != nil {
		return nil, fmt.Errorf("service: restore %s: snapshot integrity: %w", snapID, err)
	}

	// Capability check outside the lock: type assertion has no I/O.
	forker, ok := s.driver.(driver.Forker)
	if !ok {
		return nil, fmt.Errorf(
			"service: restore %s: driver %q does not support fork: %w",
			snapID, s.driver.Name(), ErrNoSubstrate,
		)
	}

	// Mint child IDs. Each is a UUIDv7; collision-free even in the same ms.
	childIDs := make([]domain.SandboxID, count)
	for i := range childIDs {
		childIDs[i] = domain.NewSandboxID()
	}

	// Spawn all children from the retained snapshot in one driver call.
	instanceIDs, err := forker.ForkFrom(ctx, snap, childIDs)
	if err != nil {
		return nil, fmt.Errorf("service: restore %s: driver: %w", snapID, err)
	}

	// Persist each child record. Children start directly in Running state
	// (edge 5: ∅→running). Provenance.SourceSnapshot satisfies S2-AC3.
	children := make([]domain.Sandbox, 0, count)
	for i, id := range childIDs {
		child := domain.Sandbox{
			ID:         id,
			Name:       fmt.Sprintf("restore-%s", id.String()[3:]),
			State:      domain.Running,
			InstanceID: instanceIDs[i],
			Provenance: &domain.Provenance{
				ParentID:       snap.SandboxID,
				SourceSnapshot: string(snapID),
			},
		}
		if err := s.store.Create(ctx, child); err != nil {
			return nil, fmt.Errorf("service: restore %s: persist child %s: %w", snapID, id, err)
		}
		children = append(children, child)
	}

	return children, nil
}
