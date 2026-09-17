package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHerdrWorktreePluginMounts(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(tmp, "ext", "plugin")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ext, filepath.Join(pluginsDir, "nexus")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "missing"), filepath.Join(pluginsDir, "dangling")); err != nil {
		t.Fatal(err)
	}
	resolvedExt, err := filepath.EvalSymlinks(ext)
	if err != nil {
		t.Fatal(err)
	}

	var warnings []string
	got := herdrWorktreePluginMounts(home, func(msg string) { warnings = append(warnings, msg) })

	wantSpec := resolvedExt + ":" + resolvedExt + ":ro"
	if len(got) != 1 || got[0] != wantSpec {
		t.Fatalf("mounts = %q, want [%q]", got, wantSpec)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one (dangling link)", warnings)
	}

	args := herdrWorktreeSandboxCreateArgs("h", "/w:/workspace", "--image", "x", got, nil, "", nil, false)
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--mount" && args[i+1] == wantSpec {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("create args %q lack consecutive pair --mount %q", args, wantSpec)
	}
}
