package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

type resDriver struct {
	*fake.FakeDriver
	restores   atomic.Int32
	restoreErr error
	gotDir     string
	gotMode    driver.RestoreMode
}

func (d *resDriver) HibernateTo(context.Context, domain.SandboxID, string) (driver.HibernateResult, error) {
	return driver.HibernateResult{}, nil
}
func (d *resDriver) StopAfterHibernate(context.Context, domain.SandboxID) error { return nil }
func (d *resDriver) RestoreInPlace(_ context.Context, _ domain.SandboxID, dir string, o driver.RestoreOptions) (driver.RestoreResult, error) {
	d.restores.Add(1)
	d.gotDir, d.gotMode = dir, o.Mode
	if d.restoreErr != nil {
		return driver.RestoreResult{}, d.restoreErr
	}
	return driver.RestoreResult{RestoreMs: 7, AgentReadyMs: 3, TotalMs: 12, Mode: o.Mode}, nil
}

func newHibernated(t *testing.T) (*service.Service, *resDriver, store.Store, domain.Sandbox) {
	t.Helper()
	st := newFileStore(t)
	d := &resDriver{FakeDriver: fake.New()}
	svc := service.New(st, d, lifecycle.New())
	sb, err := svc.Create(context.Background(), "p", "r", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(context.Background(), sb.ID, func(r *domain.Sandbox) error {
		r.State = domain.Hibernated
		r.HibernateDir = "/snap/dir"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return svc, d, st, sb
}

func state(t *testing.T, st store.Store, id domain.SandboxID) domain.State {
	t.Helper()
	got, err := st.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got.State
}

func startCalls(d *resDriver, id domain.SandboxID) int {
	n := 0
	for _, c := range d.Calls() {
		if c.Kind == fake.CallStart && c.ID == id {
			n++
		}
	}
	return n
}

func TestResumeHibernated_Snapshot(t *testing.T) {
	svc, d, st, sb := newHibernated(t)
	base := startCalls(d, sb.ID)
	out, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{Mode: driver.RestoreModeOnDemand})
	if err != nil {
		t.Fatal(err)
	}
	if out.ResumedFrom != "snapshot" || out.Already || out.RestoreMs != 7 || out.AgentReadyMs != 3 || out.TotalMs != 12 || out.Mode != driver.RestoreModeOnDemand {
		t.Fatalf("out=%+v", out)
	}
	if d.gotDir != "/snap/dir" || d.gotMode != driver.RestoreModeOnDemand {
		t.Fatalf("dir=%q mode=%q", d.gotDir, d.gotMode)
	}
	if state(t, st, sb.ID) != domain.Running || startCalls(d, sb.ID) != base {
		t.Fatal("want Running without cold start")
	}
}

func TestResumeHibernated_ColdFallback(t *testing.T) {
	for name, rerr := range map[string]error{
		"invalid":      fmt.Errorf("x: %w", driver.ErrHibernateInvalid),
		"incompatible": fmt.Errorf("x: %w", driver.ErrHibernateIncompatible),
		"runtime":      errors.New("vm.restore exploded"),
	} {
		t.Run(name, func(t *testing.T) {
			svc, d, st, sb := newHibernated(t)
			d.restoreErr = rerr
			base := startCalls(d, sb.ID)
			out, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if out.ResumedFrom != "cold" || out.FallbackReason != rerr.Error() {
				t.Fatalf("out=%+v", out)
			}
			if state(t, st, sb.ID) != domain.Running || startCalls(d, sb.ID) != base+1 {
				t.Fatal("want Running via one cold start")
			}
		})
	}
}

func TestResumeHibernated_NoColdFallback(t *testing.T) {
	svc, d, st, sb := newHibernated(t)
	d.restoreErr = fmt.Errorf("x: %w", driver.ErrHibernateInvalid)
	base := startCalls(d, sb.ID)
	_, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{NoColdFallback: true})
	if !errors.Is(err, driver.ErrHibernateInvalid) || !errors.Is(err, service.ErrSnapshotFailed) {
		t.Fatalf("err=%v", err)
	}
	if state(t, st, sb.ID) != domain.Hibernated || startCalls(d, sb.ID) != base {
		t.Fatal("want Hibernated, no start")
	}
}

func TestResumeHibernated_ColdStartFailure(t *testing.T) {
	svc, d, st, sb := newHibernated(t)
	d.restoreErr = errors.New("restore boom")
	d.SetStartError(errors.New("start boom"))
	_, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{})
	if err == nil {
		t.Fatal("want error")
	}
	if state(t, st, sb.ID) != domain.Error {
		t.Fatalf("state=%v", state(t, st, sb.ID))
	}
}

