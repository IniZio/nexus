package service

import (
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
}
