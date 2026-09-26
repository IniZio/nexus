package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

func TestResolvePluginSymlinkMounts(t *testing.T) {
	home := t.TempDir()
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(rel string) string {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ln := func(target, rel string) {
		if err := os.Symlink(target, filepath.Join(home, rel)); err != nil {
			t.Fatal(err)
		}
	}

	plugins := mk(".claude/plugins")
	skills := mk(".claude/skills")
	pluginA := mk("ext/pluginA")
	nested := mk("ext/nested")
	nestedDir := mk("ext/nested/dir")
	mk(".claude/plugins/marketplaces")

	ln(pluginA, ".claude/plugins/a")
	ln(skills, ".claude/plugins/inside")
	ln(nested, ".claude/plugins/marketplaces/m1")
	ln(nestedDir, ".claude/plugins/marketplaces/m2")
	ln(filepath.Join(home, "does-not-exist"), ".claude/plugins/dangling")
	if err := os.WriteFile(filepath.Join(plugins, "installed_plugins.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	specs, warnings := ResolvePluginSymlinkMounts(home)

	wantNested := filepath.Join(realHome, "ext", "nested")
	wantA := filepath.Join(realHome, "ext", "pluginA")
	want := []string{
		wantNested + ":" + wantNested + ":ro",
		wantA + ":" + wantA + ":ro",
	}
	if strings.Join(specs, "\n") != strings.Join(want, "\n") {
		t.Errorf("specs = %q, want %q", specs, want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "dangling") {
		t.Errorf("warnings = %q, want exactly one mentioning dangling", warnings)
	}

	t.Run("missing plugins dir", func(t *testing.T) {
		specs, warnings := ResolvePluginSymlinkMounts(t.TempDir())
		if specs != nil || warnings != nil {
			t.Errorf("got %q, %q; want nil, nil", specs, warnings)
		}
	})

	t.Run("symlinked level-1 dir is not descended", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".claude", "plugins"), 0o755); err != nil {
			t.Fatal(err)
		}
		linkedDir := filepath.Join(home, "ext", "linkeddir")
		deep := filepath.Join(home, "ext", "deep")
		for _, d := range []string{linkedDir, deep} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(deep, filepath.Join(linkedDir, "inner")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(linkedDir, filepath.Join(home, ".claude", "plugins", "viaLink")); err != nil {
			t.Fatal(err)
		}
		specs, warnings := ResolvePluginSymlinkMounts(home)
		realLinked, _ := filepath.EvalSymlinks(linkedDir)
		if len(specs) != 1 || specs[0] != realLinked+":"+realLinked+":ro" {
			t.Errorf("specs = %q, want only %q", specs, realLinked)
		}
		if len(warnings) != 0 {
			t.Errorf("warnings = %q, want none", warnings)
		}
	})

	t.Run("marketplace outside claude dir yields spec", func(t *testing.T) {
		home := t.TempDir()
		realHome, _ := filepath.EvalSymlinks(home)
		pluginsDir := filepath.Join(home, ".claude", "plugins")
		if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		extDir := filepath.Join(home, "ext", "mymarket")
		if err := os.MkdirAll(extDir, 0o755); err != nil {
			t.Fatal(err)
		}
		raw := map[string]any{
			"mymarket": map[string]any{"installLocation": extDir},
		}
		b, _ := json.Marshal(raw)
		if err := os.WriteFile(filepath.Join(pluginsDir, "known_marketplaces.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		specs, warnings := ResolvePluginSymlinkMounts(home)
		want := filepath.Join(realHome, "ext", "mymarket")
		if len(specs) != 1 || specs[0] != want+":"+want+":ro" {
			t.Errorf("specs = %q, want %q", specs, want)
		}
		if len(warnings) != 0 {
			t.Errorf("unexpected warnings: %q", warnings)
		}
	})

	t.Run("marketplace inside claude dir yields no spec", func(t *testing.T) {
		home := t.TempDir()
		pluginsDir := filepath.Join(home, ".claude", "plugins")
		insideDir := filepath.Join(home, ".claude", "inside-market")
		for _, d := range []string{pluginsDir, insideDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		raw := map[string]any{
			"inside": map[string]any{"installLocation": insideDir},
		}
		b, _ := json.Marshal(raw)
		if err := os.WriteFile(filepath.Join(pluginsDir, "known_marketplaces.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		specs, warnings := ResolvePluginSymlinkMounts(home)
		if len(specs) != 0 {
			t.Errorf("expected no specs for inside-claude path, got %q", specs)
		}
		if len(warnings) != 0 {
			t.Errorf("unexpected warnings: %q", warnings)
		}
	})

	t.Run("nonexistent marketplace path yields warning not spec", func(t *testing.T) {
		home := t.TempDir()
		pluginsDir := filepath.Join(home, ".claude", "plugins")
		if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		raw := map[string]any{
			"ghost": map[string]any{"installLocation": "/nonexistent/path/for/test"},
		}
		b, _ := json.Marshal(raw)
		if err := os.WriteFile(filepath.Join(pluginsDir, "known_marketplaces.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		specs, warnings := ResolvePluginSymlinkMounts(home)
		if len(specs) != 0 {
			t.Errorf("expected no specs for nonexistent path, got %q", specs)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "does not exist") {
			t.Errorf("warnings = %q, want one mentioning 'does not exist'", warnings)
		}
	})

	t.Run("marketplace deduped with symlink scan", func(t *testing.T) {
		home := t.TempDir()
		realHome, _ := filepath.EvalSymlinks(home)
		pluginsDir := filepath.Join(home, ".claude", "plugins")
		if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		extDir := filepath.Join(home, "ext", "shared")
		if err := os.MkdirAll(extDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(extDir, filepath.Join(pluginsDir, "shared-link")); err != nil {
			t.Fatal(err)
		}
		raw := map[string]any{
			"shared": map[string]any{"installLocation": extDir},
		}
		b, _ := json.Marshal(raw)
		if err := os.WriteFile(filepath.Join(pluginsDir, "known_marketplaces.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		specs, warnings := ResolvePluginSymlinkMounts(home)
		want := filepath.Join(realHome, "ext", "shared")
		if len(specs) != 1 || specs[0] != want+":"+want+":ro" {
			t.Errorf("specs = %q, want exactly one %q", specs, want)
		}
		if len(warnings) != 0 {
			t.Errorf("unexpected warnings: %q", warnings)
		}
	})
}

func TestStagingExcludeLiveMounts(t *testing.T) {
	profile := cred.MustProfileByName(cred.ClaudeCodeProfileName)

	t.Run("returns entry when cache dir exists", func(t *testing.T) {
		agentDir := t.TempDir()
		cacheDir := filepath.Join(agentDir, "plugins", "cache")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Fatal(err)
		}
		entries := StagingExcludeLiveMounts(profile, agentDir)
		if len(entries) != 1 {
			t.Fatalf("want 1 entry, got %d", len(entries))
		}
		e := entries[0]
		if e.HostPath != cacheDir {
			t.Errorf("HostPath = %q, want %q", e.HostPath, cacheDir)
		}
		if e.GuestPath != cacheDir {
			t.Errorf("GuestPath = %q, want %q", e.GuestPath, cacheDir)
		}
		if !strings.HasPrefix(e.StagingGuestPath, "/run/nexus/usermount/stagex-") {
			t.Errorf("StagingGuestPath = %q, want prefix /run/nexus/usermount/stagex-", e.StagingGuestPath)
		}
		if strings.HasPrefix(e.StagingGuestPath, agentDir) {
			t.Errorf("StagingGuestPath %q must not be inside agentDir (would be shadowed by overlay)", e.StagingGuestPath)
		}
		if !e.Rebind {
			t.Error("Rebind must be true so the seed script bind-mounts after the agentcfg overlay")
		}
	})

	t.Run("skips when cache dir absent", func(t *testing.T) {
		agentDir := t.TempDir()
		entries := StagingExcludeLiveMounts(profile, agentDir)
		if len(entries) != 0 {
			t.Errorf("expected no entries, got %v", entries)
		}
	})

}

func TestStagingExcludeLiveMounts_OrderingAfterOverlay(t *testing.T) {
	agentDir := t.TempDir()
	cacheDir := filepath.Join(agentDir, "plugins", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := cred.MustProfileByName(cred.ClaudeCodeProfileName)
	entries := StagingExcludeLiveMounts(profile, agentDir)
	if len(entries) == 0 {
		t.Fatal("no entries returned")
	}
	manifest := UserMountManifest{Mounts: entries}
	script := buildUserMountScript(manifest)

	stagingPath := entries[0].StagingGuestPath
	guestPath := entries[0].GuestPath

	if !strings.Contains(script, "mount --bind '"+stagingPath+"' '"+guestPath+"'") {
		t.Errorf("seed script missing bind mount of staging path %q to guest path %q\nscript:\n%s", stagingPath, guestPath, script)
	}
	if !strings.Contains(script, "mount -o remount,ro,bind '"+guestPath+"'") {
		t.Errorf("seed script missing ro remount of %q\nscript:\n%s", guestPath, script)
	}
	step1Pos := strings.Index(script, "mount --bind")
	step2Pos := strings.Index(script, "PATH drop-in")
	if step1Pos < 0 || step2Pos < 0 || step1Pos > step2Pos {
		t.Errorf("rebind step must appear before PATH drop-in: bind at %d, path at %d\nscript:\n%s", step1Pos, step2Pos, script)
	}
}
