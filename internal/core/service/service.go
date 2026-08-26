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
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

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
	"github.com/IniZio/nexus3/internal/core/volumestore"
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
	artifacts *artifact.Store          // optional; nil means no artifact persistence
	diskDir   string                   // durable dir for per-sandbox ext4 copies (S-COW); empty = defaultDiskDir()
	volumes   *volumestore.VolumeStore // optional; nil means no named-volume support (SD2-6-MOUNT)

	// perimeter fields — all optional; nil means no egress enforcement.
	broker    *cred.Broker // host-side credential store for MITM token swap
	caSeeder  GuestSeeder  // delivers the MITM CA cert into the guest trust store
	sshSeeder GuestSeeder  // injects SSH authorized_keys into the guest (ORCA-S1)

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

// WithVolumes attaches a VolumeStore for named-volume operations (SD2-6-MOUNT).
// When set, Service.Remove detaches all MountedVolumes for the removed sandbox
// under the per-volume lock. Service.Snapshot and Service.Fork refuse outright
// when any named volume is attached (D-PD-96, TBR-PD-15) — snapshot-with-volumes
// is not yet supported; see TBR-PD-15 for the design work that will lift this.
func (s *Service) WithVolumes(vs *volumestore.VolumeStore) *Service {
	s.volumes = vs
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

// WithSSHSeeder attaches a [GuestSeeder] that injects SSH authorized_keys into
// the guest at sandbox start and restart. The seeder is called only when the
// sandbox's Envelope.SSHPublicKey is non-empty. Typically constructed via
// [NewAgentSSHKeyCopySeeder].
//
// The injection is best-effort on Start (restart): if the guest agent is not
// yet reachable the error is logged but does not fail Start.
func (s *Service) WithSSHSeeder(seeder GuestSeeder) *Service {
	s.sshSeeder = seeder
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
	// Labels is the arbitrary key=value map stamped onto the sandbox record at
	// creation time. Nil and empty map are equivalent (sandbox carries no labels).
	// Callers set labels via repeatable --label KEY=VALUE flags (D-PD-21).
	Labels map[string]string

	// RemoveOnExit records the --rm intent durably at creation time.
	// When true, the sandbox is removed when its primary command exits.
	RemoveOnExit bool

	// AgentName is the resolved agent profile name (e.g. "claude-code") to
	// stamp on the sandbox record. Empty means no agent profile is attached.
	// Mirrors the AgentName field on domain.Sandbox; the value comes from
	// resolveAgentPosture (flag OR user-global / project config default).
	AgentName string
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
		Labels:       opts.Labels,
		State:        domain.Created,
		Envelope:     domain.Envelope{}, // frozen at creation; future slices populate fields
		RemoveOnExit: opts.RemoveOnExit,
		AgentName:    opts.AgentName,
	}
	if err := s.store.Create(ctx, sb); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: create: %w", err)
	}
	return sb, nil
}

// List returns all user-visible sandboxes from the store. The returned slice
// is always non-nil (an empty store returns []domain.Sandbox{}, never nil),
// so callers and JSON marshallers see [] rather than null.
//
// # __builder filter (UNI-TEARDOWN)
//
// Records with Project == "__builder" are transient: they exist only for the
// duration of an in-VM build and are always deleted on normal exit. They must
// not appear in user-facing listings because:
//
//   - They are implementation-internal (not user-created sandboxes).
//   - If the CLI is SIGKILL'd after record creation but before the supervisor
//     calls svc.Remove (and before the pipe-watchdog has triggered), a stale
//     __builder record would pollute `nexus3 sandbox list`.
//
// The filter here is the last-resort safety net. The primary cleanup mechanism
// is: supervisor's svc.Remove in ephemeral mode + parent-watchdog pipe for
// SIGKILL. Both are belt-and-suspenders; the filter handles any gap.
//
// In addition, reapBuilders is called before filtering to actually delete
// orphan __builder records whose creator process has exited (R-REAP).
func (s *Service) List(ctx context.Context) ([]domain.Sandbox, error) {
	all, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: list: %w", err)
	}
	// Reap stale orphan builder records before filtering (R-REAP).
	s.reapBuilders(ctx, all)
	// Filter transient builder records. Use in-place filtering to avoid alloc.
	out := all[:0]
	for _, sb := range all {
		if sb.Project != "__builder" {
			out = append(out, sb)
		}
	}
	if out == nil {
		out = []domain.Sandbox{}
	}
	return out, nil
}

