package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
)

type routedDriver struct {
	noGuestDialDriver
	mu    sync.Mutex
	execs int
	stops int
}

func (d *routedDriver) Exec(_ context.Context, _ domain.SandboxID, _ agent.ExecOptions) (int32, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.execs++
	return 0, nil
}

func (d *routedDriver) Stop(_ context.Context, _ domain.SandboxID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stops++
	return nil
}

const (
	routeDefault = "route-test-default"
	routeOther   = "route-test-other"
)

func init() {
	for _, n := range []string{routeDefault, routeOther} {
		registry.Register(n, func(any) (driver.Driver, error) { return nil, nil })
	}
}

func newRouted(t *testing.T) (svc *service.Service, def, other *routedDriver, builds *int) {
	t.Helper()
	def = &routedDriver{noGuestDialDriver: noGuestDialDriver{name: routeDefault}}
	other = &routedDriver{noGuestDialDriver: noGuestDialDriver{name: routeOther}}
	builds = new(int)
	svc = service.New(newFileStore(t), def, lifecycle.New()).WithBackendDriverFactory(func(b string) (driver.Driver, error) {
		*builds++
		return other, nil
	})
	return svc, def, other, builds
}

func TestBackendRouting_RecordRoutesExecAndRemove(t *testing.T) {
	ctx := context.Background()
	svc, def, other, builds := newRouted(t)
	sb, err := svc.Create(ctx, "p", "sp", service.CreateOptions{Backend: routeOther})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Backend != routeOther {
		t.Fatalf("Backend = %q", sb.Backend)
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.Exec(ctx, sb.ID.String(), agent.ExecOptions{Argv: []string{"true"}}); err != nil {
			t.Fatal(err)
		}
	}
	if other.execs != 2 || def.execs != 0 || *builds != 1 {
		t.Fatalf("other.execs=%d def.execs=%d builds=%d", other.execs, def.execs, *builds)
	}
	if err := svc.Remove(ctx, sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	if other.stops != 1 || def.stops != 0 {
		t.Fatalf("other.stops=%d def.stops=%d", other.stops, def.stops)
	}
}

func TestBackendRouting_DefaultStampedAndEmptyUsesDefault(t *testing.T) {
	ctx := context.Background()
	svc, def, other, _ := newRouted(t)
	sb, err := svc.Create(ctx, "p", "d", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Backend != routeDefault {
		t.Fatalf("Backend = %q, want %q", sb.Backend, routeDefault)
	}
	if _, err := svc.Exec(ctx, sb.ID.String(), agent.ExecOptions{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if def.execs != 1 || other.execs != 0 {
		t.Fatalf("def.execs=%d other.execs=%d", def.execs, other.execs)
	}
}

func TestBackendRouting_LegacyRecordWithoutBackendUsesDefault(t *testing.T) {
	ctx := context.Background()
	st := newFileStore(t)
	def := &routedDriver{noGuestDialDriver: noGuestDialDriver{name: routeDefault}}
	svc := service.New(st, def, lifecycle.New())
	old := domain.Sandbox{ID: domain.NewSandboxID(), Project: "p", Name: "old", State: domain.Created}
	if err := st.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, old.ID)
	if err != nil || got.Backend != "" {
		t.Fatalf("Get: %+v err=%v", got, err)
	}
	if _, err := svc.Exec(ctx, old.ID.String(), agent.ExecOptions{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if def.execs != 1 {
		t.Fatalf("def.execs=%d", def.execs)
	}
}

func TestBackendRouting_NoFactoryForeignBackendErrors(t *testing.T) {
	ctx := context.Background()
	def := &routedDriver{noGuestDialDriver: noGuestDialDriver{name: routeDefault}}
	st := newFileStore(t)
	svc := service.New(st, def, lifecycle.New())
	sb, err := svc.Create(ctx, "p", "f", service.CreateOptions{Backend: routeOther})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Exec(ctx, sb.ID.String(), agent.ExecOptions{Argv: []string{"true"}}); err == nil {
		t.Fatal("want error")
	}
	if def.execs != 0 {
		t.Fatal("default driver must not serve a foreign backend")
	}
}

type deprovDriver struct {
	routedDriver
	deprov int
}

func (d *deprovDriver) Deprovision(context.Context, domain.SandboxID) error {
	d.deprov++
	return nil
}

func TestRemove_DeprovisionsRemoteSubstrate(t *testing.T) {
	ctx := context.Background()
	d := &deprovDriver{routedDriver: routedDriver{noGuestDialDriver: noGuestDialDriver{name: routeDefault}}}
	svc := service.New(newFileStore(t), d, lifecycle.New())
	sb, err := svc.Create(ctx, "p", "r", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(ctx, sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	if d.deprov != 1 {
		t.Fatalf("deprov=%d", d.deprov)
	}
}
