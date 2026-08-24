package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// TestBuildUserMountManifest_ExistingDir verifies that a mount whose host dir
// exists is included and direct-mounted (overlay=false) when guest is not under /root/.claude/.
func TestBuildUserMountManifest_ExistingDir(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got := service.BuildUserMountManifest(home, []string{dir + ":/root/.local/bin"})
	if len(got.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(got.Mounts))
	}
	m := got.Mounts[0]
	if m.HostPath != dir {
		t.Errorf("HostPath = %q, want %q", m.HostPath, dir)
	}
	if m.GuestPath != "/root/.local/bin" {
		t.Errorf("GuestPath = %q, want /root/.local/bin", m.GuestPath)
	}
	if m.Overlay {
		t.Errorf("expected Overlay=false for non-.claude path")
	}
	if m.StagingGuestPath != m.GuestPath {
		t.Errorf("non-overlay row: StagingGuestPath %q != GuestPath %q", m.StagingGuestPath, m.GuestPath)
	}
}

// TestBuildUserMountManifest_AbsentDirSkipped verifies that a mount whose host
// dir does not exist is silently skipped.
func TestBuildUserMountManifest_AbsentDirSkipped(t *testing.T) {
	home := t.TempDir()
	got := service.BuildUserMountManifest(home, []string{home + "/nonexistent:/root/.local/bin"})
	if len(got.Mounts) != 0 {
		t.Errorf("expected 0 mounts for absent dir, got %d", len(got.Mounts))
	}
}

// TestBuildUserMountManifest_OverlayForClaude verifies that guest paths under
// /root/.claude/ get Overlay=true and a staging path.
func TestBuildUserMountManifest_OverlayForClaude(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got := service.BuildUserMountManifest(home, []string{dir + ":/root/.claude/plugins:ro"})
	if len(got.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(got.Mounts))
	}
	m := got.Mounts[0]
	if !m.Overlay {
		t.Errorf("expected Overlay=true for /root/.claude/ path")
	}
	const wantStaging = "/run/nexus3/usermount/plugins"
	if m.StagingGuestPath != wantStaging {
		t.Errorf("StagingGuestPath = %q, want %q", m.StagingGuestPath, wantStaging)
	}
}

// TestBuildUserMountManifest_TildeExpansion verifies that ~ is expanded against hostHome.
func TestBuildUserMountManifest_TildeExpansion(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got := service.BuildUserMountManifest(home, []string{"~/.local/bin:/root/.local/bin"})
	if len(got.Mounts) != 1 {
		t.Fatalf("expected 1 mount after ~ expansion, got %d", len(got.Mounts))
	}
	if got.Mounts[0].HostPath != dir {
		t.Errorf("HostPath = %q, want %q", got.Mounts[0].HostPath, dir)
	}
}

// TestBuildUserMountManifest_EmptyMounts verifies an empty input returns no mounts.
func TestBuildUserMountManifest_EmptyMounts(t *testing.T) {
	home := t.TempDir()
	got := service.BuildUserMountManifest(home, nil)
	if len(got.Mounts) != 0 {
		t.Errorf("expected 0 mounts, got %d", len(got.Mounts))
	}
	if got.HostHome != home {
		t.Errorf("HostHome = %q, want %q", got.HostHome, home)
	}
}

// TestWriteUserMountManifest_RoundTrip verifies the manifest writes to
// usermounts.json at mode 0o600 and round-trips through JSON faithfully.
func TestWriteUserMountManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := service.UserMountManifest{
		HostHome: "$HOME",
		Mounts: []service.ResolvedUserMount{
			{
				HostPath:         "$HOME/.local/bin",
				GuestPath:        "/root/.local/bin",
				Overlay:          false,
				StagingGuestPath: "/root/.local/bin",
			},
			{
				HostPath:         "$HOME/.claude/plugins",
				GuestPath:        "/root/.claude/plugins",
				Overlay:          true,
				StagingGuestPath: "/run/nexus3/usermount/plugins",
			},
		},
	}
	if err := service.WriteUserMountManifest(dir, m); err != nil {
		t.Fatalf("WriteUserMountManifest: %v", err)
	}

	path := filepath.Join(dir, "usermounts.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat usermounts.json: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %04o, want 0600", perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read usermounts.json: %v", err)
	}
	var got service.UserMountManifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.HostHome != m.HostHome {
		t.Errorf("HostHome = %q, want %q", got.HostHome, m.HostHome)
	}
	if len(got.Mounts) != len(m.Mounts) {
		t.Fatalf("Mounts len = %d, want %d", len(got.Mounts), len(m.Mounts))
	}
}
