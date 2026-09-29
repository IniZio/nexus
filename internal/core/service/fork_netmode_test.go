package service_test

import (
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/artifact"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

// snapModeDriver reports a fixed NIC mode for every snapshot.
type snapModeDriver struct {
	*fake.FakeDriver
	mode domain.NetMode
}

func (d *snapModeDriver) SnapshotNetMode(artifact.Snapshot) (domain.NetMode, error) {
	return d.mode, nil
}

var _ driver.SnapshotNetModer = (*snapModeDriver)(nil)

func newSnapModeSvc(t *testing.T, snapMode domain.NetMode) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return service.New(st, &snapModeDriver{FakeDriver: fake.New(), mode: snapMode}, lifecycle.New())
}

func TestForkRefusesSnapshotNetModeMismatch(t *testing.T) {
	svc := newSnapModeSvc(t, domain.NetModeVhostUser)
	ctx := ctx()
	parent, err := svc.Create(ctx, "proj", "p", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, parent.ID.String()); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Fork(ctx, parent.ID.String(), 1)
	if err == nil || !strings.Contains(err.Error(), "net mode") {
		t.Fatalf("Fork err = %v, want net mode mismatch", err)
	}
}