func TestResumeHibernated_NotHibernator(t *testing.T) {
	st := newFileStore(t)
	svc := service.New(st, fake.New(), lifecycle.New())
	sb, _ := svc.Create(context.Background(), "p", "n", service.CreateOptions{})
	_ = st.Update(context.Background(), sb.ID, func(r *domain.Sandbox) error { r.State = domain.Hibernated; return nil })
	if _, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{}); !errors.Is(err, service.ErrHibernateUnsupported) {
		t.Fatalf("err=%v", err)
	}
}

func TestResumeHibernated_RunningAlready(t *testing.T) {
	svc, d, _, sb := newHibernated(t)
	if _, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	out, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{})
	if err != nil || !out.Already || d.restores.Load() != 1 {
		t.Fatalf("out=%+v err=%v restores=%d", out, err, d.restores.Load())
	}
}

func TestResumeHibernated_PausedMemory(t *testing.T) {
	svc, d, st, sb := newHibernated(t)
	_ = st.Update(context.Background(), sb.ID, func(r *domain.Sandbox) error { r.State = domain.Paused; return nil })
	out, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{})
	if err != nil || out.ResumedFrom != "memory" || d.restores.Load() != 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if state(t, st, sb.ID) != domain.Running {
		t.Fatal("want Running")
	}
}

func TestResumeHibernated_StoppedIllegal(t *testing.T) {
	svc, _, st, sb := newHibernated(t)
	_ = st.Update(context.Background(), sb.ID, func(r *domain.Sandbox) error { r.State = domain.Stopped; return nil })
	_, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{})
	var ite *lifecycle.IllegalTransitionError
	if !errors.As(err, &ite) {
		t.Fatalf("err=%v", err)
	}
}

func TestResumeHibernated_ConcurrentSerializes(t *testing.T) {
	svc, d, _, sb := newHibernated(t)
	var wg sync.WaitGroup
	var already atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{})
			if err != nil {
				t.Error(err)
			}
			if out.Already {
				already.Add(1)
			}
		}()
	}
	wg.Wait()
	if d.restores.Load() != 1 || already.Load() != 3 {
		t.Fatalf("restores=%d already=%d", d.restores.Load(), already.Load())
	}
}

type netnsResDriver struct {
	*resDriver
	ns driver.NetnsIdentity
}

func (d *netnsResDriver) NetnsState(domain.SandboxID) (driver.NetnsIdentity, bool) { return d.ns, true }

func TestResumeHibernated_PersistsNetnsState(t *testing.T) {
	for name, restoreErr := range map[string]error{"snapshot": nil, "cold": errors.New("boom")} {
		t.Run(name, func(t *testing.T) {
			st := newFileStore(t)
			d := &netnsResDriver{resDriver: &resDriver{FakeDriver: fake.New(), restoreErr: restoreErr},
				ns: driver.NetnsIdentity{ChildPID: 4242, ChildPGID: 4242, ChildStartTime: 99, VhostSocket: "/run/v.sock", APISocket: "/run/api.sock", ControlSocket: "/run/c.sock", ControlToken: "tok"}}
			svc := service.New(st, d, lifecycle.New())
			sb, err := svc.Create(context.Background(), "p", "ns", service.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_ = st.Update(context.Background(), sb.ID, func(r *domain.Sandbox) error {
				r.State = domain.Hibernated
				r.HibernateDir = "/snap/dir"
				return nil
			})
			if _, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{}); err != nil {
				t.Fatal(err)
			}
			got, _ := st.Get(context.Background(), sb.ID)
			if got.NetnsChildPID != 4242 || got.NetnsChildPGID != 4242 || got.NetnsChildStartTime != 99 ||
				got.VhostSocket != "/run/v.sock" || got.CHAPISocket != "/run/api.sock" ||
				got.NetnsControlSocket != "/run/c.sock" || got.NetnsControlToken != "tok" {
				t.Fatalf("netns not persisted: %+v", got)
			}
		})
	}
}

func TestResumeHibernated_ColdFallbackReportsTotalMs(t *testing.T) {
	svc, _, _, sb := newHibernated(t)
	out, err := svc.ResumeHibernated(context.Background(), sb.ID.String(), service.ResumeOptions{
		Restore: func(context.Context, domain.SandboxID, string, driver.RestoreOptions) (driver.RestoreResult, error) {
			return driver.RestoreResult{}, errors.New("boom")
		},
		ColdStart: func(context.Context, *domain.Sandbox) error { time.Sleep(15 * time.Millisecond); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ResumedFrom != "cold" || out.TotalMs < 10 {
		t.Fatalf("out=%+v", out)
	}
}
