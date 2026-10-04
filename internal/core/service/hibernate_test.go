package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

type hibDriver struct {
	*fake.FakeDriver
	hibCalls, stops, resumes atomic.Int32
	hibErr                   error
}

func (d *hibDriver) HibernateTo(context.Context, domain.SandboxID, string) (driver.HibernateResult, error) {
	d.hibCalls.Add(1)
	if d.hibErr != nil {
		return driver.HibernateResult{}, d.hibErr
	}
	return driver.HibernateResult{TotalMs: 5, SnapshotBytes: 100, SnapshotBytesOnDisk: 40}, nil
}
func (d *hibDriver) StopAfterHibernate(context.Context, domain.SandboxID) error {
	d.stops.Add(1)
	return nil
}
func (d *hibDriver) RestoreInPlace(context.Context, domain.SandboxID, string, driver.RestoreOptions) (driver.RestoreResult, error) {
	return driver.RestoreResult{}, nil
}
func (d *hibDriver) Resume(ctx context.Context, id domain.SandboxID) error {
	d.resumes.Add(1)
	return d.FakeDriver.Resume(ctx, id)
}

// failStore fails Update's write phase by erroring after the callback ran.
type failStore struct {
	store.Store
	fail bool
}

func (f *failStore) Update(ctx context.Context, id domain.SandboxID, fn func(*domain.Sandbox) error) error {
	if !f.fail {
		return f.Store.Update(ctx, id, fn)
	}
	if err := f.Store.Update(ctx, id, func(r *domain.Sandbox) error {
		c := *r
		if err := fn(&c); err != nil {
			return err
		}
		return errors.New("disk full")
	}); err != nil {
		return err
	}
	return nil
}

func newHib(t *testing.T) (*service.Service, *hibDriver, store.Store, domain.Sandbox) {
	t.Helper()
	st := newFileStore(t)
	d := &hibDriver{FakeDriver: fake.New()}
	root := t.TempDir()
	svc := service.New(st, d, lifecycle.New()).WithAuditRoot(func() (string, error) { return root, nil })
	sb, err := svc.Create(context.Background(), "p", "h", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	return svc, d, st, sb
}

func TestHibernate_Success(t *testing.T) {
	svc, d, st, sb := newHib(t)
	out, err := svc.Hibernate(context.Background(), sb.ID.String())
	if err != nil || out.Already {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	got, _ := st.Get(context.Background(), sb.ID)
	if got.State != domain.Hibernated || got.HibernateDir == "" || got.SnapshotBytes != 100 || got.SnapshotBytesOnDisk != 40 {
		t.Fatalf("record: %+v", got)
	}
	if out.Result.TotalMs != 5 || d.stops.Load() != 1 {
		t.Fatalf("result=%+v stops=%d", out.Result, d.stops.Load())
	}
}

func TestHibernate_SkipVMMStop(t *testing.T) {
	svc, d, _, sb := newHib(t)
	if _, err := svc.Hibernate(context.Background(), sb.ID.String(), service.WithSkipVMMStop()); err != nil {
		t.Fatal(err)
	}
	if d.stops.Load() != 0 {
		t.Fatal("stop called")
	}
}

func TestHibernate_Already(t *testing.T) {
	svc, d, _, sb := newHib(t)
	if _, err := svc.Hibernate(context.Background(), sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Hibernate(context.Background(), sb.ID.String())
	if err != nil || !out.Already || d.hibCalls.Load() != 1 {
		t.Fatalf("out=%+v err=%v calls=%d", out, err, d.hibCalls.Load())
	}
}

func TestHibernate_Unsupported(t *testing.T) {
	st := newFileStore(t)
	svc := service.New(st, fake.New(), lifecycle.New())
	sb, _ := svc.Create(context.Background(), "p", "u", service.CreateOptions{})
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Hibernate(context.Background(), sb.ID.String()); !errors.Is(err, service.ErrHibernateUnsupported) {
		t.Fatalf("err=%v", err)
	}
}

func TestHibernate_Refused(t *testing.T) {
	for name, mut := range map[string]func(*domain.Sandbox){
		"live mount": func(r *domain.Sandbox) { r.LiveMounts = []domain.LiveMount{{HostPath: "/h", GuestPath: "/g"}} },
		"volume":     func(r *domain.Sandbox) { r.MountedVolumes = []domain.VolumeAttachment{{Name: "v", Kind: "dir"}} },
		"builder":    func(r *domain.Sandbox) { r.Project = "__builder" },
	} {
		t.Run(name, func(t *testing.T) {
			svc, d, st, sb := newHib(t)
			_ = st.Update(context.Background(), sb.ID, func(r *domain.Sandbox) error { mut(r); return nil })
			_, err := svc.Hibernate(context.Background(), sb.ID.String())
			if !errors.Is(err, service.ErrHibernateRefused) || d.hibCalls.Load() != 0 {
				t.Fatalf("err=%v calls=%d", err, d.hibCalls.Load())
			}
		})
	}
}

func TestHibernate_StoppedIllegal(t *testing.T) {
	svc, _, _, sb := newHib(t)
	if _, err := svc.Stop(context.Background(), sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Hibernate(context.Background(), sb.ID.String())
	var ite *lifecycle.IllegalTransitionError
	if !errors.As(err, &ite) {
		t.Fatalf("err=%v", err)
	}
}

func TestHibernate_DriverError(t *testing.T) {
	svc, d, st, sb := newHib(t)
	d.hibErr = errors.New("boom")
	_, err := svc.Hibernate(context.Background(), sb.ID.String())
	if !errors.Is(err, service.ErrSnapshotFailed) {
		t.Fatalf("err=%v", err)
	}
	got, _ := st.Get(context.Background(), sb.ID)
	if got.State != domain.Running || d.stops.Load() != 0 {
		t.Fatalf("state=%v stops=%d", got.State, d.stops.Load())
	}
}

func TestHibernate_StoreFailureResumes(t *testing.T) {
	st := newFileStore(t)
	d := &hibDriver{FakeDriver: fake.New()}
	root := t.TempDir()
	fs := &failStore{Store: st}
	svc := service.New(fs, d, lifecycle.New()).WithAuditRoot(func() (string, error) { return root, nil })
	sb, _ := svc.Create(context.Background(), "p", "f", service.CreateOptions{})
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	fs.fail = true
	if _, err := svc.Hibernate(context.Background(), sb.ID.String()); err == nil {
		t.Fatal("want error")
	}
	got, _ := st.Get(context.Background(), sb.ID)
	if got.State != domain.Running || d.resumes.Load() != 1 || d.stops.Load() != 0 {
		t.Fatalf("state=%v resumes=%d stops=%d", got.State, d.resumes.Load(), d.stops.Load())
	}
}

func TestHibernate_ConcurrentSerializes(t *testing.T) {
	svc, d, _, sb := newHib(t)
	var wg sync.WaitGroup
	var already atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := svc.Hibernate(context.Background(), sb.ID.String())
			if err != nil {
				t.Error(err)
			}
			if out.Already {
				already.Add(1)
			}
		}()
	}
	wg.Wait()
	if d.hibCalls.Load() != 1 || already.Load() != 3 {
		t.Fatalf("calls=%d already=%d", d.hibCalls.Load(), already.Load())
	}
}
