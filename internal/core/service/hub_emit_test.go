package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/hubclient"
)

type fakeHub struct {
	mu    sync.Mutex
	got   []hubclient.Event
	panic bool
}

func (f *fakeHub) EmitBestEffort(_ context.Context, ev hubclient.Event) {
	f.mu.Lock()
	f.got = append(f.got, ev)
	f.mu.Unlock()
	if f.panic {
		panic("boom")
	}
}

func (f *fakeHub) Last(context.Context, string) (*hubclient.Event, error) { return nil, nil }

func newHubSvc(t *testing.T, h service.HubEmitter) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return service.New(st, fake.New(), lifecycle.New()).
		WithAuditRoot(func() (string, error) { return t.TempDir(), nil }).
		WithHubEmitter(h)
}

func TestHubEmit_CreateRemoveEmitOnce(t *testing.T) {
	t.Setenv(hubclient.EnvSession, "sess-1")
	h := &fakeHub{}
	svc := newHubSvc(t, h)
	ctx := context.Background()

	sb, err := svc.Create(ctx, "p", "n", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.got) != 1 || h.got[0].Type != hubclient.TypeSandboxCreated {
		t.Fatalf("after create: %+v", h.got)
	}
	ev := h.got[0]
	if ev.Topic != "sandbox:"+sb.ID.String() || ev.Subject != sb.ID.String() || ev.Actor != "sess-1" {
		t.Fatalf("bad event: %+v", ev)
	}

	if err := svc.Remove(ctx, sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	if len(h.got) != 2 || h.got[1].Type != hubclient.TypeSandboxRemoved {
		t.Fatalf("after remove: %+v", h.got)
	}
}

func TestHubEmit_AnonymousActor(t *testing.T) {
	t.Setenv(hubclient.EnvSession, "")
	h := &fakeHub{}
	if _, err := newHubSvc(t, h).Create(context.Background(), "p", "n", service.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if h.got[0].Actor != hubclient.ActorAnonymous {
		t.Fatalf("actor = %q", h.got[0].Actor)
	}
}

func TestHubEmit_PanickingEmitterDoesNotFailOps(t *testing.T) {
	h := &fakeHub{panic: true}
	svc := newHubSvc(t, h)
	ctx := context.Background()
	sb, err := svc.Create(ctx, "p", "n", service.CreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Remove(ctx, sb.ID.String()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(h.got) != 2 {
		t.Fatalf("emits = %d", len(h.got))
	}
}
