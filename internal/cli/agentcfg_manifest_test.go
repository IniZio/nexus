package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/service"
)

func TestAppendStagingExcludeMounts_NoFile(t *testing.T) {
	dir := t.TempDir()
	extra := []service.ResolvedUserMount{
		{HostPath: "/host/a", GuestPath: "/guest/a", StagingGuestPath: "/stage/a"},
	}
	if err := appendStagingExcludeMounts(dir, extra); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := readManifest(t, dir)
	if len(got.Mounts) != 1 || got.Mounts[0].HostPath != "/host/a" {
		t.Fatalf("unexpected mounts: %+v", got.Mounts)
	}
}

func TestAppendStagingExcludeMounts_ExistingManifest(t *testing.T) {
	dir := t.TempDir()
	existing := service.UserMountManifest{
		Mounts: []service.ResolvedUserMount{
			{HostPath: "/host/orig", GuestPath: "/guest/orig", StagingGuestPath: "/stage/orig"},
		},
	}
	writeManifest(t, dir, existing)

	extra := []service.ResolvedUserMount{
		{HostPath: "/host/new", GuestPath: "/guest/new", StagingGuestPath: "/stage/new"},
	}
	if err := appendStagingExcludeMounts(dir, extra); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := readManifest(t, dir)
	if len(got.Mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d: %+v", len(got.Mounts), got.Mounts)
	}
	if got.Mounts[0].HostPath != "/host/orig" {
		t.Errorf("original mount not preserved: %+v", got.Mounts[0])
	}
	if got.Mounts[1].HostPath != "/host/new" {
		t.Errorf("extra mount missing: %+v", got.Mounts[1])
	}
}

func TestAppendStagingExcludeMounts_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	corrupt := []byte(`not valid json`)
	if err := os.WriteFile(filepath.Join(dir, "usermounts.json"), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	extra := []service.ResolvedUserMount{
		{HostPath: "/host/x", GuestPath: "/guest/x", StagingGuestPath: "/stage/x"},
	}
	err := appendStagingExcludeMounts(dir, extra)
	if err == nil {
		t.Fatal("expected error for corrupt JSON, got nil")
	}
	// File must be unchanged.
	got, _ := os.ReadFile(filepath.Join(dir, "usermounts.json"))
	if string(got) != string(corrupt) {
		t.Errorf("corrupt file was overwritten; want unchanged, got %q", string(got))
	}
}

func TestAppendStagingExcludeMounts_UnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; chmod 000 has no effect")
	}
	dir := t.TempDir()
	// Create a directory at the usermounts.json path to make it unreadable as a file.
	blocked := filepath.Join(dir, "usermounts.json")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	extra := []service.ResolvedUserMount{
		{HostPath: "/host/y", GuestPath: "/guest/y", StagingGuestPath: "/stage/y"},
	}
	err := appendStagingExcludeMounts(dir, extra)
	if err == nil {
		t.Fatal("expected error for unreadable path, got nil")
	}
	// Confirm the directory was not replaced.
	info, statErr := os.Stat(blocked)
	if statErr != nil {
		t.Fatalf("stat after failed append: %v", statErr)
	}
	if !info.IsDir() {
		t.Error("usermounts.json path was overwritten despite read error")
	}
}

// helpers

func writeManifest(t *testing.T, dir string, m service.UserMountManifest) {
	t.Helper()
	if err := service.WriteUserMountManifest(dir, m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
}

func readManifest(t *testing.T, dir string) service.UserMountManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "usermounts.json"))
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	var m service.UserMountManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("readManifest unmarshal: %v", err)
	}
	return m
}
