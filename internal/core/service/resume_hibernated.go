package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/lifecycle"
)

// ResumedFrom values reported by ResumeHibernated.
const (
	ResumedFromSnapshot = "snapshot"
	ResumedFromCold     = "cold"
	ResumedFromMemory   = "memory"
)

// ResumeOptions tunes ResumeHibernated. Restore and ColdStart are injection
// points: the supervisor spawn path (HB-9) supplies its own so the VMM stays
// owned by the supervisor. Nil means the driver's RestoreInPlace and a plain
// driver.Start on the same disks.
type ResumeOptions struct {
	Mode           driver.RestoreMode
	NoColdFallback bool

	Restore   func(ctx context.Context, id domain.SandboxID, dir string, o driver.RestoreOptions) (driver.RestoreResult, error)
	ColdStart func(ctx context.Context, rec *domain.Sandbox) error
}

// ResumeOutcome reports a ResumeHibernated call. Already is true when the
// sandbox was already Running (idempotent no-op).
type ResumeOutcome struct {
	Already        bool
	Sandbox        domain.Sandbox
	ResumedFrom    string // snapshot | cold | memory
	FallbackReason string // set when ResumedFrom is cold
	Mode           driver.RestoreMode
	RestoreMs      int64
	AgentReadyMs   int64
	TotalMs        int64
}

// ResumeHibernated brings a sandbox back to Running. Running is a no-op;
// Paused delegates to Resume (vm.resume); Hibernated restores the snapshot in
// place and, unless NoColdFallback, cold-starts on the same disks when that
// fails. The restore runs under the per-sandbox store lock, so concurrent
// calls serialize and the losers observe Running. The snapshot dir is kept
// until the next hibernate or rm.
func (s *Service) ResumeHibernated(ctx context.Context, ref string, opts ResumeOptions) (ResumeOutcome, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return ResumeOutcome{}, err
	}
	switch sb.State {
	case domain.Running:
		return ResumeOutcome{Already: true, Sandbox: sb}, nil
	case domain.Paused:
		got, err := s.Resume(ctx, ref)
		if err != nil {
			return ResumeOutcome{}, err
		}
		return ResumeOutcome{Sandbox: got, ResumedFrom: ResumedFromMemory}, nil
	}
	if _, err := s.machine.Next(sb.State, lifecycle.TriggerResume); err != nil {
		return ResumeOutcome{}, fmt.Errorf("service: resume %s: %w", sb.ID, err)
	}
	drv, err := s.drvFor(sb)
	if err != nil {
		return ResumeOutcome{}, err
	}
	hib, ok := drv.(driver.Hibernator)
	if !ok {
		return ResumeOutcome{}, fmt.Errorf("service: resume %s: driver %q: %w", sb.ID, drv.Name(), ErrHibernateUnsupported)
	}
	restore := opts.Restore
	if restore == nil {
		restore = hib.RestoreInPlace
	}
	cold := opts.ColdStart
	if cold == nil {
		cold = func(ctx context.Context, rec *domain.Sandbox) error {
			id, err := drv.Start(ctx, driver.StartRequest{SandboxID: rec.ID, ImageDigest: rec.Envelope.ImageDigest})
			if err != nil {
				return err
			}
			rec.InstanceID = id
			rec.StopReason = ""
			return nil
		}
	}

	var out ResumeOutcome
	var coldStarted, restored bool
	var failure error
	err = s.store.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		if rec.State == domain.Running {
			out = ResumeOutcome{Already: true, Sandbox: *rec}
			return nil
		}
		tr, err := s.machine.Next(rec.State, lifecycle.TriggerResume)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		if rec.State != domain.Hibernated {
			return fmt.Errorf("re-validate: state %s: %w", rec.State, ErrHibernateRefused)
		}
		res, rerr := restore(ctx, rec.ID, rec.HibernateDir, driver.RestoreOptions{Mode: opts.Mode})
		if rerr == nil {
			rec.State = tr.NextState
			restored = true
			persistNetnsState(rec, drv)
			out = ResumeOutcome{
				Sandbox: *rec, ResumedFrom: ResumedFromSnapshot, Mode: res.Mode,
				RestoreMs: res.RestoreMs, AgentReadyMs: res.AgentReadyMs, TotalMs: res.TotalMs,
			}
			return nil
		}
		if opts.NoColdFallback {
			// Record stays Hibernated. A post-attempt failure leaves the
			// snapshot unusable (.attempted marker); the next resume cold-starts.
			failure = fmt.Errorf("%w: restore failed (an attempted snapshot is unusable on retry): %w", ErrSnapshotFailed, rerr)
			return nil
		}
		coldT0 := time.Now()
		if cerr := cold(ctx, rec); cerr != nil {
			rec.State = domain.Error
			failure = fmt.Errorf("cold start after restore failure (%v): %w", rerr, cerr)
			return nil
		}
		coldStarted = true
		rec.State = tr.NextState
		persistNetnsState(rec, drv)
		out = ResumeOutcome{Sandbox: *rec, ResumedFrom: ResumedFromCold, FallbackReason: rerr.Error(), TotalMs: time.Since(coldT0).Milliseconds()}
		return nil
	})
	if err != nil {
		return ResumeOutcome{}, fmt.Errorf("service: resume %s: %w", sb.ID, err)
	}
	if failure != nil {
		return ResumeOutcome{}, fmt.Errorf("service: resume %s: %w", sb.ID, failure)
	}
	// The old supervisor's perimeter died with it, so a restored VM needs a
	// fresh one exactly like a cold-started one.
	if (coldStarted && opts.ColdStart == nil) || (restored && opts.Restore == nil) {
		if hook, ok := drv.(driver.NetworkHook); ok && s.broker != nil {
			if err := s.startSupervisor(ctx, hook, out.Sandbox, nil); err != nil {
				return ResumeOutcome{}, fmt.Errorf("service: resume %s: perimeter: %w", sb.ID, errors.Join(err))
			}
		}
	}
	if !out.Already {
		s.emitStarted(ctx, out.Sandbox.ID.String(), out.Sandbox.Handle())
	}
	return out, nil
}

// persistNetnsState records the driver's netns adoption identity on rec so a
// replacement supervisor can call AdoptNetnsRuntime without consulting
// ps/nsenter. Drivers without a netns runtime (or reporting none) clear the
// fields so a stale pid from an earlier boot is never targeted.
func persistNetnsState(rec *domain.Sandbox, drv driver.Driver) {
	var ns driver.NetnsIdentity
	if nsp, ok := drv.(driver.NetnsStateProvider); ok {
		ns, _ = nsp.NetnsState(rec.ID)
	}
	rec.NetnsChildPID = ns.ChildPID
	rec.NetnsChildPGID = ns.ChildPGID
	rec.NetnsChildStartTime = ns.ChildStartTime
	rec.VhostSocket = ns.VhostSocket
	rec.CHAPISocket = ns.APISocket
	rec.NetnsControlSocket = ns.ControlSocket
	rec.NetnsControlToken = ns.ControlToken
}
