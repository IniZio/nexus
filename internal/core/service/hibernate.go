package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/statedir"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/hubclient"
)

var (
	// ErrHibernateUnsupported: the sandbox's driver cannot hibernate.
	ErrHibernateUnsupported = errors.New("service: hibernate unsupported")
	// ErrHibernateRefused: the sandbox cannot be hibernated (mounts, volumes, builder).
	ErrHibernateRefused = errors.New("service: hibernate refused")
	// ErrSnapshotFailed: the driver failed to write the hibernate snapshot.
	ErrSnapshotFailed = errors.New("service: snapshot failed")
)

// HibernateOutcome reports a Hibernate call. Already is true when the sandbox
// was already hibernated (idempotent no-op); the other fields are then zero.
type HibernateOutcome struct {
	Already bool
	Sandbox domain.Sandbox
	Result  driver.HibernateResult
}

type hibernateOpts struct{ skipVMMStop bool }

// HibernateOption tunes Service.Hibernate.
type HibernateOption func(*hibernateOpts)

// WithSkipVMMStop leaves the VMM running after the hibernated record is
// committed; the caller (the supervisor) stops it itself.
func WithSkipVMMStop() HibernateOption {
	return func(o *hibernateOpts) { o.skipVMMStop = true }
}

// Hibernate snapshots the sandbox's VM into <state dir>/hibernate and records
// State=Hibernated. The whole operation runs under the per-sandbox store lock,
// so it cannot interleave with Resume. Hibernating a Hibernated sandbox is a
// no-op reporting Already.
func (s *Service) Hibernate(ctx context.Context, ref string, opts ...HibernateOption) (HibernateOutcome, error) {
	var o hibernateOpts
	for _, fn := range opts {
		fn(&o)
	}
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return HibernateOutcome{}, err
	}
	if sb.State == domain.Hibernated {
		return HibernateOutcome{Already: true, Sandbox: sb}, nil
	}
	if _, err := s.machine.Next(sb.State, lifecycle.TriggerHibernate); err != nil {
		return HibernateOutcome{}, fmt.Errorf("service: hibernate %s: %w", sb.ID, err)
	}
	drv, err := s.drvFor(sb)
	if err != nil {
		return HibernateOutcome{}, err
	}
	hib, ok := drv.(driver.Hibernator)
	if !ok {
		return HibernateOutcome{}, fmt.Errorf("service: hibernate %s: driver %q: %w", sb.ID, drv.Name(), ErrHibernateUnsupported)
	}
	rootFn := s.auditRoot
	if rootFn == nil {
		rootFn = store.DefaultRoot
	}
	root, err := rootFn()
	if err != nil {
		return HibernateOutcome{}, fmt.Errorf("service: hibernate %s: state root: %w", sb.ID, err)
	}
	dir := filepath.Join(statedir.SupervisorDir(root, sb.ID), "hibernate")

	var out HibernateOutcome
	committed := false
	err = s.store.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		if rec.State == domain.Hibernated {
			out = HibernateOutcome{Already: true, Sandbox: *rec}
			return nil
		}
		if rec.Project == "__builder" {
			return fmt.Errorf("%w: builder sandbox", ErrHibernateRefused)
		}
		if d := attachedVolumeDescs(*rec); len(d) > 0 {
			return fmt.Errorf("%w: attached named volume(s) [%s]", ErrHibernateRefused, strings.Join(d, ", "))
		}
		if d := liveMountDescs(*rec); len(d) > 0 {
			return fmt.Errorf("%w: live host-directory mount(s) [%s]", ErrHibernateRefused, strings.Join(d, ", "))
		}
		tr, err := s.machine.Next(rec.State, lifecycle.TriggerHibernate)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		res, err := hib.HibernateTo(ctx, rec.ID, dir)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrSnapshotFailed, err)
		}
		committed = true
		rec.State = tr.NextState
		rec.HibernateDir = dir
		rec.SnapshotBytes = res.SnapshotBytes
		rec.SnapshotBytesOnDisk = res.SnapshotBytesOnDisk
		out = HibernateOutcome{Sandbox: *rec, Result: res}
		return nil
	})
	if err != nil {
		if committed {
			// Record not persisted: undo so the sandbox stays Running.
			if pr, ok := drv.(driver.PauseResumer); ok {
				if rerr := pr.Resume(ctx, sb.ID); rerr != nil {
					err = errors.Join(err, fmt.Errorf("resume after failed hibernate: %w", rerr))
				}
			}
			_ = os.RemoveAll(dir)
		}
		return HibernateOutcome{}, fmt.Errorf("service: hibernate %s: %w", sb.ID, err)
	}
	if out.Already || o.skipVMMStop {
		return out, nil
	}
	if err := hib.StopAfterHibernate(ctx, sb.ID); err != nil {
		return out, fmt.Errorf("service: hibernate %s: stop vmm: %w", sb.ID, err)
	}
	return out, nil
}

// stopHibernated discards the hibernate snapshot and moves Hibernated to
// Stopped. No VMM or supervisor exists, so no driver call is made.
func (s *Service) stopHibernated(ctx context.Context, sb domain.Sandbox) (domain.Sandbox, error) {
	var updated domain.Sandbox
	err := s.store.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		tr, err := s.machine.Next(rec.State, lifecycle.TriggerStop)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		if rec.HibernateDir != "" {
			if err := os.RemoveAll(rec.HibernateDir); err != nil {
				return fmt.Errorf("discard snapshot: %w", err)
			}
		}
		rec.State = tr.NextState
		rec.HibernateDir = ""
		rec.SnapshotBytes = 0
		rec.SnapshotBytesOnDisk = 0
		rec.StopReason = domain.StopReasonClean
		updated = *rec
		return nil
	})
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: stop %s: %w", sb.ID, err)
	}
	if suppressed, _ := ctx.Value(hubSuppressStopKey{}).(bool); !suppressed {
		s.emitSandboxEvent(ctx, hubclient.TypeSandboxStopped, updated.ID.String(), updated.Handle())
	}
	return updated, nil
}
