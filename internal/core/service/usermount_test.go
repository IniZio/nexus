package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// TestUserMountTable_Rows verifies the built-in default rows resolve in order
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
	got, _ := service.ResolveUserMounts(home)
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
	got, _ := service.ResolveUserMounts(home)
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
	got, _ := service.ResolveUserMounts(home)
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
	got, _ := service.ResolveUserMounts(home)
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
	got, symlinks := service.ResolveUserMounts(home)
	if len(got) != 0 {
		t.Errorf("expected 0 mounts for empty home, got %d", len(got))
	}
	if len(symlinks) != 0 {
		t.Errorf("expected 0 symlinks for empty home, got %d", len(symlinks))
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
		Symlinks: []service.ResolvedUserSymlink{
			{Link: "/root/.config/opencode/plugins/groundwork", Target: "/root/.local/share/groundwork"},
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
	if len(got.Symlinks) != 1 {
		t.Fatalf("len(Symlinks) = %d, want 1", len(got.Symlinks))
	}
	if got.Symlinks[0].Link != "/root/.config/opencode/plugins/groundwork" {
		t.Errorf("Symlinks[0].Link = %q", got.Symlinks[0].Link)
	}
	if got.Symlinks[0].Target != "/root/.local/share/groundwork" {
		t.Errorf("Symlinks[0].Target = %q", got.Symlinks[0].Target)
	}
}

// --- Config loader tests ---

// writeConfig creates ~/.config/nexus3/config.yaml under home with content.
func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "nexus3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadUserMountConfig_NoFile verifies absent config → zero struct, nil error.
func TestLoadUserMountConfig_NoFile(t *testing.T) {
	home := t.TempDir()
	cfg, err := service.LoadUserMountConfig(home)
	if err != nil {
		t.Fatalf("expected nil error for absent config, got: %v", err)
	}
	if cfg.DisableDefaults || len(cfg.Mounts) != 0 || len(cfg.Symlinks) != 0 {
		t.Errorf("expected zero config, got %+v", cfg)
	}
}

// TestLoadUserMountConfig_ParsesMountsAndSymlinks verifies mounts and symlinks
// are parsed, and ~ in host is expanded against hostHome.
func TestLoadUserMountConfig_ParsesMountsAndSymlinks(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `
agent_mounts:
  mounts:
    - host: ~/.mytools
      guest: /root/.mytools
      overlay: false
    - host: $HOME/.local/share/custom
      guest: /root/.local/share/custom
  symlinks:
    - link: /root/.config/opencode/plugins/groundwork
      target: /root/.local/share/groundwork
`)
	cfg, err := service.LoadUserMountConfig(home)
	if err != nil {
		t.Fatalf("LoadUserMountConfig: %v", err)
	}
	if len(cfg.Mounts) != 2 {
		t.Fatalf("len(Mounts) = %d, want 2", len(cfg.Mounts))
	}
	// ~ expanded
	wantHost0 := filepath.Join(home, ".mytools")
	if cfg.Mounts[0].Host != wantHost0 {
		t.Errorf("Mounts[0].Host = %q, want %q", cfg.Mounts[0].Host, wantHost0)
	}
	// $HOME expanded
	wantHost1 := filepath.Join(home, ".local/share/custom")
	if cfg.Mounts[1].Host != wantHost1 {
		t.Errorf("Mounts[1].Host = %q, want %q", cfg.Mounts[1].Host, wantHost1)
	}
	if len(cfg.Symlinks) != 1 {
		t.Fatalf("len(Symlinks) = %d, want 1", len(cfg.Symlinks))
	}
	if cfg.Symlinks[0].Link != "/root/.config/opencode/plugins/groundwork" {
		t.Errorf("Symlinks[0].Link = %q", cfg.Symlinks[0].Link)
	}
	if cfg.Symlinks[0].Target != "/root/.local/share/groundwork" {
		t.Errorf("Symlinks[0].Target = %q", cfg.Symlinks[0].Target)
	}
}

// TestLoadUserMountConfig_MalformedYAML verifies malformed YAML returns an error.
func TestLoadUserMountConfig_MalformedYAML(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "agent_mounts: [bad: yaml: }{")
	_, err := service.LoadUserMountConfig(home)
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

