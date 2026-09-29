package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/artifact"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
)

// tapSnapDriver reports every snapshot as a tap snapshot and records removals.
type tapSnapDriver struct {
	*fake.FakeDriver
	tap     bool
	removed []artifact.SnapshotID
}

func (d *tapSnapDriver) SnapshotNetMode(artifact.Snapshot) (domain.NetMode, error) {
	if d.tap {
		return "", driver.ErrTapSnapshot
	}
	return "", nil
}

func (d *tapSnapDriver) RemoveSnapshot(id artifact.SnapshotID) error {
	d.removed = append(d.removed, id)
	return d.FakeDriver.RemoveSnapshot(id)
}

func countCalls(f *fake.FakeDriver, k fake.CallKind) int {
	n := 0
	for _, c := range f.Calls() {
		if c.Kind == k {
			n++
		}
	}
	return n
}

func TestFork_TapSnapshotCleansTransient(t *testing.T) {
	st := newTestStore(t)
	drv := &tapSnapDriver{FakeDriver: fake.New(), tap: true}
	svc := service.New(st, drv, lifecycle.New())
	sb := makeSandbox("tap-parent", "test", domain.Running)
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Fork(context.Background(), sb.ID.String(), 1)
	if !errors.Is(err, driver.ErrTapSnapshot) {
		t.Fatalf("Fork err = %v, want ErrTapSnapshot", err)
	}
	if len(drv.removed) != 1 {
		t.Fatalf("removed snapshots = %v, want exactly the transient one", drv.removed)
	}
	if _, ok := drv.Snapshot(drv.removed[0]); ok {
		t.Error("transient snapshot still present after failed fork")
	}
}

func TestFork_DriverFailureCleansTransient(t *testing.T) {
	st := newTestStore(t)
	drv := &tapSnapDriver{FakeDriver: fake.New()}
	svc := service.New(st, drv, lifecycle.New())
	drv.SetForkError(errors.New("boom"))
	sb := makeSandbox("fail-parent", "test", domain.Running)
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Fork(context.Background(), sb.ID.String(), 1); err == nil {
		t.Fatal("Fork: want error")
	}
	if len(drv.removed) != 1 {
		t.Fatalf("removed snapshots = %v, want 1", drv.removed)
	}
}

func legacyParent(t *testing.T, netnsPID int) (*service.Service, *fake.FakeDriver, domain.Sandbox) {
	t.Helper()
	st := newTestStore(t)
	f := fake.New()
	svc := service.New(st, f, lifecycle.New())
	sb := makeSandbox("legacy-parent", "test", domain.Running)
	sb.NetnsChildPID = netnsPID
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatal(err)
	}
	return svc, f, sb
}

func TestSnapshotAndFork_RefuseLegacyNIC(t *testing.T) {
	svc, f, sb := legacyParent(t, 4242)
	ref := sb.ID.String()
	want := domain.LegacyNICMessage(ref)
	if _, err := svc.Snapshot(context.Background(), ref); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Snapshot err = %v, want %q", err, want)
	}
	if _, err := svc.Fork(context.Background(), ref, 1); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Fork err = %v, want %q", err, want)
	}
	if n := countCalls(f, fake.CallTakeSnapshot); n != 0 {
		t.Errorf("TakeSnapshot called %d times, want 0", n)
	}
}

func TestSnapshot_NetlessStillWorks(t *testing.T) {
	svc, f, sb := legacyParent(t, 0)
	if _, err := svc.Snapshot(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Snapshot netless: %v", err)
	}
	if n := countCalls(f, fake.CallTakeSnapshot); n != 1 {
		t.Errorf("TakeSnapshot calls = %d, want 1", n)
	}
}
