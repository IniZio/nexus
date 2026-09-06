package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
)

// minimalRecipe returns a ToolRecipe shaped like the real claude-code recipe,
// usable for shadow-check tests without depending on the live profile value.
func minimalRecipe() cred.ToolRecipe {
	return cred.ToolRecipe{
		BinPath: "/usr/local/bin/claude",
		Packages: []cred.RecipePackage{
			{
				Kind:       cred.RecipeKindTarball,
				Name:       "node",
				Version:    "22.0.0",
				InstallDir: "/usr/local",
			},
			{
				Kind:    cred.RecipeKindNPM,
				Name:    "@anthropic-ai/claude-code",
				Version: "2.0.0",
			},
		},
	}
}

// minimalCursorRecipe returns a ToolRecipe shaped like the real cursor-agent recipe.
func minimalCursorRecipe() cred.ToolRecipe {
	return cred.ToolRecipe{
		BinPath: "/usr/local/bin/cursor-agent",
		Packages: []cred.RecipePackage{
			{
				Kind:       cred.RecipeKindTarball,
				Name:       "cursor-agent",
				Version:    "2026.08.25-3e8eec8",
				InstallDir: "/usr/local/share/cursor-agent/versions/{VERSION}",
				Symlinks: []cred.RecipeSymlink{
					{
						LinkPath:   "/usr/local/bin/cursor-agent",
						TargetPath: "/usr/local/share/cursor-agent/versions/{VERSION}/agent-cli",
					},
				},
			},
		},
	}
}

// TestCheckRecipeShadows_BinPathExact verifies that a mount exactly at the
// recipe's BinPath triggers a warning quoting the raw spec text. This is the
// real call site for the shadow diagnostic (AC-5) — the test drives
// service.CheckRecipeShadows directly rather than a hand-built stand-in.
func TestCheckRecipeShadows_BinPathExact(t *testing.T) {
	spec := "~/.local/bin/claude:/usr/local/bin/claude:ro"
	warnings := service.CheckRecipeShadows([]string{spec}, minimalRecipe())
	if len(warnings) == 0 {
		t.Fatal("expected a shadow warning for a mount exactly at BinPath, got none")
	}
	if !strings.Contains(warnings[0], spec) {
		t.Errorf("warning %q does not contain raw spec %q", warnings[0], spec)
	}
}

// TestCheckRecipeShadows_PathEntryDir verifies that a mount at the parent
// directory of BinPath (the PATH-entry directory) also triggers a warning.
func TestCheckRecipeShadows_PathEntryDir(t *testing.T) {
	spec := "~/.local/bin:/usr/local/bin:ro"
	warnings := service.CheckRecipeShadows([]string{spec}, minimalRecipe())
	if len(warnings) == 0 {
		t.Fatal("expected a shadow warning for a mount at the BinPath parent dir, got none")
	}
	if !strings.Contains(warnings[0], spec) {
		t.Errorf("warning %q does not contain raw spec %q", warnings[0], spec)
	}
}

// TestCheckRecipeShadows_InstallDir verifies that a mount covering a recipe
// package's InstallDir prefix triggers a warning.
func TestCheckRecipeShadows_InstallDir(t *testing.T) {
	// cursor-agent InstallDir is /usr/local/share/cursor-agent/versions/{VERSION};
	// a mount at the stable prefix /usr/local/share/cursor-agent shadows it.
	spec := "~/.local/share/cursor-agent:/usr/local/share/cursor-agent:ro"
	warnings := service.CheckRecipeShadows([]string{spec}, minimalCursorRecipe())
	if len(warnings) == 0 {
		t.Fatal("expected a shadow warning for a mount covering the recipe install dir prefix, got none")
	}
	if !strings.Contains(warnings[0], spec) {
		t.Errorf("warning %q does not contain raw spec %q", warnings[0], spec)
	}
}

// TestCheckRecipeShadows_Symlink verifies that a mount exactly at a recipe
// symlink's LinkPath triggers a warning.
func TestCheckRecipeShadows_Symlink(t *testing.T) {
	spec := "~/.local/bin/cursor-agent:/usr/local/bin/cursor-agent:ro"
	warnings := service.CheckRecipeShadows([]string{spec}, minimalCursorRecipe())
	if len(warnings) == 0 {
		t.Fatal("expected a shadow warning for a mount at a recipe symlink path, got none")
	}
	if !strings.Contains(warnings[0], spec) {
		t.Errorf("warning %q does not contain raw spec %q", warnings[0], spec)
	}
}

