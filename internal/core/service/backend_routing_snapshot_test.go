package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/artifact"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
)

// capDriver adds snapshot/fork/pause capabilities and counts calls. The
// default driver in these tests has none, so a mis-routed call to it fails.
type capDriver struct {
	routedDriver
	snaps, forks, pauses, resumes int
}

func (d *capDriver) TakeSnapshot(_ context.Context, id domain.SandboxID, kind artifact.SnapshotKind) (artifact.Snapshot, error) {
	d.snaps++
	return artifact.Snapshot{ID: artifact.SnapshotID("s-" + id.String()), SandboxID: id, Kind: kind, CommitMarker: "ok", CreatedAt: time.Now()}, nil
}

func (d *capDriver) ForkFrom(_ context.Context, _ artifact.Snapshot, ids []domain.SandboxID) ([]string, error) {
	d.forks++
	out := make([]string, len(ids))
	for i := range ids {
		out[i] = "iid-fork"
	}
	return out, nil
}

func (d *capDriver) Pause(context.Context, domain.SandboxID) error  { d.pauses++; return nil }
func (d *capDriver) Resume(context.Context, domain.SandboxID) error { d.resumes++; return nil }

var (
	_ driver.Snapshotter  = (*capDriver)(nil)
	_ driver.Forker       = (*capDriver)(nil)
	_ driver.PauseResumer = (*capDriver)(nil)
)

func newCapRouted(t *testing.T) (*service.Service, *routedDriver, *capDriver, string) {
	t.Helper()
	def := &routedDriver{noGuestDialDriver: noGuestDialDriver{name: routeDefault}}
	other := &capDriver{routedDriver: routedDriver{noGuestDialDriver: noGuestDialDriver{name: routeOther}}}
	art, err := artifact.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(newFileStore(t), def, lifecycle.New()).
		WithBackendDriverFactory(func(string) (driver.Driver, error) { return other, nil }).
		WithArtifacts(art)
	sb, err := svc.Create(context.Background(), "p", "sp", service.CreateOptions{Backend: routeOther})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatal(err)
	}
	return svc, def, other, sb.ID.String()
}

func TestBackendRouting_SnapshotUsesSandboxDriver(t *testing.T) {
	svc, _, other, ref := newCapRouted(t)
	if _, err := svc.Snapshot(context.Background(), ref); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if other.snaps != 1 {
		t.Fatalf("other.snaps=%d", other.snaps)
	}
}

func TestBackendRouting_ForkUsesSandboxDriver(t *testing.T) {
	svc, _, other, ref := newCapRouted(t)
	kids, err := svc.Fork(context.Background(), ref, 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if other.snaps != 1 || other.forks != 1 {
		t.Fatalf("other.snaps=%d forks=%d", other.snaps, other.forks)
	}
	if kids[0].Backend != routeOther {
		t.Fatalf("child Backend=%q, want %q", kids[0].Backend, routeOther)
	}
}

func TestBackendRouting_PauseResumeUseSandboxDriver(t *testing.T) {
	svc, _, other, ref := newCapRouted(t)
	if _, err := svc.Pause(context.Background(), ref); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := svc.Resume(context.Background(), ref); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if other.pauses != 1 || other.resumes != 1 {
		t.Fatalf("pauses=%d resumes=%d", other.pauses, other.resumes)
	}
}
