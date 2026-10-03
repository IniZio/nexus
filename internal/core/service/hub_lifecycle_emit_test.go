package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/hubclient"
)

type lifecycleHub struct {
	mu   sync.Mutex
	got  []hubclient.Event
	prev *hubclient.Event
}

func (f *lifecycleHub) EmitBestEffort(_ context.Context, ev hubclient.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, ev)
}

func (f *lifecycleHub) Last(context.Context, string) (*hubclient.Event, error) { return f.prev, nil }

func (f *lifecycleHub) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.got {
		out = append(out, e.Type)
	}
	return out
}

func wantTypes(t *testing.T, h *lifecycleHub, want ...string) {
	t.Helper()
	got := h.types()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestHubEmit_CreateAndBootEmitsCreated(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	img := putFakeImage(t, ctx, cache)
	h := &lifecycleHub{}
	svc := newTestSvc(t, fake.New()).WithHubEmitter(h)
	var opts CreateAndBootOptions
	opts.Image = ImageSpec{Digest: string(img.Digest)}
	opts.CacheRoot = cacheRoot
	opts.DiskDir = t.TempDir()

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "booted", opts)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	wantTypes(t, h, hubclient.TypeSandboxCreated)
	var p hubclient.SandboxPayload
	_ = json.Unmarshal(h.got[0].Payload, &p)
	if p.ID != sb.ID.String() || p.Handle != "proj/booted" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestHubEmit_StartStopEmitOnce(t *testing.T) {
	ctx := context.Background()
	h := &lifecycleHub{}
	svc := newTestSvc(t, fake.New()).WithHubEmitter(h)
	sb, err := svc.Create(ctx, "proj", "ss", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var p hubclient.SandboxStartedPayload
	_ = json.Unmarshal(h.got[len(h.got)-1].Payload, &p)
	if p.ID != sb.ID.String() || p.Handle != "proj/ss" {
		t.Fatalf("started payload = %+v", p)
	}
	if _, err := svc.Stop(ctx, sb.ID.String()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := svc.Stop(ctx, sb.ID.String()); err == nil {
		t.Fatal("second Stop should fail")
	}
	wantTypes(t, h, hubclient.TypeSandboxCreated, hubclient.TypeSandboxStarted, hubclient.TypeSandboxStopped)
}

func TestHubEmit_StartBackfillsDiedBeforeStarted(t *testing.T) {
	ctx := context.Background()
	raw, _ := json.Marshal(hubclient.SandboxStartedPayload{BootID: "old"})
	h := &lifecycleHub{prev: &hubclient.Event{Type: hubclient.TypeSandboxStarted, Payload: raw}}
	svc := newTestSvc(t, fake.New()).WithHubEmitter(h)
	sb, err := svc.Create(ctx, "proj", "bf", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, h, hubclient.TypeSandboxCreated, hubclient.TypeSandboxDied, hubclient.TypeSandboxStarted)
	var d hubclient.SandboxDiedPayload
	_ = json.Unmarshal(h.got[1].Payload, &d)
	if !d.Backfilled {
		t.Fatalf("died not backfilled: %+v", d)
	}
}

func TestHubEmit_NoBackfillAfterTerminal(t *testing.T) {
	ctx := context.Background()
	h := &lifecycleHub{prev: &hubclient.Event{Type: hubclient.TypeSandboxStopped}}
	svc := newTestSvc(t, fake.New()).WithHubEmitter(h)
	sb, _ := svc.Create(ctx, "proj", "nb", CreateOptions{})
	if _, err := svc.Start(ctx, sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, h, hubclient.TypeSandboxCreated, hubclient.TypeSandboxStarted)
}

func TestHubEmit_ForkEmitsCreatedPerChild(t *testing.T) {
	ctx := context.Background()
	h := &lifecycleHub{}
	svc := newTestSvc(t, fake.New()).WithHubEmitter(h)
	sb, err := svc.Create(ctx, "proj", "parent", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	children, err := svc.Fork(ctx, sb.ID.String(), 2)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	created := 0
	for _, ty := range h.types() {
		if ty == hubclient.TypeSandboxCreated {
			created++
		}
	}
	if created != 1+len(children) {
		t.Fatalf("created events = %d, want %d (%v)", created, 1+len(children), h.types())
	}
}

func TestHubEmit_StopSuppressedForHandoff(t *testing.T) {
	ctx := context.Background()
	h := &lifecycleHub{}
	svc := newTestSvc(t, fake.New()).WithHubEmitter(h)
	sb, _ := svc.Create(ctx, "proj", "ho", CreateOptions{})
	if _, err := svc.Start(ctx, sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stop(WithHubStopSuppressed(ctx), sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, h, hubclient.TypeSandboxCreated, hubclient.TypeSandboxStarted)
}

func TestHubEmit_ActorSystemSupervisor(t *testing.T) {
	ctx := context.Background()
	t.Setenv(hubclient.EnvSession, "sess-x")
	raw, _ := json.Marshal(hubclient.SandboxStartedPayload{BootID: "old"})
	h := &lifecycleHub{prev: &hubclient.Event{Type: hubclient.TypeSandboxStarted, Payload: raw}}
	svc := newTestSvc(t, fake.New()).WithHubEmitter(h)
	sb, _ := svc.Create(ctx, "proj", "act", CreateOptions{})
	if _, err := svc.Start(ctx, sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	// died always system; started follows caller unless the service is the supervisor.
	if got := h.got[1].Actor; got != hubclient.ActorSupervisor {
		t.Fatalf("died actor = %q", got)
	}
	if got := h.got[2].Actor; got != "sess-x" {
		t.Fatalf("started actor = %q", got)
	}
	h2 := &lifecycleHub{}
	svc2 := newTestSvc(t, fake.New()).WithHubEmitter(h2).WithHubSystemActor()
	sb2, _ := svc2.Create(ctx, "proj", "act2", CreateOptions{})
	if _, err := svc2.Start(ctx, sb2.ID.String()); err != nil {
		t.Fatal(err)
	}
	if got := h2.got[1].Actor; got != hubclient.ActorSupervisor {
		t.Fatalf("started actor = %q", got)
	}
}
