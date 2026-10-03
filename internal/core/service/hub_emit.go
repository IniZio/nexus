package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/IniZio/nexus/internal/core/oomattr"
	"github.com/IniZio/nexus/internal/hubclient"
)

// hubEmitTimeout bounds each hub call so a slow journal write cannot stall lifecycle ops.
const hubEmitTimeout = 5 * time.Second

// HubEmitter delivers a session-hub event. Implementations must be best-effort:
// failures are swallowed, never returned.
type HubEmitter interface {
	EmitBestEffort(ctx context.Context, ev hubclient.Event)
	// Last returns the latest event for subject, or nil when none exists.
	Last(ctx context.Context, subject string) (*hubclient.Event, error)
}

// defaultHubEmitter is the production emitter, used when a Service has none
// injected.
var defaultHubEmitter HubEmitter = hubclient.New()

// WithHubEmitter injects the hub emitter and returns the receiver.
func (s *Service) WithHubEmitter(e HubEmitter) *Service {
	s.hub = e
	return s
}

type hubSuppressStopKey struct{}

// WithHubStopSuppressed marks ctx so Service.Stop skips its sandbox.stopped
// event; used for the internal create-to-supervisor handoff stop.
func WithHubStopSuppressed(ctx context.Context) context.Context {
	return context.WithValue(ctx, hubSuppressStopKey{}, true)
}

// WithHubSystemActor makes the service attribute hub events to
// system:supervisor; the detached supervisor process sets it.
func (s *Service) WithHubSystemActor() *Service {
	s.hubSystem = true
	return s
}

func (s *Service) hubActor() string {
	if s.hubSystem {
		return hubclient.ActorSupervisor
	}
	return callerHubActor()
}

func callerHubActor() string {
	if a := os.Getenv(hubclient.EnvSession); a != "" {
		return a
	}
	return hubclient.ActorAnonymous
}

func (s *Service) hubEmitter() HubEmitter {
	if s.hub != nil {
		return s.hub
	}
	return defaultHubEmitter
}

func (s *Service) emitSandboxEvent(ctx context.Context, typ, id, handle string) {
	s.emitHub(ctx, typ, id, hubclient.SandboxPayload{
		ID:     id,
		Handle: handle,
		Cause:  hubclient.CauseUser,
		By:     s.hubActor(),
	})
}

func (s *Service) emitHub(ctx context.Context, typ, id string, payload any) {
	s.emitHubAs(ctx, s.hubActor(), typ, id, payload)
}

func (s *Service) emitHubAs(ctx context.Context, actor, typ, id string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("service: hub payload marshal failed", "type", typ, "err", err)
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("service: hub emit panicked", "type", typ, "panic", r)
		}
	}()
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hubEmitTimeout)
	defer cancel()
	s.hubEmitter().EmitBestEffort(ectx, hubclient.Event{
		Topic:   hubclient.SandboxTopic(id),
		Type:    typ,
		Actor:   actor,
		Subject: id,
		Payload: raw,
	})
}

// emitStarted backfills a died event for a prior unterminated run, then emits
// sandbox.started carrying the boot id and vmstat oom_kill baseline.
func (s *Service) emitStarted(ctx context.Context, id, handle string) {
	now := oomattr.Take()
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("service: hub last panicked", "panic", r)
			}
		}()
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hubEmitTimeout)
		defer cancel()
		prev, err := s.hubEmitter().Last(lctx, id)
		if err != nil {
			slog.Warn("service: hub last failed", "sandbox", id, "err", err)
			return
		}
		if prev != nil && prev.Type == hubclient.TypeSandboxStarted {
			s.emitHubAs(ctx, hubclient.ActorSupervisor, hubclient.TypeSandboxDied, id, hubclient.SandboxDiedPayload{
				SandboxPayload: hubclient.SandboxPayload{
					ID: id, Handle: handle,
					Cause: oomattr.BackfillCause(prev.Payload, now),
					By:    hubclient.ActorSupervisor,
				},
				Backfilled: true,
			})
		}
	}()
	s.emitHub(ctx, hubclient.TypeSandboxStarted, id, hubclient.SandboxStartedPayload{
		SandboxPayload: hubclient.SandboxPayload{ID: id, Handle: handle, By: s.hubActor()},
		BootID:         now.BootID,
		OOMKill:        now.VmstatOOMKill,
	})
}
