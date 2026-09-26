package volumestore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTrashStore(t *testing.T) *VolumeStore {
	t.Helper()
	root := filepath.Join(t.TempDir(), "volumes")
	return New(root)
}

func mustCreateDir(t *testing.T, s *VolumeStore, name string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Create(ctx, name, KindDir, 0, ""); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

func TestTrash_ShowsInListTrash_HiddenFromList(t *testing.T) {
	s := newTrashStore(t)
	mustCreateDir(t, s, "myvol")

	entry, err := s.Trash(context.Background(), "myvol")
	if err != nil {
		t.Fatalf("Trash: %v", err)
	}

	trash, err := s.ListTrash(context.Background())
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(trash) != 1 || trash[0].Name != entry {
		t.Fatalf("want 1 entry %s; got %+v", entry, trash)
	}
	if trash[0].Original != "myvol" {
		t.Errorf("Original = %q; want myvol", trash[0].Original)
	}

	live, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range live {
		if r.Name == "myvol" {
			t.Error("myvol still visible in List after Trash")
		}
	}
}

func TestTrash_Restore_RoundTrip(t *testing.T) {
	s := newTrashStore(t)
	mustCreateDir(t, s, "myvol")

	dataFile := filepath.Join(s.DataPath("myvol"), "sentinel.txt")
	if err := os.WriteFile(dataFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	entry, err := s.Trash(context.Background(), "myvol")
	if err != nil {
		t.Fatalf("Trash: %v", err)
	}

	if _, err := s.Restore(context.Background(), entry, ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	restored := filepath.Join(s.DataPath("myvol"), "sentinel.txt")
	got, err := os.ReadFile(restored)
	if err != nil {
		t.Fatalf("read restored sentinel: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("sentinel content = %q; want hello", got)
	}

	trash, err := s.ListTrash(context.Background())
	if err != nil {
		t.Fatalf("ListTrash after restore: %v", err)
	}
	if len(trash) != 0 {
		t.Errorf("expected empty trash after restore; got %+v", trash)
	}
}

func TestTrash_Restore_RefusesWhenLiveVolumeExists(t *testing.T) {
	s := newTrashStore(t)
	mustCreateDir(t, s, "myvol")

	entry, err := s.Trash(context.Background(), "myvol")
	if err != nil {
		t.Fatalf("Trash: %v", err)
	}

	mustCreateDir(t, s, "myvol")

	if _, err := s.Restore(context.Background(), entry, ""); err == nil {
		t.Error("Restore should refuse when live volume exists")
	}
}

func TestExpireTrash_DeletesOldOnly(t *testing.T) {
	s := newTrashStore(t)
	mustCreateDir(t, s, "oldvol")
	mustCreateDir(t, s, "newvol")

	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	trashNow = func() time.Time { return old }
	if _, err := s.Trash(context.Background(), "oldvol"); err != nil {
		t.Fatalf("Trash oldvol: %v", err)
	}

	fresh := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	trashNow = func() time.Time { return fresh }
	if _, err := s.Trash(context.Background(), "newvol"); err != nil {
		t.Fatalf("Trash newvol: %v", err)
	}

	trashNow = time.Now
	t.Cleanup(func() { trashNow = time.Now })

	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	deleted, err := s.ExpireTrash(context.Background(), TrashGrace, now)
	if err != nil {
		t.Fatalf("ExpireTrash: %v", err)
	}

	if len(deleted) != 1 {
		t.Fatalf("expected 1 deleted; got %v", deleted)
	}

	trash, err := s.ListTrash(context.Background())
	if err != nil {
		t.Fatalf("ListTrash after expire: %v", err)
	}
	if len(trash) != 1 || trash[0].Original != "newvol" {
		t.Errorf("expected newvol to remain; got %+v", trash)
	}
}

func TestTrash_MissingVolume_Errors(t *testing.T) {
	s := newTrashStore(t)
	_, err := s.Trash(context.Background(), "ghost")
	if err == nil {
		t.Error("expected error trashing non-existent volume")
	}
}

func TestTrash_TraversalEntry_Rejected(t *testing.T) {
	s := newTrashStore(t)
	mustCreateDir(t, s, "myvol")
	if _, err := s.Trash(context.Background(), "myvol"); err != nil {
		t.Fatalf("Trash: %v", err)
	}

	badNames := []string{"../evil", "a/b", ".."}
	for _, bad := range badNames {
		if _, err := s.Restore(context.Background(), bad, ""); err == nil {
			t.Errorf("Restore(%q) should have been rejected", bad)
		}
	}
}

func TestListTrash_EmptyWhenNoDotTrashDir(t *testing.T) {
	s := newTrashStore(t)
	trash, err := s.ListTrash(context.Background())
	if err != nil {
		t.Fatalf("ListTrash on fresh store: %v", err)
	}
	if len(trash) != 0 {
		t.Errorf("expected empty; got %+v", trash)
	}
}

func TestTrash_SameSecondDisambiguation(t *testing.T) {
	s := newTrashStore(t)
	mustCreateDir(t, s, "vol1")
	mustCreateDir(t, s, "vol2")

	fixed := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	trashNow = func() time.Time { return fixed }
	t.Cleanup(func() { trashNow = time.Now })

	e1, err := s.Trash(context.Background(), "vol1")
	if err != nil {
		t.Fatalf("Trash vol1: %v", err)
	}

	mustCreateDir(t, s, "vol1")
	e2, err := s.Trash(context.Background(), "vol1")
	if err != nil {
		t.Fatalf("Trash vol1 again: %v", err)
	}

	if e1 == e2 {
		t.Errorf("same-second trash entries must be distinct: both = %s", e1)
	}

	trash, err := s.ListTrash(context.Background())
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(trash) != 2 {
		t.Errorf("expected 2 trash entries; got %+v", trash)
	}
	_ = e2
	_ = e1
}