// TestResolveUserMounts_UserExtendsDefaults verifies user config mounts are
// appended after defaults when disable_defaults is false.
func TestResolveUserMounts_UserExtendsDefaults(t *testing.T) {
	home := t.TempDir()
	// Create one default dir and the user mount dir.
	if err := os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	userDir := filepath.Join(home, "mytools")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, `
agent_mounts:
  mounts:
    - host: ~/mytools
      guest: /root/mytools
`)
	mounts, _ := service.ResolveUserMounts(home)
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts (1 default + 1 user), got %d", len(mounts))
	}
	// Default comes first.
	if mounts[0].GuestPath != "/root/.local/bin" {
		t.Errorf("mounts[0].GuestPath = %q, want /root/.local/bin", mounts[0].GuestPath)
	}
	// User mount is appended.
	if mounts[1].GuestPath != "/root/mytools" {
		t.Errorf("mounts[1].GuestPath = %q, want /root/mytools", mounts[1].GuestPath)
	}
	if mounts[1].HostPath != userDir {
		t.Errorf("mounts[1].HostPath = %q, want %q", mounts[1].HostPath, userDir)
	}
}

// TestResolveUserMounts_DisableDefaults verifies disable_defaults=true removes
// all built-in rows; only user entries appear.
func TestResolveUserMounts_DisableDefaults(t *testing.T) {
	home := t.TempDir()
	// Create a default dir — it must NOT appear in output.
	if err := os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	userDir := filepath.Join(home, "mytools")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, `
agent_mounts:
  disable_defaults: true
  mounts:
    - host: ~/mytools
      guest: /root/mytools
`)
	mounts, _ := service.ResolveUserMounts(home)
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount (user only), got %d: %v", len(mounts), mounts)
	}
	if mounts[0].GuestPath != "/root/mytools" {
		t.Errorf("mounts[0].GuestPath = %q, want /root/mytools", mounts[0].GuestPath)
	}
}

// TestResolveUserMounts_DedupByGuestPath verifies a user entry with the same
// guest path as a default replaces the default (user override).
func TestResolveUserMounts_DedupByGuestPath(t *testing.T) {
	home := t.TempDir()
	// Create both the default dir and the user-specified dir.
	if err := os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	altBin := filepath.Join(home, "altbin")
	if err := os.MkdirAll(altBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// User overrides /root/.local/bin with a different host path.
	writeConfig(t, home, `
agent_mounts:
  mounts:
    - host: ~/altbin
      guest: /root/.local/bin
`)
	mounts, _ := service.ResolveUserMounts(home)
	// Only one /root/.local/bin row — the user's.
	var binMounts []service.ResolvedUserMount
	for _, m := range mounts {
		if m.GuestPath == "/root/.local/bin" {
			binMounts = append(binMounts, m)
		}
	}
	if len(binMounts) != 1 {
		t.Fatalf("expected exactly 1 /root/.local/bin mount, got %d", len(binMounts))
	}
	if binMounts[0].HostPath != altBin {
		t.Errorf("dedup: HostPath = %q, want %q (user entry should override default)", binMounts[0].HostPath, altBin)
	}
}

// TestResolveUserMounts_SymlinksInManifest verifies symlinks from config flow
// through ResolveUserMounts and survive a manifest round-trip.
func TestResolveUserMounts_SymlinksInManifest(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `
agent_mounts:
  symlinks:
    - link: /root/.config/opencode/plugins/groundwork
      target: /root/.local/share/groundwork
    - link: /root/.config/opencode/plugins/codegraph
      target: /root/.codegraph
`)
	_, symlinks := service.ResolveUserMounts(home)
	if len(symlinks) != 2 {
		t.Fatalf("expected 2 symlinks, got %d", len(symlinks))
	}
	if symlinks[0].Link != "/root/.config/opencode/plugins/groundwork" {
		t.Errorf("symlinks[0].Link = %q", symlinks[0].Link)
	}
	if symlinks[1].Target != "/root/.codegraph" {
		t.Errorf("symlinks[1].Target = %q", symlinks[1].Target)
	}

	// Round-trip through manifest.
	dir := t.TempDir()
	manifest := service.UserMountManifest{
		HostHome: home,
		Symlinks: symlinks,
	}
	if err := service.WriteUserMountManifest(dir, manifest); err != nil {
		t.Fatalf("WriteUserMountManifest: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "usermounts.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got service.UserMountManifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Symlinks) != 2 {
		t.Fatalf("round-trip: len(Symlinks) = %d, want 2", len(got.Symlinks))
	}
	if got.Symlinks[0].Link != symlinks[0].Link || got.Symlinks[0].Target != symlinks[0].Target {
		t.Errorf("round-trip symlinks[0] mismatch: got %+v", got.Symlinks[0])
	}
}