// reapBuilders deletes __builder sandbox records whose creator process has
// exited. It is called opportunistically from List before the __builder filter
// runs, so stale orphan records (from a SIGKILL'd BuildInVM process) accumulate
// only until the next listing.
//
// # Predicate
//
// A record is reaped if and only if all three conditions hold:
//  1. Project == "__builder"
//  2. CreatorPID != 0 (written by a binary that stamps the creator PID)
//  3. kill(CreatorPID, 0) == ESRCH — the creator process is provably dead
//
// ESRCH ("no such process") is the only safe signal: it means the kernel has
// removed the PID from its table. A live BuildInVM goroutine IS that process,
// so kill(os.Getpid(), 0) always returns nil while the build runs. Only after
// the process exits does the PID become absent and ESRCH is returned.
//
// Records with CreatorPID == 0 (written before this feature) are silently
// skipped; the __builder filter in List still hides them from listings.
//
// # What is cleaned up
//
// Cleanup order mirrors BuildInVM's LIFO defer order — Stop before Delete:
//  1. drv.Stop: terminates any surviving CH process and removes the API socket,
//     vsock socket, .iid sidecar, and tears down the kernel netns. drv.Stop
//     is safe to call when CH is already dead: isAbsent returns immediately
//     after calling clearState (which removes the stale socket files).
//  2. store.Delete: removes the sandbox record directory.
//
// # What is NOT cleaned up
//
// The ephemeral build disk images (ctx.ext4 and artifact.ext4) live in an
// os.MkdirTemp directory created by the CLI before calling BuildInVM. Their
// paths are never written into the sandbox record, so the record is not the
// "handle" to them. Deleting the record does not orphan them further — they
// are already unrecoverable once the SIGKILL'd process's stack dies.
// The os.MkdirTemp directories live in /tmp and are swept by the host OS.
//
// # Trigger timing
//
// Reaping fires on the next call to svc.List — sandbox list, herdr plugin
// list, MCP sandbox listing. Nothing on the sandbox create --file build path
// calls svc.List, so reaping does not fire automatically during a build.
//
// # Concurrency
//
// Concurrent List calls may attempt to Stop and Delete the same record
// simultaneously; store.ErrNotFound is silently swallowed to make this
// idempotent. The double-Stop is also safe: the second call sees an absent
// socket and returns immediately.
func (s *Service) reapBuilders(ctx context.Context, all []domain.Sandbox) {
	for _, sb := range all {
		if sb.Project != "__builder" || sb.CreatorPID == 0 {
			continue
		}
		if err := syscall.Kill(sb.CreatorPID, 0); !errors.Is(err, syscall.ESRCH) {
			continue // process still alive (nil) or uncertain (EPERM) — do not delete
		}
		// Creator is provably dead.
		//
		// Stop the VM and clean driver-side state (sockets, netns).
		// Non-fatal: if CH is already gone the Stop returns nil after clearState.
		// Ignore errors; we proceed to delete the record regardless.
		_ = s.driver.Stop(ctx, sb.ID)

		// Delete the store record. ErrNotFound is harmless — a concurrent
		// reap already deleted it.
		if derr := s.store.Delete(ctx, sb.ID); derr != nil && !errors.Is(derr, store.ErrNotFound) {
			// Non-fatal: a stale record that cannot be deleted is cosmetic;
			// the __builder filter still hides it.
			_ = derr
		}
	}
}

// GetByLabels returns all sandboxes whose Labels map contains every key=value
// pair in labels (AND-semantics). An empty labels argument matches nothing.
func (s *Service) GetByLabels(ctx context.Context, labels map[string]string) ([]domain.Sandbox, error) {
	return s.store.GetByLabels(ctx, labels)
}

// GetByMotive returns all sandboxes associated with the given motive ID.
// Convenience wrapper over GetByLabels for the motive=<id> pattern.
// An unknown or empty motive returns an empty (non-nil) slice and nil error.
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

