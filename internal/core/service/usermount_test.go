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

// minimalRecipe returns a claude-code-shaped ToolRecipe for testing.
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

// minimalCursorRecipe returns a cursor-agent-shaped ToolRecipe for testing.
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

// TestCheckRecipeShadows_BinPathExact verifies mount at BinPath triggers warning.
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

// TestCheckRecipeShadows_PathEntryDir verifies mount at PATH parent triggers warning.
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

// TestCheckRecipeShadows_InstallDir verifies mount at InstallDir prefix triggers warning.
func TestCheckRecipeShadows_InstallDir(t *testing.T) {
	spec := "~/.local/share/cursor-agent:/usr/local/share/cursor-agent:ro"
	warnings := service.CheckRecipeShadows([]string{spec}, minimalCursorRecipe())
	if len(warnings) == 0 {
		t.Fatal("expected a shadow warning for a mount covering the recipe install dir prefix, got none")
	}
	if !strings.Contains(warnings[0], spec) {
		t.Errorf("warning %q does not contain raw spec %q", warnings[0], spec)
	}
}

// TestCheckRecipeShadows_Symlink verifies mount at symlink LinkPath triggers warning.
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

// TestCheckRecipeShadows_NonBinaryRowsUntouched verifies non-binary mounts are not flagged.
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

// TestCheckRecipeShadows_EmptyRecipe verifies empty recipe produces no warnings.
func TestCheckRecipeShadows_EmptyRecipe(t *testing.T) {
	warnings := service.CheckRecipeShadows([]string{"~/.local/bin:/usr/local/bin:ro"}, cred.ToolRecipe{})
	if len(warnings) != 0 {
		t.Errorf("expected nil for empty recipe, got %v", warnings)
	}
}

// TestBuildUserMountManifest_CuratedPATHDir verifies curated PATH handling.
func TestBuildUserMountManifest_CuratedPATHDir(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
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
	if m.GuestPath != "/root/.local/bin" {
		t.Errorf("GuestPath = %q, want /root/.local/bin", m.GuestPath)
	}
	if m.HostPath != localBin {
		t.Errorf("HostPath = %q, want %q", m.HostPath, localBin)
	}

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

// TestBuildUserMountManifest_ExistingDir verifies existing dirs are included.
func TestBuildUserMountManifest_ExistingDir(t *testing.T) {
	home := t.TempDir()
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

// TestBuildUserMountManifest_AbsentDirSkipped verifies absent dirs are skipped.
func TestBuildUserMountManifest_AbsentDirSkipped(t *testing.T) {
	home := t.TempDir()
	got := service.BuildUserMountManifest(home, []string{home + "/nonexistent:/root/.local/bin"})
	if len(got.Mounts) != 0 {
		t.Errorf("expected 0 mounts for absent dir, got %d", len(got.Mounts))
	}
}

// TestBuildUserMountManifest_OverlayForClaude verifies .claude paths get Overlay=false.
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
	if m.Overlay {
		t.Errorf("expected Overlay=false for /root/.claude/ path (live rw mount replaces overlay)")
	}
	if m.StagingGuestPath != m.GuestPath {
		t.Errorf("non-overlay non-curated row: StagingGuestPath %q != GuestPath %q", m.StagingGuestPath, m.GuestPath)
	}
}

// TestBuildUserMountManifest_TildeExpansion verifies tilde expansion.
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

// TestBuildUserMountManifest_ContainmentMatch verifies containment matching for curated dirs.
func TestBuildUserMountManifest_ContainmentMatch(t *testing.T) {
	home := t.TempDir()
	miseDir := filepath.Join(home, ".local", "share", "mise")
	if err := os.MkdirAll(miseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got := service.BuildUserMountManifest(home, []string{miseDir + ":/root/.local/share/mise"})
	if len(got.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(got.Mounts))
	}
	m := got.Mounts[0]
	if !m.Curated {
		t.Error("expected Curated=true for parent of /root/.local/share/mise/shims; " +
			"without containment matching the exact-match check leaves Curated=false, " +
			"allowing a raw host directory on the guest PATH (D-TP-05 violated)")
	}
	if m.Overlay {
		t.Error("Curated and Overlay must be mutually exclusive")
	}
	const wantStaging = "/run/nexus3/usermount/bin-mise"
	if m.StagingGuestPath != wantStaging {
		t.Errorf("StagingGuestPath = %q, want %q", m.StagingGuestPath, wantStaging)
	}
	const wantSubPath = "shims"
	if m.CuratedSubPath != wantSubPath {
		t.Errorf("CuratedSubPath = %q, want %q", m.CuratedSubPath, wantSubPath)
	}
	if m.GuestPath != "/root/.local/share/mise" {
		t.Errorf("GuestPath = %q, want /root/.local/share/mise", m.GuestPath)
	}
}

// TestBuildUserMountManifest_EmptyMounts verifies empty input returns no mounts.
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

// TestWriteUserMountManifest_RoundTrip verifies manifest serialization and mode.
func TestWriteUserMountManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := service.UserMountManifest{
		HostHome: "/home/alice",
		Mounts: []service.ResolvedUserMount{
			{
				HostPath:         "/home/alice/.local/bin",
				GuestPath:        "/root/.local/bin",
				Overlay:          false,
				Curated:          true,
				StagingGuestPath: "/run/nexus3/usermount/bin-bin",
			},
			{
				HostPath:         "/home/alice/.claude/plugins",
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
