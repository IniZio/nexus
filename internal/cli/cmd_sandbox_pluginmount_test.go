package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/service"
)

func TestSandboxCreatePluginMounts(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(tmp, "ext", "myplugin")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ext, filepath.Join(pluginsDir, "foo")); err != nil {
		t.Fatal(err)
	}
	resolvedExt, err := filepath.EvalSymlinks(ext)
	if err != nil {
		t.Fatal(err)
	}

	specs, warns := service.ResolveClaudeCodeBindMounts(home, "")
	for _, w := range warns {
		t.Logf("warning: %s", w)
	}

	wantSpec := resolvedExt + ":" + resolvedExt + ":ro"
	found := false
	for _, s := range specs {
		if s == wantSpec {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("specs %q do not contain %q", specs, wantSpec)
	}

	lm, err := parseMountLive(wantSpec)
	if err != nil {
		t.Fatalf("parseMountLive(%q): %v", wantSpec, err)
	}
	if lm.HostPath != resolvedExt || lm.GuestPath != resolvedExt || !lm.ReadOnly {
		t.Fatalf("LiveMount = %+v, want HostPath=%s GuestPath=%s ReadOnly=true", lm, resolvedExt, resolvedExt)
	}
}

func TestSandboxCreatePluginMountsGroundwork(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mainRepo := filepath.Join(tmp, "repo")
	groundwork := filepath.Join(mainRepo, ".groundwork")
	if err := os.MkdirAll(groundwork, 0o755); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(mainRepo, ".git", "worktrees", "wt1")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(tmp, "worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	specs, _ := service.ResolveClaudeCodeBindMounts(home, worktree)

	wantGW := groundwork + ":" + groundwork
	found := false
	for _, s := range specs {
		if s == wantGW {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("specs %q do not contain groundwork mount %q", specs, wantGW)
	}
}
