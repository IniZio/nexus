package spotlight

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

type fakeSpotlightRepo struct {
	rows          []store.SpotlightForwardRow
	upserted      []store.SpotlightForwardRow
	deleted       []string
	listErr       error
	upsertErr     error
	deleteErr     error
	upsertErrOnce bool
	deleteErrOnce bool
}

func (f *fakeSpotlightRepo) UpsertSpotlightForwardRow(row store.SpotlightForwardRow) error {
	f.upserted = append(f.upserted, row)
	if f.upsertErr != nil {
		if f.upsertErrOnce {
			err := f.upsertErr
			f.upsertErr = nil
			return err
		}
		return f.upsertErr
	}
	f.rows = append(f.rows, row)
	return nil
}

func (f *fakeSpotlightRepo) DeleteSpotlightForwardRow(id string) error {
	f.deleted = append(f.deleted, id)
	if f.deleteErr != nil {
		if f.deleteErrOnce {
			err := f.deleteErr
			f.deleteErr = nil
			return err
		}
		return f.deleteErr
	}
	next := make([]store.SpotlightForwardRow, 0, len(f.rows))
	for _, row := range f.rows {
		if row.ID != id {
			next = append(next, row)
		}
	}
	f.rows = next
	return nil
}

func (f *fakeSpotlightRepo) ListSpotlightForwardRows() ([]store.SpotlightForwardRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.SpotlightForwardRow, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func TestExpose_FailsOnLocalPortCollision(t *testing.T) {
	mgr := NewManager()
	localPort := freeTCPPort(t)
	_, err := mgr.Expose(context.Background(), ExposeSpec{WorkspaceID: "ws-1", LocalPort: localPort, RemotePort: 5173})
	if err != nil {
		t.Fatalf("expected first expose to succeed, got %v", err)
	}

	_, err = mgr.Expose(context.Background(), ExposeSpec{WorkspaceID: "ws-2", LocalPort: localPort, RemotePort: 8000})
	if err == nil {
		t.Fatal("expected second expose to fail due to port collision")
	}
}

func TestListAndClose(t *testing.T) {
	mgr := NewManager()
	localPort := freeTCPPort(t)
	fwd, err := mgr.Expose(context.Background(), ExposeSpec{WorkspaceID: "ws-1", LocalPort: localPort, RemotePort: 5173})
	if err != nil {
		t.Fatalf("unexpected expose error: %v", err)
	}

	list := mgr.List("ws-1")
	if len(list) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(list))
	}

	if !mgr.Close(fwd.ID) {
		t.Fatal("expected close to succeed")
	}

	list = mgr.List("ws-1")
	if len(list) != 0 {
		t.Fatalf("expected 0 forwards, got %d", len(list))
	}
}

func TestManager_HydratesAndPersistsViaRepository(t *testing.T) {
	t.Run("hydrates from repository rows", func(t *testing.T) {
		created := time.Date(2026, time.April, 9, 12, 0, 0, 0, time.UTC)
		payload, err := json.Marshal(&Forward{
			ID:          "spot-1",
			WorkspaceID: "ws-1",
			Service:     "api",
			RemotePort:  8000,
			LocalPort:   18000,
			Host:        "127.0.0.1",
			CreatedAt:   created,
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}

		repo := &fakeSpotlightRepo{rows: []store.SpotlightForwardRow{{
			ID:          "spot-1",
			WorkspaceID: "ws-1",
			LocalPort:   18000,
			Payload:     payload,
			CreatedAt:   created,
		}}}

		mgr, err := NewManagerWithRepository(repo)
		if err != nil {
			t.Fatalf("new manager with repo: %v", err)
		}

		all := mgr.List("")
		if len(all) != 1 {
			t.Fatalf("expected 1 hydrated forward, got %d", len(all))
		}
		if all[0].ID != "spot-1" || all[0].WorkspaceID != "ws-1" {
			t.Fatalf("unexpected hydrated forward: %#v", all[0])
		}
	})

	t.Run("expose persists row to repository", func(t *testing.T) {
		repo := &fakeSpotlightRepo{}
		mgr, err := NewManagerWithRepository(repo)
		if err != nil {
			t.Fatalf("new manager with repo: %v", err)
		}

		fwd, err := mgr.Expose(context.Background(), ExposeSpec{
			WorkspaceID: "ws-1",
			Service:     "api",
			RemotePort:  8000,
			LocalPort:   freeTCPPort(t),
		})
		if err != nil {
			t.Fatalf("expose: %v", err)
		}
		if len(repo.upserted) != 1 {
			t.Fatalf("expected 1 upsert call, got %d", len(repo.upserted))
		}
		if repo.upserted[0].ID != fwd.ID {
			t.Fatalf("expected upsert ID %q, got %q", fwd.ID, repo.upserted[0].ID)
		}

		repo.upsertErr = errors.New("boom")
		repo.upsertErrOnce = true
		_, err = mgr.Expose(context.Background(), ExposeSpec{
			WorkspaceID: "ws-1",
			Service:     "web",
			RemotePort:  3000,
			LocalPort:   freeTCPPort(t),
		})
		if err == nil {
			t.Fatal("expected expose to fail when upsert fails")
		}
		if len(mgr.List("")) != 1 {
			t.Fatal("expected failed expose to rollback in-memory state")
		}
	})

	t.Run("close deletes row from repository", func(t *testing.T) {
		repo := &fakeSpotlightRepo{}
		mgr, err := NewManagerWithRepository(repo)
		if err != nil {
			t.Fatalf("new manager with repo: %v", err)
		}

		fwd, err := mgr.Expose(context.Background(), ExposeSpec{
			WorkspaceID: "ws-1",
			Service:     "api",
			RemotePort:  8000,
			LocalPort:   freeTCPPort(t),
		})
		if err != nil {
			t.Fatalf("expose: %v", err)
		}

		if !mgr.Close(fwd.ID) {
			t.Fatal("expected close to succeed")
		}
		if len(repo.deleted) != 1 || repo.deleted[0] != fwd.ID {
			t.Fatalf("expected delete call for %q, got %#v", fwd.ID, repo.deleted)
		}

		fwd, err = mgr.Expose(context.Background(), ExposeSpec{
			WorkspaceID: "ws-1",
			Service:     "api",
			RemotePort:  8001,
			LocalPort:   freeTCPPort(t),
		})
		if err != nil {
			t.Fatalf("second expose: %v", err)
		}

		repo.deleteErr = errors.New("delete failed")
		repo.deleteErrOnce = true
		if mgr.Close(fwd.ID) {
			t.Fatal("expected close to fail when repository delete fails")
		}
		if len(mgr.List("")) != 1 {
			t.Fatal("expected close failure to restore in-memory state")
		}
	})
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free tcp port: %v", err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("expected tcp address")
	}
	return addr.Port
}
