package supervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/service"
)

type fakeHibSvc struct {
	out service.HibernateOutcome
	err error
	// onCall runs inside Hibernate (e.g. to observe the governor state).
	onCall func()
	skip   atomic.Bool
}

func (f *fakeHibSvc) Hibernate(_ context.Context, _ string, opts ...service.HibernateOption) (service.HibernateOutcome, error) {
	if f.onCall != nil {
		f.onCall()
	}
	return f.out, f.err
}

type fakeHibDrv struct {
	stops atomic.Int32
	err   error
}

func (d *fakeHibDrv) HibernateTo(context.Context, domain.SandboxID, string) (driver.HibernateResult, error) {
	return driver.HibernateResult{}, nil
}
func (d *fakeHibDrv) StopAfterHibernate(context.Context, domain.SandboxID) error {
	d.stops.Add(1)
	return d.err
}
func (d *fakeHibDrv) RestoreInPlace(context.Context, domain.SandboxID, string, driver.RestoreOptions) (driver.RestoreResult, error) {
	return driver.RestoreResult{}, nil
}

func hibServer(t *testing.T, svc *fakeHibSvc, drv *fakeHibDrv) (*hibernateCtl, ipcHandles, string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	dir := t.TempDir()
	sock := SockPath(dir)
	ctl := newHibernateCtl(svc, drv, "ref")
	var quiesced, resumed atomic.Int32
	ctl.bindGovernor(func() { quiesced.Add(1) }, func() { resumed.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := serveIPC(ctx, sock, nil, "ref", nil, nil, nil, "h", ctl)
	if err != nil {
		t.Fatal(err)
	}
	return ctl, h, dir, &quiesced, &resumed
}

func TestHibernateIPC_Success(t *testing.T) {
	var id domain.SandboxID
	id[0] = 7
	svc := &fakeHibSvc{out: service.HibernateOutcome{Sandbox: domain.Sandbox{ID: id}, Result: driver.HibernateResult{SnapshotBytes: 42, TotalMs: 9}}}
	drv := &fakeHibDrv{}
	ctl, h, dir, q, r := hibServer(t, svc, drv)

	out, err := RequestHibernate(context.Background(), dir)
	if err != nil {
		t.Fatalf("RequestHibernate: %v", err)
	}
	if out.Already || out.Result.SnapshotBytes != 42 || out.Result.TotalMs != 9 {
		t.Fatalf("outcome = %+v", out)
	}
	if !ctl.Expecting() {
		t.Fatal("expecting flag must stay set after success")
	}
	if drv.stops.Load() != 1 {
		t.Fatalf("StopAfterHibernate calls = %d, want 1", drv.stops.Load())
	}
	if q.Load() != 1 || r.Load() != 0 {
		t.Fatalf("governor quiesce=%d resume=%d, want 1/0", q.Load(), r.Load())
	}
	select {
	case <-h.HibernatedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor exit not signalled after response")
	}
	// VM exit now: reconcile must be suppressed (hibernated settles true).
	if cause := awaitShutdownHib(context.Background(), nil, nil, closedCh(), h.HibernatedCh); cause != shutdownByHibernate {
		t.Fatalf("cause = %v, want shutdownByHibernate", cause)
	}
}

func closedCh() <-chan struct{} { c := make(chan struct{}); close(c); return c }

func TestHibernateIPC_VMDeathDuringHibernateSuppressed(t *testing.T) {
	ctl := newHibernateCtl(&fakeHibSvc{}, &fakeHibDrv{}, "ref")
	ctl.expecting.Store(true)
	go func() { time.Sleep(60 * time.Millisecond); ctl.markResponded() }()
	if !ctl.waitSettled(context.Background()) {
		t.Fatal("waitSettled = false, want true (hibernated)")
	}
	// Not expecting: VM death is genuine.
	if newHibernateCtl(&fakeHibSvc{}, nil, "r").waitSettled(context.Background()) {
		t.Fatal("waitSettled true without a hibernate")
	}
	var nilCtl *hibernateCtl
	if nilCtl.Expecting() || nilCtl.waitSettled(context.Background()) {
		t.Fatal("nil ctl must be inert")
	}
}

func TestHibernateIPC_Failure(t *testing.T) {
	svc := &fakeHibSvc{err: errors.New("snapshot boom")}
	drv := &fakeHibDrv{}
	ctl, h, dir, q, r := hibServer(t, svc, drv)

	_, err := RequestHibernate(context.Background(), dir)
	if err == nil {
		t.Fatal("want error")
	}
	if ctl.Expecting() {
		t.Fatal("expecting flag must be cleared on failure")
	}
	if drv.stops.Load() != 0 {
		t.Fatal("StopAfterHibernate must not run on failure")
	}
	if q.Load() != 1 || r.Load() != 1 {
		t.Fatalf("governor quiesce=%d resume=%d, want 1/1", q.Load(), r.Load())
	}
	select {
	case <-h.HibernatedCh:
		t.Fatal("supervisor must keep running on failure")
	case <-time.After(100 * time.Millisecond):
	}
	// Retry works: flag was cleared.
	svc.err = nil
	if _, err := RequestHibernate(context.Background(), dir); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestHibernateIPC_Already(t *testing.T) {
	svc := &fakeHibSvc{out: service.HibernateOutcome{Already: true}}
	drv := &fakeHibDrv{}
	ctl, h, dir, _, r := hibServer(t, svc, drv)

	out, err := RequestHibernate(context.Background(), dir)
	if err != nil || !out.Already {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if drv.stops.Load() != 0 || ctl.Expecting() || r.Load() != 1 {
		t.Fatalf("Already must be a no-op (stops=%d expecting=%v resume=%d)", drv.stops.Load(), ctl.Expecting(), r.Load())
	}
	select {
	case <-h.HibernatedCh:
		t.Fatal("must not exit on Already")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHibernateIPC_NoController503(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := serveIPC(ctx, SockPath(dir), nil, "r", nil, nil, nil, "h"); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestHibernate(context.Background(), dir); err == nil {
		t.Fatal("want error")
	}
}

func TestRefuseHibernated(t *testing.T) {
	if refuseHibernated(domain.Sandbox{State: domain.Hibernated}) == nil {
		t.Fatal("hibernated must be refused")
	}
	if refuseHibernated(domain.Sandbox{State: domain.Running}) != nil {
		t.Fatal("running must pass")
	}
}

func TestGovRunner_QuiesceResume(t *testing.T) {
	var running atomic.Int32
	run := func(ctx context.Context) { running.Add(1); <-ctx.Done(); running.Add(-1) }
	g := newGovRunner(context.Background(), run)
	waitFor(t, func() bool { return running.Load() == 1 })
	g.Quiesce()
	if running.Load() != 0 {
		t.Fatal("governor still running after Quiesce")
	}
	g.Resume()
	waitFor(t, func() bool { return running.Load() == 1 })
	g.Quiesce()
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
