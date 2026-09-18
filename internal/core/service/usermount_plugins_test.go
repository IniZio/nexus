package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestResolveHookRuntimeMounts(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	miseInstalls := filepath.Join(home, ".local", "share", "mise", "installs")

	fakeStat := func(p string) (os.FileInfo, error) {
		switch p {
		case localBin, miseInstalls:
			return nil, nil
		default:
			return nil, os.ErrNotExist
		}
	}

	t.Run("both present linux", func(t *testing.T) {
		mounts := ResolveHookRuntimeMounts(home, "linux", fakeStat)
		if len(mounts) != 2 {
			t.Fatalf("want 2 mounts, got %d", len(mounts))
		}
		for _, m := range mounts {
			if !m.ReadOnly {
				t.Errorf("mount %s: want ReadOnly=true", m.HostPath)
			}
			if m.HostPath != m.GuestPath {
				t.Errorf("host-path identity violated: host=%s guest=%s", m.HostPath, m.GuestPath)
			}
		}
		if mounts[0].HostPath != localBin {
			t.Errorf("first mount: got %s, want %s", mounts[0].HostPath, localBin)
		}
		if mounts[1].HostPath != miseInstalls {
			t.Errorf("second mount: got %s, want %s", mounts[1].HostPath, miseInstalls)
		}
	})

	t.Run("non-linux returns nil", func(t *testing.T) {
		if got := ResolveHookRuntimeMounts(home, "darwin", fakeStat); got != nil {
			t.Errorf("want nil on darwin, got %v", got)
		}
	})

	t.Run("absent dir skipped", func(t *testing.T) {
		onlyBin := func(p string) (os.FileInfo, error) {
			if p == localBin {
				return nil, nil
			}
			return nil, os.ErrNotExist
		}
		mounts := ResolveHookRuntimeMounts(home, "linux", onlyBin)
		if len(mounts) != 1 || mounts[0].HostPath != localBin {
			t.Errorf("want exactly localBin, got %v", mounts)
		}
	})
}

func TestResolveHookRuntimePathDirs(t *testing.T) {
	home := "/home/testuser"
	localBin := home + "/.local/bin"
	miseInstalls := home + "/.local/share/mise/installs"
	projectedDirs := []string{localBin, miseInstalls}

	fakeLookPath := func(name string) (string, error) {
		switch name {
		case "rtk":
			return localBin + "/rtk", nil
		case "bun":
			return miseInstalls + "/bun/latest/bin/bun", nil
		default:
			return "", fmt.Errorf("not found")
		}
	}

	t.Run("both resolve under projected dirs", func(t *testing.T) {
		dirs := ResolveHookRuntimePathDirs("linux", fakeLookPath, projectedDirs)
		if len(dirs) != 2 {
			t.Fatalf("want 2 dirs, got %v", dirs)
		}
		if dirs[0] != localBin {
			t.Errorf("first dir: got %s, want %s", dirs[0], localBin)
		}
		wantBunDir := miseInstalls + "/bun/latest/bin"
		if dirs[1] != wantBunDir {
			t.Errorf("second dir: got %s, want %s", dirs[1], wantBunDir)
		}
	})

	t.Run("non-linux returns nil", func(t *testing.T) {
		if got := ResolveHookRuntimePathDirs("darwin", fakeLookPath, projectedDirs); got != nil {
			t.Errorf("want nil on darwin, got %v", got)
		}
	})

	t.Run("binary outside projected dirs skipped", func(t *testing.T) {
		outsideLookPath := func(name string) (string, error) {
			if name == "rtk" {
				return "/usr/local/bin/rtk", nil
			}
			return "", fmt.Errorf("not found")
		}
		dirs := ResolveHookRuntimePathDirs("linux", outsideLookPath, projectedDirs)
		if len(dirs) != 0 {
			t.Errorf("want nil, got %v", dirs)
		}
	})

	t.Run("missing binary skipped silently", func(t *testing.T) {
		noneLookPath := func(name string) (string, error) {
			return "", fmt.Errorf("not found")
		}
		dirs := ResolveHookRuntimePathDirs("linux", noneLookPath, projectedDirs)
		if len(dirs) != 0 {
			t.Errorf("want nil, got %v", dirs)
		}
	})
}
