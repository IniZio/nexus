package service

import (
	"context"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// SetSupervisor persists the detached supervisor's PID and Unix-socket path
// onto the sandbox record. Called by orcaCreate after supervisor.SpawnDetached
// returns READY (D-PP-01 S2).
//
// The sandbox must already exist. State is not changed by this call.
func (s *Service) SetSupervisor(ctx context.Context, id domain.SandboxID, pid int, sock string) error {
	return s.store.Update(ctx, id, func(sb *domain.Sandbox) error {
		sb.SupervisorPID = pid
		sb.SupervisorSock = sock
		return nil
	})
}

// ClearSupervisor zeroes the supervisor fields on the sandbox record.
// Called after the supervisor has exited and the sandbox has been cleaned up.
func (s *Service) ClearSupervisor(ctx context.Context, id domain.SandboxID) error {
	return s.store.Update(ctx, id, func(sb *domain.Sandbox) error {
		sb.SupervisorPID = 0
		sb.SupervisorSock = ""
		return nil
	})
}

// SetNetnsIdentity persists a verified netns child identity onto the
// sandbox record in one write. It is the persistence half of ticket 11's
// netns identity backfill (supervisor.BackfillNetnsIdentity does the
// verification); this method performs NO verification of its own — it
// trusts the caller has already confirmed every field against /proc, and
// refuses only the shape-level case of an incomplete identity, mirroring
// the same fail-closed predicate cmd_supervisor_upgrade.go's refusal check
// and AdoptNetnsRuntime both apply on read.
func (s *Service) SetNetnsIdentity(ctx context.Context, id domain.SandboxID, pid, pgid int, startTime uint64, guestTap, apiSocket string) error {
	if pid <= 0 || pgid <= 0 || startTime == 0 || guestTap == "" || apiSocket == "" {
		return fmt.Errorf("service: set netns identity %s: refusing to persist an incomplete identity (pid=%d pgid=%d startTime=%d guestTap=%q apiSocket=%q)", id, pid, pgid, startTime, guestTap, apiSocket)
	}
	return s.store.Update(ctx, id, func(sb *domain.Sandbox) error {
		sb.NetnsChildPID = pid
		sb.NetnsChildPGID = pgid
		sb.NetnsChildStartTime = startTime
		sb.GuestTapName = guestTap
		sb.CHAPISocket = apiSocket
		return nil
	})
}

// GetSandboxByID retrieves a sandbox record by its ID.
// Thin accessor used by callers (e.g. orcaCreate) that already hold a
// domain.SandboxID and need the full record without going through a ref string.
func (s *Service) GetSandboxByID(ctx context.Context, id domain.SandboxID) (domain.Sandbox, error) {
	sb, err := s.store.Get(ctx, id)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: get sandbox %s: %w", id, err)
	}
	return sb, nil
}

// ResolveRef finds a sandbox by handle, exact ID, or ID prefix.
func (s *Service) ResolveRef(ctx context.Context, ref string) (domain.Sandbox, error) {
	return s.resolve(ctx, ref)
}