// TestCheckRecipeShadows_NonBinaryRowsUntouched verifies that operator mounts
// carrying non-binary payloads (plugins, mise, groundwork, codegraph, vscode)
// produce no shadow warnings. These rows must still reach the manifest
// unchanged (AC-5 item 5: only binary rows are flagged).
func TestCheckRecipeShadows_NonBinaryRowsUntouched(t *testing.T) {
	nonBinaryMounts := []string{
		"~/.claude/plugins:/root/.claude/plugins:ro",
		"~/.local/share/mise:/root/.local/share/mise:ro",
		"~/.config/mise:/root/.config/mise:ro",
		"~/.local/share/groundwork:/root/.local/share/groundwork:ro",
		"~/.codegraph:/root/.codegraph:ro",
		"~/.vscode-server/extensions:/root/.vscode-server/extensions:ro",
	}
	warnings := service.CheckRecipeShadows(nonBinaryMounts, minimalRecipe())
	if len(warnings) != 0 {
		t.Errorf("expected no shadow warnings for non-binary operator mounts, got %d: %v",
			len(warnings), warnings)
	}
}

// TestCheckRecipeShadows_EmptyRecipe verifies that a zero/empty recipe
// (no Packages) returns nil — no false positives when no recipe is registered.
func TestCheckRecipeShadows_EmptyRecipe(t *testing.T) {
	warnings := service.CheckRecipeShadows([]string{"~/.local/bin:/usr/local/bin:ro"}, cred.ToolRecipe{})
	if len(warnings) != 0 {
		t.Errorf("expected nil for empty recipe, got %v", warnings)
	}
}

// TestBuildUserMountManifest_CuratedPATHDir verifies S6-AC1:
// a mount whose guest path is a GuestCuratedPATHDirs entry gets Curated=true,
// a StagingGuestPath under /run/nexus3/usermount/bin-<base>, and Overlay=false.
// A mount with a non-curated guest path is unchanged in every field.
func TestBuildUserMountManifest_CuratedPATHDir(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// /root/.local/bin is a GuestCuratedPATHDirs entry.
	got := service.BuildUserMountManifest(home, []string{localBin + ":/root/.local/bin"})
	if len(got.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(got.Mounts))
	}
	m := got.Mounts[0]
	if !m.Curated {
		t.Error("expected Curated=true for /root/.local/bin")
	}
	if m.Overlay {
		t.Error("Curated and Overlay must be mutually exclusive; Overlay=true")
	}
	const wantStaging = "/run/nexus3/usermount/bin-bin"
	if m.StagingGuestPath != wantStaging {
		t.Errorf("StagingGuestPath = %q, want %q", m.StagingGuestPath, wantStaging)
	}
	// GuestPath and HostPath must be unchanged.
	if m.GuestPath != "/root/.local/bin" {
		t.Errorf("GuestPath = %q, want /root/.local/bin", m.GuestPath)
	}
	if m.HostPath != localBin {
		t.Errorf("HostPath = %q, want %q", m.HostPath, localBin)
	}

	// Non-curated row: /root/.config is not a PATH-entry dir.
	configDir := filepath.Join(home, ".config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got2 := service.BuildUserMountManifest(home, []string{configDir + ":/root/.config"})
	if len(got2.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(got2.Mounts))
	}
	n := got2.Mounts[0]
	if n.Curated {
		t.Error("non-PATH dir should have Curated=false")
	}
	if n.StagingGuestPath != n.GuestPath {
		t.Errorf("non-curated non-overlay row: StagingGuestPath %q != GuestPath %q", n.StagingGuestPath, n.GuestPath)
	}
}

// TestBuildUserMountManifest_ExistingDir verifies that a mount whose host dir
// exists is included. Uses /root/.config (non-curated, non-overlay) to exercise
// the direct-mount path (Overlay=false, StagingGuestPath==GuestPath).
func TestBuildUserMountManifest_ExistingDir(t *testing.T) {
	home := t.TempDir()
	// Use /root/.config: not a curated PATH dir, not under /root/.claude.
	dir := filepath.Join(home, ".config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got := service.BuildUserMountManifest(home, []string{dir + ":/root/.config"})
	if len(got.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(got.Mounts))
	}
	m := got.Mounts[0]
	if m.HostPath != dir {
		t.Errorf("HostPath = %q, want %q", m.HostPath, dir)
	}
	if m.GuestPath != "/root/.config" {
		t.Errorf("GuestPath = %q, want /root/.config", m.GuestPath)
	}
	if m.Overlay {
		t.Errorf("expected Overlay=false for non-.claude path")
	}
	if m.Curated {
		t.Errorf("expected Curated=false for non-PATH-entry path")
	}
	if m.StagingGuestPath != m.GuestPath {
		t.Errorf("non-overlay non-curated row: StagingGuestPath %q != GuestPath %q", m.StagingGuestPath, m.GuestPath)
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
// Uses /root/.local/bin (a curated dir) to also confirm Curated=true is set after expansion.
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
	if !got.Mounts[0].Curated {
		t.Error("expected Curated=true for /root/.local/bin after tilde expansion")
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
				Curated:          true,
				StagingGuestPath: "/run/nexus3/usermount/bin-bin",
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
