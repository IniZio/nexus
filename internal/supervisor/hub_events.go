package supervisor

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/oomattr"
	"github.com/IniZio/nexus/internal/hubclient"
)

// hubEmitter is the supervisor's view of the session hub. Production wiring
// adapts internal/hubclient; tests use a fake.
type hubEmitter interface {
	emit(ctx context.Context, ev hubclient.Event) error
}

type clientHubEmitter struct{ c *hubclient.Client }

func (e clientHubEmitter) emit(ctx context.Context, ev hubclient.Event) error {
	_, err := e.c.Append(ctx, ev)
	return err
}

func newProdHubEmitter() hubEmitter { return clientHubEmitter{c: hubclient.New()} }

const hubEmitTimeout = 5 * time.Second

// lifecycleEvents emits sandbox.died for one sandbox. A nil
// receiver is a no-op.
type lifecycleEvents struct {
	em     hubEmitter
	take   func() oomattr.Snapshot
	id     string
	handle string
	before oomattr.Snapshot
}

func newLifecycleEvents(em hubEmitter, id, handle string) *lifecycleEvents {
	return &lifecycleEvents{em: em, take: oomattr.Take, id: id, handle: handle}
}

func (l *lifecycleEvents) base(cause hubclient.Cause) hubclient.SandboxPayload {
	return hubclient.SandboxPayload{ID: l.id, Handle: l.handle, Cause: cause, By: hubclient.ActorSupervisor}
}

func (l *lifecycleEvents) send(typ string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("hub.emit_marshal_failed", "type", typ, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hubEmitTimeout)
	defer cancel()
	ev := hubclient.Event{
		Topic:   hubclient.SandboxTopic(l.id),
		Type:    typ,
		Actor:   hubclient.ActorSupervisor,
		Subject: l.id,
		Payload: raw,
	}
	if err := l.em.emit(ctx, ev); err != nil {
		slog.Warn("hub.emit_failed", "type", typ, "sandbox", l.id, "err", err)
	}
}

// adopted records the baseline snapshot used to classify a later death. It
// emits nothing: sandbox.started/stopped come from service.Start/Stop.
func (l *lifecycleEvents) adopted() {
	if l == nil {
		return
	}
	l.before = l.take()
}

// died emits sandbox.died with a cause classified from counter deltas and sig.
func (l *lifecycleEvents) died(sig oomattr.Signal) {
	if l == nil {
		return
	}
	after := l.take()
	cause := oomattr.Classify(l.before, after, sig)
	if sig.Signal == oomattr.SignalUnknown {
		sig = oomattr.Signal{}
	}
	l.send(hubclient.TypeSandboxDied, hubclient.SandboxDiedPayload{
		SandboxPayload: l.base(cause),
		Signal:         sig.Signal,
		ExitCode:       sig.Code,
		OOM: hubclient.OOMDelta{
			VmstatDelta:       after.VmstatOOMKill - l.before.VmstatOOMKill,
			ScopeOOMKillDelta: after.ScopeOOMKill - l.before.ScopeOOMKill,
			AncestorOOMDelta:  after.AncestorOOMKill - l.before.AncestorOOMKill,
		},
	})
}

// runtimeExiter is implemented by drivers that record how the VM process exited.
type runtimeExiter interface {
	RuntimeExit(id string) (cloudhypervisor.ExitInfo, bool)
}

// exitSignal returns the recorded exit of id, or the zero Signal when unknown.
func exitSignal(drv any, id string) oomattr.Signal {
	if re, ok := drv.(runtimeExiter); ok {
		if info, ok := re.RuntimeExit(id); ok {
			return oomattr.Signal{Signal: info.Signal, Code: info.Code}
		}
	}
	return oomattr.Signal{}
}
