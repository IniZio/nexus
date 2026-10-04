package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/statedir"
	"github.com/IniZio/nexus/internal/core/store"
)

// ResumeOutcomeFile is written to the state dir by a supervisor spawned in
// restore mode, once the VM is back and before supervisor.pid (READY) appears.
// The spawning CLI reads it to report how the resume went.
const ResumeOutcomeFile = "resume-outcome.json"

// clockSetTimeout bounds the post-restore SetClock RPC.
const clockSetTimeout = 5 * time.Second

// ResumeReport is the JSON body of ResumeOutcomeFile.
type ResumeReport struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	ResumedFrom    string `json:"resumed_from"` // snapshot | cold
	RestoreMode    string `json:"restore_mode,omitempty"`
	RestoreMs      int64  `json:"restore_ms"`
	AgentReadyMs   int64  `json:"agent_ready_ms"`
	TotalMs        int64  `json:"total_ms"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	ClockSkewMs    int64  `json:"clock_skew_ms,omitempty"`
}

// ResumeOutcomePath returns <stateDir>/resume-outcome.json.
func ResumeOutcomePath(stateDir string) string {
	return filepath.Join(stateDir, ResumeOutcomeFile)
}

// ReadResumeOutcome loads the report a restore-mode supervisor wrote.
func ReadResumeOutcome(stateDir string) (ResumeReport, error) {
	data, err := os.ReadFile(ResumeOutcomePath(stateDir))
	if err != nil {
		return ResumeReport{}, fmt.Errorf("supervisor: read resume outcome: %w", err)
	}
	var r ResumeReport
	if err := json.Unmarshal(data, &r); err != nil {
		return ResumeReport{}, fmt.Errorf("supervisor: decode resume outcome: %w", err)
	}
	return r, nil
}

func writeResumeOutcome(stateDir string, out service.ResumeOutcome, skewMs int64) error {
	r := ResumeReport{
		ID:             out.Sandbox.ID.String(),
		State:          out.Sandbox.State.String(),
		ResumedFrom:    out.ResumedFrom,
		RestoreMode:    string(out.Mode),
		RestoreMs:      out.RestoreMs,
		AgentReadyMs:   out.AgentReadyMs,
		TotalMs:        out.TotalMs,
		FallbackReason: out.FallbackReason,
		ClockSkewMs:    skewMs,
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(ResumeOutcomePath(stateDir), data, statedir.FileMode)
}

// startOrResume boots the sandbox this supervisor owns: svc.Start normally,
// service.ResumeHibernated when cfg.RestoreFrom is set or the record is
// Hibernated. ResumeHibernated runs on svc's driver, which is this process's
// own CHDriver, so the restored VMM and netns child belong to this supervisor
// exactly as after Start; the service also persists the netns identity fields.
//
// setClock, when non-nil, is called after a snapshot restore to resync the
// guest wall clock (which lags by the hibernated duration) and returns the
// measured skew in ms. Cold starts and cold fallbacks boot a fresh guest that
// syncs its own clock, so it is not called for them. A setClock failure is
// logged and leaves clock_skew_ms unset; it never fails the resume.
func startOrResume(ctx context.Context, svc *service.Service, st store.Store, cfg Config, setClock func(context.Context, domain.SandboxID) (int64, error)) (domain.Sandbox, error) {
	rec, err := st.ResolveByPrefix(ctx, cfg.SandboxRef)
	if err != nil || (cfg.RestoreFrom == "" && rec.State != domain.Hibernated) {
		return svc.Start(ctx, cfg.SandboxRef)
	}
	if rec.State != domain.Hibernated {
		return domain.Sandbox{}, fmt.Errorf("supervisor: restore requested but sandbox %s is %s, not hibernated", rec.ID, rec.State)
	}
	if cfg.RestoreFrom != "" && rec.HibernateDir != cfg.RestoreFrom {
		if err := st.Update(ctx, rec.ID, func(r *domain.Sandbox) error {
			r.HibernateDir = cfg.RestoreFrom
			return nil
		}); err != nil {
			return domain.Sandbox{}, fmt.Errorf("supervisor: point %s at %s: %w", rec.ID, cfg.RestoreFrom, err)
		}
	}
	out, err := svc.ResumeHibernated(ctx, cfg.SandboxRef, service.ResumeOptions{
		Mode:           driver.RestoreMode(cfg.RestoreMode),
		NoColdFallback: cfg.NoColdFallback,
	})
	if err != nil {
		return domain.Sandbox{}, err
	}
	if out.Already || out.ResumedFrom == service.ResumedFromMemory {
		return domain.Sandbox{}, fmt.Errorf("supervisor: sandbox %s was already live; refusing to supervise a VM this process did not restore", rec.ID)
	}
	var skewMs int64
	if setClock != nil && out.ResumedFrom == service.ResumedFromSnapshot {
		cctx, cancel := context.WithTimeout(ctx, clockSetTimeout)
		skew, cerr := setClock(cctx, out.Sandbox.ID)
		cancel()
		if cerr != nil {
			slog.Warn("supervisor.resume_set_clock_failed", "id", out.Sandbox.ID, "err", cerr)
		} else {
			skewMs = skew
		}
	}
	if err := writeResumeOutcome(cfg.StateDir, out, skewMs); err != nil {
		return domain.Sandbox{}, fmt.Errorf("supervisor: write resume outcome: %w", err)
	}
	return out.Sandbox, nil
}
