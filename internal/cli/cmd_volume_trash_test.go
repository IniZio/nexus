package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/volumestore"
)

// newTrashTestVS creates a VolumeStore and a dir-kind volume, then trashes it.
// Returns the store, the trash entry name, and the original volume name.
func newTrashTestVS(t *testing.T) (*volumestore.VolumeStore, string, string) {
	t.Helper()
	vs, _ := newVolTestVolumeStore(t)
	ctx := context.Background()
	if _, err := vs.Create(ctx, "myvol", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("create volume: %v", err)
	}
	entry, err := vs.Trash(ctx, "myvol")
	if err != nil {
		t.Fatalf("trash volume: %v", err)
	}
	return vs, entry, "myvol"
}

// TestVolumeLsTrash_ListsEntry verifies --trash shows a trashed entry.
func TestVolumeLsTrash_ListsEntry(t *testing.T) {
	vs, entry, _ := newTrashTestVS(t)
	out, buf := newVolTestOutput()

	if err := runVolumeLs(context.Background(), []string{"--trash"}, out, vs); err != nil {
		t.Fatalf("runVolumeLs --trash: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, entry) {
		t.Errorf("expected entry %q in output; got: %s", entry, got)
	}
	if !strings.Contains(got, "myvol") {
		t.Errorf("expected original name 'myvol' in output; got: %s", got)
	}
}

// TestVolumeLsTrash_Empty verifies --trash on empty trash prints "no trashed volumes".
func TestVolumeLsTrash_Empty(t *testing.T) {
	vs, _ := newVolTestVolumeStore(t)
	out, buf := newVolTestOutput()

	if err := runVolumeLs(context.Background(), []string{"--trash"}, out, vs); err != nil {
		t.Fatalf("runVolumeLs --trash: %v", err)
	}
	if !strings.Contains(buf.String(), "no trashed volumes") {
		t.Errorf("expected 'no trashed volumes'; got: %s", buf.String())
	}
}

// TestVolumeLsTrash_JSONShape verifies --trash JSON output shape.
func TestVolumeLsTrash_JSONShape(t *testing.T) {
	vs, entry, _ := newTrashTestVS(t)
	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, true) // JSON mode

	if err := runVolumeLs(context.Background(), []string{"--trash"}, out, vs); err != nil {
		t.Fatalf("runVolumeLs --trash JSON: %v", err)
	}

	var envelope struct {
		Kind string `json:"kind"`
		Data []struct {
			Name      string `json:"name"`
			Original  string `json:"original"`
			TrashedAt string `json:"trashed_at"`
			Expires   string `json:"expires"`
		} `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("decode JSON: %v\nraw: %s", err, buf.String())
	}
	if envelope.Kind != "volume.trash.list" {
		t.Errorf("kind = %q; want volume.trash.list", envelope.Kind)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("want 1 entry; got %d", len(envelope.Data))
	}
	if envelope.Data[0].Name != entry {
		t.Errorf("name = %q; want %q", envelope.Data[0].Name, entry)
	}
	if envelope.Data[0].Original != "myvol" {
		t.Errorf("original = %q; want myvol", envelope.Data[0].Original)
	}
	if envelope.Data[0].Expires == "" {
		t.Error("expires must be non-empty")
	}
}

// TestVolumeRestore_HappyPath verifies restore brings back the volume.
func TestVolumeRestore_HappyPath(t *testing.T) {
	vs, entry, _ := newTrashTestVS(t)
	out, buf := newVolTestOutput()

	if err := runVolumeRestoreWith(context.Background(), []string{entry}, out, vs); err != nil {
		t.Fatalf("runVolumeRestoreWith: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "myvol") {
		t.Errorf("expected restored volume name 'myvol'; got: %s", got)
	}

	// Volume should be live again.
	records, err := vs.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, r := range records {
		if r.Name == "myvol" {
			found = true
		}
	}
	if !found {
		t.Error("volume 'myvol' not in live list after restore")
	}
}

// TestVolumeRestore_AsFlag verifies --as restores under a different name.
func TestVolumeRestore_AsFlag(t *testing.T) {
	vs, entry, _ := newTrashTestVS(t)
	out, buf := newVolTestOutput()

	if err := runVolumeRestoreWith(context.Background(), []string{"--as", "renamed-vol", entry}, out, vs); err != nil {
		t.Fatalf("runVolumeRestoreWith --as: %v", err)
	}

	if !strings.Contains(buf.String(), "renamed-vol") {
		t.Errorf("output should contain 'renamed-vol'; got: %s", buf.String())
	}
}

// TestVolumeRestore_MissingEntry verifies error on missing trash entry.
func TestVolumeRestore_MissingEntry(t *testing.T) {
	vs, _ := newVolTestVolumeStore(t)
	out, _ := newVolTestOutput()

	err := runVolumeRestoreWith(context.Background(), []string{"nonexistent@20260101T000000Z"}, out, vs)
	if err == nil {
		t.Fatal("expected error for missing trash entry; got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found'; got: %v", err)
	}
}

// TestVolumeRestore_NoArgs verifies usage error when entry is omitted.
func TestVolumeRestore_NoArgs(t *testing.T) {
	vs, _ := newVolTestVolumeStore(t)
	out, _ := newVolTestOutput()

	err := runVolumeRestoreWith(context.Background(), []string{}, out, vs)
	if err == nil {
		t.Fatal("expected usage error; got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Errorf("expected *UsageError; got %T: %v", err, err)
	}
}
