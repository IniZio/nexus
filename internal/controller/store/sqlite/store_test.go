package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/store/sqlite"
	"github.com/IniZio/nexus/internal/controller/storetest"
)

func newStore(t *testing.T) controller.TaskStore {
	t.Helper()
	dir := t.TempDir()
	s, err := sqlite.Open(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLiteTaskStoreContract(t *testing.T) {
	storetest.RunTaskStoreContract(t, newStore)
}

func TestTransitionCASRejectsStale(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ref := controller.NewThreadRef("T1", "C1", "ts1")

	task := controller.Task{
		ThreadRef: ref,
		Status:    controller.StatusIdle,
	}
	if err := s.Upsert(ctx, task); err != nil {
		t.Fatal(err)
	}

	if err := s.Transition(ctx, ref, controller.StatusWorking, controller.StatusIdle, 2); err != controller.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	got, err := s.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != controller.StatusIdle {
		t.Fatalf("status mutated unexpectedly: %v", got.Status)
	}
}

func TestReopenPersists(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tasks.db")

	ref := controller.NewThreadRef("T1", "C1", "persist-ts")
	task := controller.Task{
		ThreadRef: ref,
		Project:   "my-project",
		Status:    controller.StatusWorking,
	}

	func() {
		s, err := sqlite.Open(dbPath)
		if err != nil {
			t.Fatalf("open1: %v", err)
		}
		defer s.Close()
		if err := s.Upsert(context.Background(), task); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}()

	s2, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer s2.Close()

	got, err := s2.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.Project != "my-project" {
		t.Fatalf("Project = %q, want %q", got.Project, "my-project")
	}
	if got.Status != controller.StatusWorking {
		t.Fatalf("Status = %v, want %v", got.Status, controller.StatusWorking)
	}
}
