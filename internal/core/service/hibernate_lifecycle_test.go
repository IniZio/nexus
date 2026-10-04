package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/statedir"
	"github.com/IniZio/nexus/internal/core/store"
)

// hibernatedFixture returns a Hibernated sandbox whose committed snapshot dir
// sits under <XDG_STATE_HOME>/nexus/supervisors/<id>/hibernate.
func hibernatedFixture(t *testing.T) (*service.Service, store.Store, domain.Sandbox, string, string) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	root := filepath.Join(xdg, "nexus")
	st := newFileStore(t)
	svc := service.New(st, fake.New(), lifecycle.New()).WithAuditRoot(func() (string, error) { return root, nil })
	sb, err := svc.Create(context.Background(), "p", "hib", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	supDir := statedir.SupervisorDir(root, sb.ID)
	hdir := filepath.Join(supDir, "hibernate")
	if err := os.MkdirAll(filepath.Join(hdir, "snap"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hdir, "snap", "COMMITTED"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(context.Background(), sb.ID, func(r *domain.Sandbox) error {
		r.State = domain.Hibernated
		r.HibernateDir = hdir
		r.SnapshotBytes = 100
		r.SnapshotBytesOnDisk = 40
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return svc, st, sb, supDir, hdir
}

func TestStop_Hibernated_DiscardsSnapshot(t *testing.T) {
	svc, st, sb, supDir, hdir := hibernatedFixture(t)
	got, err := svc.Stop(context.Background(), sb.ID.String())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got.State != domain.Stopped || got.HibernateDir != "" || got.SnapshotBytes != 0 || got.SnapshotBytesOnDisk != 0 {
		t.Fatalf("returned: %+v", got)
	}
	rec, _ := st.Get(context.Background(), sb.ID)
	if rec.State != domain.Stopped || rec.HibernateDir != "" || rec.SnapshotBytes != 0 {
		t.Fatalf("record: %+v", rec)
	}
	if _, err := os.Stat(hdir); !os.IsNotExist(err) {
		t.Fatalf("snapshot dir survived stop: %v", err)
	}
	if _, err := os.Stat(supDir); err != nil {
		t.Fatalf("supervisor dir should remain: %v", err)
	}
}

func TestRemove_Hibernated_DeletesSnapshotDir(t *testing.T) {
	svc, _, sb, supDir, hdir := hibernatedFixture(t)
	if err := svc.Remove(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, p := range []string{hdir, supDir} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived Remove: %v", p, err)
		}
	}
}

func TestReap_Hibernated_KeepsCommittedSnapshot(t *testing.T) {
	_, st, sb, supDir, hdir := hibernatedFixture(t)
	stateRoot := filepath.Dir(supDir) // <root>/supervisors
	stateRoot = filepath.Dir(stateRoot)
	idx := service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})
	if _, err := service.Reap(context.Background(), st, idx, true, service.ReapOptions{ProcDir: t.TempDir()}); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hdir, "snap", "COMMITTED")); err != nil {
		t.Fatalf("committed snapshot reaped for %s: %v", sb.ID, err)
	}
}