// Get returns the sandbox identified by ref (handle, exact ID, or ID prefix).
func (s *Service) Get(ctx context.Context, ref string) (domain.Sandbox, error) {
	return s.resolve(ctx, ref)
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

	// D-PD-36 boot-time invariant: a GitHub secret host without a per-repo
	// path allowlist is a forbidden configuration. CreateAndBoot enforces this
	// at creation time, but a record written before that guard existed (or one
	// that survived a failed rollback) must not be allowed to boot. Failing the
	// entire boot — rather than silently skipping or degrading the credential
	// swap — makes the misconfiguration immediately visible to the operator.
	// The sandbox must be deleted and recreated with a valid AllowedRepo.
	for _, h := range sb.Envelope.SecretHosts {
		if isGitHubHost(h) && sb.Envelope.AllowedRepo == "" {
			return domain.Sandbox{}, fmt.Errorf("service: start %s: %w", sb.ID, ErrUnboundGitHubSecret)
		}
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
		// M-b start-time volume guard (D-PD-94): re-run the rw attach check
		// before the driver boots the VM. Row 2 allows sandbox B to attach a
		// rw volume while A is stopped; without this guard, restarting A after
		// B attached would put two live VMs on one rw ext4 filesystem.
		//
		// checkRWAttach is idempotent for A's own attachment entry (Row-skip
		// path: att.SandboxID == sandboxID). It fires Row 1 if any other
		// sandbox is now Running or Paused with the same volume — exactly the
		// case we need to block. The check runs inside store.Update (holding
		// the per-sandbox flock) so no concurrent Start can race past it; the
		// per-volume flock acquired inside checkRWAttach is independent.
		//
		// Do NOT detach in Service.Stop: a stop/start cycle must not silently
		// drop user-attached volumes (D-PD-94 ruling, explicitly rejected).
		if s.volumes != nil {
			guardDiskDir := s.diskDir
			if guardDiskDir == "" {
				var e error
				guardDiskDir, e = defaultDiskDir()
				if e != nil {
					return fmt.Errorf("start volume guard: resolve disk dir: %w", e)
				}
			}
			for _, va := range rec.MountedVolumes {
				if va.Kind == string(volumestore.KindDisk) && !va.ReadOnly {
					guardCtx, guardCancel := context.WithTimeout(ctx, 10*time.Second)
					// Start-time guard: sandbox record already exists, so D2 does
					// not apply.  Release the lock immediately after the check.
					startLk, checkErr := checkRWAttach(guardCtx, s.volumes, s.store, guardDiskDir, va.Name, rec.ID.String())
					guardCancel()
					if startLk != nil {
						_ = startLk.Unlock()
						_ = startLk.Close()
					}
					if checkErr != nil {
						return fmt.Errorf("start volume guard: %w", checkErr)
					}
				}
			}
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

	// Re-inject SSH authorized_keys on restart when a key was provisioned at
	// creation. Best-effort: the guest agent may not be ready yet immediately
	// after Start; the error is swallowed (same pattern as caSeeder).
	if updated.Envelope.SSHPublicKey != "" && s.sshSeeder != nil {
		if err := SeedSSHAuthorizedKeys(ctx, updated.Envelope.SSHPublicKey, updated.ID, s.sshSeeder); err != nil {
			_ = err // best-effort; guest agent may not yet be reachable
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

	// Write-ahead removal marker. Must precede all destructive work.
	if err := s.store.SetRemovalMarker(ctx, sb.ID); err != nil {
		return fmt.Errorf("service: remove %s: set removal marker: %w", sb.ID, err)
	}

	// Terminate the VM inside the per-sandbox exclusive lock.
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

	// Delete the record. The marker is destroyed with it; no
	// ClearRemovalMarker call is needed or correct here.
	if err := s.store.Delete(ctx, sb.ID); err != nil {
		return fmt.Errorf("service: remove %s: delete record: %w", sb.ID, err)
	}

	// Reap per-sandbox disk resources. Both helpers are idempotent
	// (missing files are not errors) so crashes mid-remove are safe to retry.
	//
	// ReapDiskCopy removes ULID-keyed resources: .raw, -workspace.ext4,
	// .create-intent.json.
	//
	// ReapShadowDisks removes handle-keyed shadow disk images created by
	// buildShadowDiskSpecs. Shadow disks are named <safeHandle>.shadow.*.ext4
	// and cannot be correlated by ULID; the sandbox handle is the owner key.
	// Both are reaped here so that a single Service.Remove call is the complete
	// reclamation contract for all disk resources of a sandbox.
	_ = ReapDiskCopy(s.diskDir, sb.ID)
	_ = ReapShadowDisks(s.diskDir, sb.Handle())

	// Detach named volumes under the per-volume lock (D-PD-87: Remove
	// NEVER deletes volume backing files — only the attachment record is cleared).
	// Uses detachVolumeLocked so the write races neither against a concurrent
	// attach check nor a concurrent prune.
	//
	// Bound the acquisition independently of the caller's ctx (RISK-SD2-1): the
	// four CLI call sites that reach here supply the root signal.NotifyContext
	// which has no deadline, so a contended volume lock would spin forever without
	// this internal bound. WithoutCancel prevents a pre-cancelled ctx (already
	// returned an error) from skipping detach entirely.
	if s.volumes != nil {
		detachCtx, detachCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer detachCancel()
		for _, va := range sb.MountedVolumes {
			_ = detachVolumeLocked(detachCtx, s.volumes, va.Name, sb.ID.String())
		}
	}

	return nil
}

// startSupervisor assembles a PerimeterSupervisor for the running sandbox and
// stores it. Called by Start after the store lock is released, and by Fork
// and RestoreFromSnapshot for children persisted directly as Running.
func (s *Service) startSupervisor(ctx context.Context, hook driver.NetworkHook, sb domain.Sandbox) error {
	fd, err := hook.GuestNetworkFD(ctx, sb.ID)
	if err != nil {
		return fmt.Errorf("guest network fd: %w", err)
	}

	// D-PD-36: SecretHosts must be admitted at the TCP layer so the SNI bridge
	// can hand them to the MITM proxy for credential swap. Without this they are
	// blocked by the deny-all ACL before the dialer hook fires, producing a
	// "connection refused" error at the guest instead of a MITM CONNECT.
	// AllowedHosts covers the curated allowlist; SecretHosts covers MITM targets.
	aclHosts := append(append([]string(nil), sb.Envelope.AllowedHosts...), sb.Envelope.SecretHosts...)
	al, err := netfilter.NewAllowList(nil, nil, aclHosts)
	if err != nil {
		fd.Close()
		return fmt.Errorf("allow list: %w", err)
	}

	// D-PD-33: open egress is an explicit opt-in stored in the Envelope. An
	// empty AllowedHosts is no longer treated as an AllowAll sentinel — a
	// sandbox with empty AllowedHosts and OpenEgress=false gets NO egress.
	// Only the human create path (sandbox create) sets OpenEgress=true;
	// agent sandboxes never do. AllowAllFor is reachable only when OpenEgress
	// is true.
	allowAll := sb.Envelope.OpenEgress
	if allowAll {
		al.AllowAllFor(72 * time.Hour) // generous window; supervisor restarts reset it
	}

	stack := netstack.New(al, nil) // onAudit: nil (discards events; a future slice wires observability)

	// D-PD-36 redundant backstop: also guard here so fork/restore children
	// are covered if any future code path copies SecretHosts onto children.
	// fd and al have been acquired; clean them up if we bail out.
	if sb.Envelope.AllowedRepo == "" {
		for _, h := range sb.Envelope.SecretHosts {
			if isGitHubHost(h) {
				fd.Close()
				al.Stop()
				return fmt.Errorf("service: sandbox %s: %w", sb.ID, ErrUnboundGitHubSecret)
			}
		}
	}

	// MITM proxy: see [SandboxHasMITMProxy] for the conditions. Pure AllowAll
	// sandboxes with nothing to swap skip MITM so build tools see real server
	// certs.
	var proxy *mitm.Proxy
	if SandboxHasMITMProxy(sb) {
		var err error
		proxy, err = mitm.New(mitm.Config{
			SandboxID:       sb.ID,
			AllowedHosts:    sb.Envelope.AllowedHosts,
			SecretHosts:     sb.Envelope.SecretHosts,
			Broker:          s.broker,
			AllowAll:        allowAll && (len(sb.Envelope.SecretHosts) > 0 || sb.AgentName != ""),
			AllowedRepo:     sb.Envelope.AllowedRepo,                    // D-PD-36: per-repo path allowlist
			AllowedBranches: sb.Envelope.ResolvedAllowedBranches(),      // S0: default applied here
		})
		if err != nil {
			fd.Close()
			al.Stop()
			return fmt.Errorf("mitm proxy: %w", err)
		}
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

	// AllowAll mode: no MITM proxy; direct forwarding is used for port 443.

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

// GetPerimeterCACert returns the MITM proxy CA certificate for the sandbox
// with the given ID. Returns nil if no perimeter supervisor is running for it
// (e.g. Start has not been called yet, or the supervisor was closed).
// Safe for concurrent use.
// SandboxHasMITMProxy reports whether starting this sandbox's perimeter will
// stand up an L7 MITM proxy, and therefore whether a CA certificate will ever
// become available from [Service.GetPerimeterCACert].
//
// It exists so the component that CREATES the proxy and the component that
// WAITS for its CA cert read the same predicate. They were separate copies:
// the supervisor's seed loop retried 30 times at 2s intervals waiting for a
// cert that the perimeter had already decided not to produce, spending a
// minute before signalling READY and then seeding nothing.
//
// Three postures need a proxy:
//   - a curated egress allowlist (OpenEgress false), where the proxy is what
//     enforces the allowlist at L7;
//   - any sandbox carrying secret hosts, where the proxy performs the
//     placeholder→real swap for the human/git path;
//   - any sandbox running an agent, for the same swap on the agent's
//     credential. An agent without a proxy would hold a placeholder bearer
//     that nothing ever exchanges, and would fail against the real API.
func SandboxHasMITMProxy(sb domain.Sandbox) bool {
	return !sb.Envelope.OpenEgress ||
		len(sb.Envelope.SecretHosts) > 0 ||
		sb.AgentName != ""
}

func (s *Service) GetPerimeterCACert(id domain.SandboxID) *x509.Certificate {
	s.supervisorsMu.Lock()
	sup := s.supervisors[id]
	s.supervisorsMu.Unlock()
	if sup == nil {
		return nil
	}
	return sup.CACert()
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
		// Interim gate (TBR-PD-15, D-PD-96): refuse snapshots when any named
		// volume is attached. A retained snapshot manifest would carry the parent's
		// volume paths; RestoreFromSnapshot calls ForkFrom with no volume check, so
		// the restored child's config.json would point at the parent's named-volume
		// image read-write. Gating here covers every ForkFrom caller — the two that
		// exist and any added later — which per-caller gating does not. TBR-PD-15
		// will design snapshot-with-volumes as a whole; this gate is not a settled
		// semantic. Use independent nexus3 create calls for sandboxes that need volumes.
		var attachedVolDescs []string
		for _, va := range rec.MountedVolumes {
			attachedVolDescs = append(attachedVolDescs, va.Name+"(kind="+va.Kind+")")
		}
		if len(attachedVolDescs) > 0 {
			return fmt.Errorf(
				"sandbox has attached named volume(s) [%s]: "+
					"snapshotting a sandbox with named volumes is not yet supported "+
					"(TBR-PD-15, D-PD-96); use independent nexus3 create calls for "+
					"sandboxes that need volumes",
				strings.Join(attachedVolDescs, ", "),
			)
		}
		// D-PD-53: refuse snapshot on any sandbox with live host-directory mounts.
		// A retained snapshot manifest would carry the parent's mount paths;
		// RestoreFromSnapshot → ForkFrom would then give the restored child a live
		// virtiofs link back to the same host directory, meaning two VMs share one
		// mutable host directory — the exact corruption D-PD-53 exists to prevent.
		var liveMountDescs []string
		for _, lm := range rec.LiveMounts {
			liveMountDescs = append(liveMountDescs, lm.HostPath+"→"+lm.GuestPath)
		}
		if len(liveMountDescs) > 0 {
			return fmt.Errorf(
				"sandbox has live host-directory mount(s) [%s]: "+
					"snapshotting a sandbox with live mounts is not supported (D-PD-53); "+
					"two VMs sharing one host directory read-write causes data corruption",
				strings.Join(liveMountDescs, ", "),
			)
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
// ForkOption customises Fork and RestoreFromSnapshot. Variadic so that the
// existing three-argument call sites keep compiling unchanged.
type ForkOption func(*forkConfig)

type forkConfig struct {
	forceDiskSpace bool
	diskPreflight  func(diskDir string, projected int64, detail string) (*DiskPreflightResult, error)
}

// ForkForceDiskSpace skips the disk-space preflight. Set by --force.
func ForkForceDiskSpace() ForkOption {
	return func(c *forkConfig) { c.forceDiskSpace = true }
}

// ForkDiskPreflight overrides the disk-space check. Tests use it to drive the
// refusal path without filling a real filesystem.
func ForkDiskPreflight(fn func(diskDir string, projected int64, detail string) (*DiskPreflightResult, error)) ForkOption {
	return func(c *forkConfig) { c.diskPreflight = fn }
}

func newForkConfig(opts []ForkOption) forkConfig {
	c := forkConfig{diskPreflight: CheckDiskSpaceBytes}
	for _, o := range opts {
		o(&c)
	}
	if c.diskPreflight == nil {
		c.diskPreflight = CheckDiskSpaceBytes
	}
	return c
}

// checkForkDiskSpace refuses a fork that cannot fit. Fork copies EVERY one of
// the parent's disks once per child — root .raw plus workspace and shadow
// disks — so an N-way fork of a 5 GiB parent needs 5N GiB. This is the single
// largest allocation nexus3 performs, and until TBD-PD-26 it was unguarded.
//
// Projection is measured, not estimated: every file being copied already
// exists on disk. A parent with nothing in diskDir projects zero and the check
// is skipped rather than charged a default.
func checkForkDiskSpace(cfg forkConfig, diskDir, parentID string, count int, what string) error {
	if cfg.forceDiskSpace {
		return nil
	}
	projected, detail := ProjectForkBytes(diskDir, parentID, count)
	if projected <= 0 {
		return nil
	}
	if _, err := cfg.diskPreflight(diskDir, projected, detail); err != nil {
		return fmt.Errorf("service: %s: %w", what, err)
	}
	return nil
}

func (s *Service) Fork(ctx context.Context, ref string, count int, opts ...ForkOption) ([]domain.Sandbox, error) {
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

	// D-PD-96: refuse fork on any parent with ANY attached named volume,
	// regardless of kind.  The correct fork-with-volumes semantic is pending
	// design (TBR-PD-15); until that design is ratified, allowing any kind
	// through would silently establish a contract that may need reversal.
	//
	// Background per kind:
	//   kind=disk: virtio-blk block device; two VMs sharing the same ext4 image
	//     read-write simultaneously corrupt it; a per-child copy leaks into
	//     unreclaimed storage permanently (D-PD-95).
	//   kind=dir: host directory served over virtiofs; two VMs sharing the same
	//     host directory get a single mutable view — the isolation that fork is
	//     supposed to provide does not exist (D-PD-53).
	// Use independent `nexus3 create` calls for sandboxes that need volumes.
	var attachedVolDescs []string
	for _, va := range parent.MountedVolumes {
		attachedVolDescs = append(attachedVolDescs, va.Name+"(kind="+va.Kind+")")
	}
	if len(attachedVolDescs) > 0 {
		return nil, fmt.Errorf(
			"service: fork %s: sandbox has attached named volume(s) [%s]: "+
				"forking a sandbox with named volumes is not yet supported (TBR-PD-15, D-PD-96); "+
				"detach the volume(s) before forking",
			parent.ID, strings.Join(attachedVolDescs, ", "),
		)
	}

	// D-PD-53: refuse fork on any parent with live host-directory mounts.
	// Two VMs sharing one host worktree read-write is the exact corruption
	// scenario D-PD-53 exists to prevent: virtiofs exposes a single mutable
	// directory and both children would write into it concurrently.
	var liveMountDescs []string
	for _, lm := range parent.LiveMounts {
		liveMountDescs = append(liveMountDescs, lm.HostPath+"→"+lm.GuestPath)
	}
	if len(liveMountDescs) > 0 {
		return nil, fmt.Errorf(
			"service: fork %s: sandbox has live host-directory mount(s) [%s]: "+
				"forking a sandbox with live mounts is not supported (D-PD-53); "+
				"two VMs sharing one host directory read-write causes data corruption",
			parent.ID, strings.Join(liveMountDescs, ", "),
		)
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

	// Lease every child ID before the driver materialises anything.
	//
	// ForkFrom writes <diskDir>/<childID>.raw for each child
	// (cloudhypervisor/fork.go), and the reaper enumerates those files as
	// ULID-keyed disks. The child records are not committed until after
	// ForkFrom returns for ALL children, so without a lease each child disk
	// spends the entire fork window unowned and unleased — precisely the shape
	// the reaper classifies as an orphan and unlinks, while Fork goes on to
	// return the child as a live sandbox. That is the same silent-loss failure
	// CreateAndBoot closes with writeCreateIntent; fork reaches it by a
	// different path and needs the same protection.
	//
	// The lease is taken here rather than inside the driver so that it covers
	// the child disk from before it exists, and it is dropped only after that
	// child's store.Create commits, so no child disk is ever simultaneously
	// unleased and unrecorded.
	//
	// diskDir must be the directory the reaper scans (store.DefaultRoot()/disks
	// via defaultDiskDir), which is also where the driver places child disks:
	// it derives them from the parent's own disk path. A parent created with a
	// non-default DiskDir puts its children outside the reaper's scan entirely,
	// so there is no window to protect there either.
	diskDir := s.diskDir
	if diskDir == "" {
		diskDir, err = defaultDiskDir()
		if err != nil {
			return nil, fmt.Errorf("service: fork %s: resolve disk dir: %w", parent.ID, err)
		}
	}
	if err := checkForkDiskSpace(newForkConfig(opts), diskDir, parent.ID.String(), count,
		fmt.Sprintf("fork %s", parent.ID)); err != nil {
		return nil, err
	}

	// Lease every child's disks before the driver writes them. Two intents are
	// needed and neither substitutes for the other: the ULID create intent
	// covers <childID>.raw (D-PD-74), and the handle-keyed shadow intent
	// covers the child's copies of the parent's shadow disks (TBD-PD-38).
	//
	// Drain whatever is still held when Fork returns. On the success path each
	// pair is released the moment its record commits and removed from the map,
	// so this drains only children that never got a record — whose disks then
	// become correctly reclaimable orphans instead of leaking forever behind a
	// flock this process still holds.
	childLeases, childShadowLeases, leaseErr := leaseForkChildren(diskDir, parent.Handle(), childIDs)
	defer func() {
		for _, l := range childLeases {
			l.release()
		}
		for _, l := range childShadowLeases {
			l.Release()
		}
	}()
	if leaseErr != nil {
		return nil, fmt.Errorf("service: fork %s: %w", parent.ID, leaseErr)
	}

	// Spawn all children from the snapshot in one driver call.
	instanceIDs, err := forker.ForkFrom(ctx, snap, childIDs)
	if err != nil {
		return nil, fmt.Errorf("service: fork %s: driver: %w", parent.ID, err)
	}

	// Persist each child record. Children start directly in Running state
	// (edge 5: ∅→running); there is no ∅ state in the domain model so we
	// use store.Create (not store.Update) with State already set to Running.
	//
	// Envelope and Labels are copied from the parent. OpenEgress is inherited
	// so a human sandbox's open-egress posture carries through to its children.
	// Copying the allowlist is NOT a credential copy: broker tokens are keyed
	// by sandbox ID, so the child MITM mints its own placeholders.
	// D-PD-22: an agent parent has no github.com in AllowedHosts, so children
	// stay dark. D-PD-33: OpenEgress is explicit — a child with an empty
	// AllowedHosts gets no egress unless the parent had OpenEgress=true.
	children := make([]domain.Sandbox, 0, count)
	for i, id := range childIDs {
		child := domain.Sandbox{
			ID:         id,
			Name:       fmt.Sprintf("fork-%s", id.String()[3:]),
			Project:    parent.Project,
			Labels:     maps.Clone(parent.Labels),
			State:      domain.Running,
			InstanceID: instanceIDs[i],
			Envelope: domain.Envelope{
				ImageDigest:  parent.Envelope.ImageDigest,
				AllowedHosts: slices.Clone(parent.Envelope.AllowedHosts),
				SSHPublicKey: parent.Envelope.SSHPublicKey,
				OpenEgress:   parent.Envelope.OpenEgress,  // D-PD-33: inherit explicit opt-in
				AllowedRepo:  parent.Envelope.AllowedRepo, // D-PD-36: inherit repo-scoped path allowlist
			},
			Provenance: &domain.Provenance{
				ParentID:       parent.ID,
				SourceSnapshot: string(snap.ID),
			},
		}
		if err := s.store.Create(ctx, child); err != nil {
			return nil, fmt.Errorf("service: fork %s: persist child %s: %w", parent.ID, id, err)
		}
		// The record is committed, so the reaper now classifies this child's
		// disk as Owned. Only now is it safe to drop the lease (release also
		// removes the intent file). Releasing before the commit — or deferring
		// all releases to the end of the loop — would reopen the window.
		// The record is committed, so the reaper now classifies this child's
		// disks as Owned — .raw by ULID, shadow copies via
		// forkChildShadowOwner. Only now is it safe to drop the leases
		// (releasing also removes the intent files). Releasing before the
		// commit — or deferring all releases to the end of the loop — would
		// reopen the window.
		releaseChildLeases(childLeases, childShadowLeases, id)
		children = append(children, child)
	}

	// Fork persists children Running and never calls Start, so this is the
	// only chance to attach a perimeter. Same gate as Start: NetworkHook
	// plus a broker. The driver already holds a per-child perimConn
	// (cloudhypervisor/fork.go); GuestNetworkFD claims it here.
	if hook, ok := s.driver.(driver.NetworkHook); ok && s.broker != nil {
		for i := range children {
			if err := s.startSupervisor(ctx, hook, children[i]); err != nil {
				return nil, fmt.Errorf("service: fork %s: perimeter child %s: %w", parent.ID, children[i].ID, err)
			}
		}
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
func (s *Service) RestoreFromSnapshot(ctx context.Context, snapID artifact.SnapshotID, count int, opts ...ForkOption) ([]domain.Sandbox, error) {
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

	restoreDiskDir := s.diskDir
	if restoreDiskDir == "" {
		d, dErr := defaultDiskDir()
		if dErr != nil {
			return nil, fmt.Errorf("service: restore %s: resolve disk dir: %w", snapID, dErr)
		}
		restoreDiskDir = d
	}

	// Disk-space preflight (TBD-PD-26). Restore copies the origin sandbox's
	// disks once per child exactly as Fork does, so it is guarded identically.
	if err := checkForkDiskSpace(newForkConfig(opts), restoreDiskDir, snap.SandboxID.String(), count,
		fmt.Sprintf("restore %s", snapID)); err != nil {
		return nil, err
	}

	// The origin sandbox must exist so we can reconstruct the egress policy
	// (Envelope). D-PD-33: without the origin we cannot determine whether
	// OpenEgress was set, so we fail loudly rather than silently producing a
	// sandbox with unknown or no egress policy.
	//
	// This lookup used to sit AFTER ForkFrom, which meant a missing origin
	// killed the restore only once N child VMs were already running. It is
	// hoisted here so the failure is free, and because the leases below need
	// the origin's handle to name its shadow disks.
	origin, originErr := s.store.Get(ctx, snap.SandboxID)
	if originErr != nil {
		return nil, fmt.Errorf("service: restore %s: origin sandbox %s unavailable — cannot reconstruct egress policy (D-PD-33): %w", snapID, snap.SandboxID, originErr)
	}

	// Lease every child's disks before the driver writes them (TBD-PD-38).
	// Restore had NO leases at all: both the ULID-keyed <childID>.raw and the
	// handle-keyed shadow copies were exposed to a concurrent reap for the
	// whole restore window. Fork's exposure was the narrower of the two and
	// was the one on record; this is the same defect in the sibling path.
	childLeases, childShadowLeases, leaseErr := leaseForkChildren(
		restoreDiskDir, origin.Handle(), childIDs)
	defer func() {
		for _, l := range childLeases {
			l.release()
		}
		for _, l := range childShadowLeases {
			l.Release()
		}
	}()
	if leaseErr != nil {
		return nil, fmt.Errorf("service: restore %s: %w", snapID, leaseErr)
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
			Project:    origin.Project,
			Labels:     maps.Clone(origin.Labels),
			Envelope: domain.Envelope{
				ImageDigest:  origin.Envelope.ImageDigest,
				AllowedHosts: slices.Clone(origin.Envelope.AllowedHosts),
				SSHPublicKey: origin.Envelope.SSHPublicKey,
				OpenEgress:   origin.Envelope.OpenEgress,  // D-PD-33: inherit explicit opt-in
				AllowedRepo:  origin.Envelope.AllowedRepo, // D-PD-36: inherit repo-scoped path allowlist
			},
			Provenance: &domain.Provenance{
				ParentID:       snap.SandboxID,
				SourceSnapshot: string(snapID),
			},
		}
		if err := s.store.Create(ctx, child); err != nil {
			return nil, fmt.Errorf("service: restore %s: persist child %s: %w", snapID, id, err)
		}
		// Record committed: the reaper resolves these disks to a live child
		// now, so and only now is it safe to drop the leases.
		releaseChildLeases(childLeases, childShadowLeases, id)
		children = append(children, child)
	}

	if hook, ok := s.driver.(driver.NetworkHook); ok && s.broker != nil {
		for i := range children {
			if err := s.startSupervisor(ctx, hook, children[i]); err != nil {
				return nil, fmt.Errorf("service: restore %s: perimeter child %s: %w", snapID, children[i].ID, err)
			}
		}
	}

	return children, nil
}
