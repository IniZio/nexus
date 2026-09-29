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

func TestForkAndRestoreInheritNetModeFromSnapshot(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		recorded domain.NetMode
		snapMode domain.NetMode
	}{
		{"tap ignores env vhost-user", "vhost-user", "", domain.NetModeTap},
		{"tap ignores env none", "none", "", domain.NetModeTap},
		{"explicit tap record", "vhost-user", domain.NetModeTap, domain.NetModeTap},
		{"vhost-user ignores env tap", "tap", domain.NetModeVhostUser, domain.NetModeVhostUser},
		{"vhost-user without env", "", domain.NetModeVhostUser, domain.NetModeVhostUser},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("NEXUS_NET_MODE", c.env)
			svc := newSnapModeSvc(t, c.snapMode)
			ctx := ctx()
			parent, err := svc.Create(ctx, "proj", "p", service.CreateOptions{NetMode: c.recorded})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Start(ctx, parent.ID.String()); err != nil {
				t.Fatal(err)
			}
			kids, err := svc.Fork(ctx, parent.ID.String(), 2)
			if err != nil {
				t.Fatalf("Fork: %v", err)
			}
			for _, k := range kids {
				if k.NetMode != c.recorded {
					t.Errorf("fork child NetMode = %q, want %q", k.NetMode, c.recorded)
				}
			}

			aStore := makeArtifactStore(t)
			svc = svc.WithArtifacts(aStore)
			writeSnapWithOrigin(t, aStore, "netmode-snap-00000000000", parent.ID, artifact.KindRetained)
			kids, err = svc.RestoreFromSnapshot(ctx, "netmode-snap-00000000000", 2)
			if err != nil {
				t.Fatalf("RestoreFromSnapshot: %v", err)
			}
			for _, k := range kids {
				if k.NetMode != c.recorded {
					t.Errorf("restore child NetMode = %q, want %q", k.NetMode, c.recorded)
				}
			}
		})
	}
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
