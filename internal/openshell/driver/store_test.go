package driver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMemStore_CRUD(t *testing.T) {
	s := NewMemStore()
	r := SandboxRecord{ID: "id1", Name: "test1", Phase: PhaseProvisioning}

	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("id1")
	if !ok || got.Name != "test1" {
		t.Fatalf("Get: got %+v ok=%v", got, ok)
	}
	byName, ok := s.GetByName("test1")
	if !ok || byName.ID != "id1" {
		t.Fatalf("GetByName: got %+v ok=%v", byName, ok)
	}
	list := s.List()
	if len(list) != 1 {
		t.Fatalf("List: want 1 got %d", len(list))
	}
	if !s.Delete("id1") {
		t.Fatal("Delete: expected true")
	}
	if _, ok := s.Get("id1"); ok {
		t.Fatal("Get after delete: expected not found")
	}
}

func TestBboltStore_CRUD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.db")

	s, err := NewBboltStore(path)
	if err != nil {
		t.Fatal(err)
	}

	r := SandboxRecord{
		ID:    "sbx1",
		Name:  "mybox",
		Phase: PhaseProvisioning,
		Spec:  SandboxSpec{Image: "myimage:latest"},
	}
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}

	got, ok := s.Get("sbx1")
	if !ok || got.Name != "mybox" {
		t.Fatalf("Get: %+v", got)
	}
	if s.Delete("sbx1") != true {
		t.Fatal("Delete returned false")
	}
	if _, ok := s.Get("sbx1"); ok {
		t.Fatal("still present after delete")
	}
	s.Close()
}

func TestBboltStore_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.db")

	s1, err := NewBboltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	records := []SandboxRecord{
		{ID: "a", Name: "alpha", Phase: PhaseReady, Spec: SandboxSpec{Image: "img1"}},
		{ID: "b", Name: "beta", Phase: PhaseStopped, LaunchAuthentication: []byte("secret-token")},
	}
	for _, r := range records {
		if err := s1.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	s1.Close()

	s2, err := NewBboltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	list := s2.List()
	if len(list) != 2 {
		t.Fatalf("after restart: want 2 records, got %d", len(list))
	}

	got, ok := s2.Get("a")
	if !ok || got.Phase != PhaseReady || got.Spec.Image != "img1" {
		t.Fatalf("record a: %+v", got)
	}
	gotB, ok := s2.Get("b")
	if !ok || string(gotB.LaunchAuthentication) != "secret-token" {
		t.Fatalf("launch_authentication not persisted: %+v", gotB)
	}

	gotName, ok := s2.GetByName("beta")
	if !ok || gotName.ID != "b" {
		t.Fatalf("GetByName after restart: %+v", gotName)
	}
}

func TestBboltStore_OpenTwice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.db")

	s1, err := NewBboltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
}
