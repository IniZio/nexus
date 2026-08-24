package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// TestUserMountTable_Rows verifies the table's declared rows resolve in order
// when all host dirs exist. Mutation guard: if a row is deleted or reordered,
// this fails. Update the expected list when the table is intentionally extended.
func TestUserMountTable_Rows(t *testing.T) {
	home := t.TempDir()
	// The declared rows, in table order.
	want := []struct{ rel, guest string }{
		{".claude/plugins", "/root/.claude/plugins"},
		{".local/bin", "/root/.local/bin"},
		{".local/share/groundwork", "/root/.local/share/groundwork"},
		{".codegraph", "/root/.codegraph"},
		{".local/share/mise", "/root/.local/share/mise"},
		{".local/share/uv", "/root/.local/share/uv"},
		{".bun", "/root/.bun"},
		{".vscode-server/extensions", "/root/.vscode-server/extensions"},
	}
	for _, w := range want {
		if err := os.MkdirAll(filepath.Join(home, w.rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := service.ResolveUserMounts(home)
	if len(got) != len(want) {
		t.Fatalf("table resolved %d rows when all dirs exist, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].GuestPath != w.guest {
			t.Errorf("row %d GuestPath = %q, want %q", i, got[i].GuestPath, w.guest)
		}
	}
}

// TestResolveUserMounts_SkipsMissing checks that absent host dirs are silently
// dropped. Mutation guard: if the existence check is removed all rows appear.
func TestResolveUserMounts_SkipsMissing(t *testing.T) {
	home := t.TempDir()
	// Create only .local/bin; the other two are absent.
	if err := os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := service.ResolveUserMounts(home)
	if len(got) != 1 {
		t.Fatalf("expected 1 resolved mount (.local/bin), got %d", len(got))
	}
	if got[0].GuestPath != "/root/.local/bin" {
		t.Errorf("unexpected GuestPath %q", got[0].GuestPath)
	}
}

// TestResolveUserMounts_NonOverlay_DirectMount verifies non-overlay rows have
// StagingGuestPath == GuestPath (direct mount, no staging step needed).
func TestResolveUserMounts_NonOverlay_DirectMount(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := service.ResolveUserMounts(home)
	if len(got) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(got))
	}
	if got[0].Overlay {
		t.Error("expected Overlay=false for .local/bin row")
	}
	if got[0].StagingGuestPath != got[0].GuestPath {
		t.Errorf("non-overlay row: StagingGuestPath %q != GuestPath %q",
			got[0].StagingGuestPath, got[0].GuestPath)
	}
}

// TestResolveUserMounts_OverlayRow_StagingPath verifies overlay rows land at
// /run/nexus3/usermount/<basename> — not directly at GuestPath.
// Mutation guard: if Overlay is removed or staging path changes, this fails.
func TestResolveUserMounts_OverlayRow_StagingPath(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude/plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := service.ResolveUserMounts(home)
	if len(got) != 1 {
		t.Fatalf("expected 1 mount (.claude/plugins), got %d", len(got))
	}
	if !got[0].Overlay {
		t.Error("expected Overlay=true for .claude/plugins row")
	}
	if got[0].StagingGuestPath == got[0].GuestPath {
		t.Errorf("overlay row: StagingGuestPath %q must differ from GuestPath %q",
			got[0].StagingGuestPath, got[0].GuestPath)
	}
	const wantStaging = "/run/nexus3/usermount/plugins"
	if got[0].StagingGuestPath != wantStaging {
		t.Errorf("StagingGuestPath = %q, want %q", got[0].StagingGuestPath, wantStaging)
	}
}

// TestResolveUserMounts_EmptyHome verifies an empty home yields no mounts.
func TestResolveUserMounts_EmptyHome(t *testing.T) {
	home := t.TempDir()
	got := service.ResolveUserMounts(home)
	if len(got) != 0 {
		t.Errorf("expected 0 mounts for empty home, got %d", len(got))
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
	if len(got.Mounts) != 2 {
		t.Fatalf("len(Mounts) = %d, want 2", len(got.Mounts))
	}
	if got.Mounts[1].StagingGuestPath != "/run/nexus3/usermount/plugins" {
		t.Errorf("Mounts[1].StagingGuestPath = %q, want /run/nexus3/usermount/plugins",
			got.Mounts[1].StagingGuestPath)
	}
}
