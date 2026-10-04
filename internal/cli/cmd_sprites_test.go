package cli

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
)

func TestSpritesParseRepoFlag(t *testing.T) {
	for _, url := range []string{"https://github.com/a/b.git", "git@github.com:a/b.git"} {
		f, err := parseSandboxCreateArgs([]string{"p/n", "--repo", url})
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		if f.repoURL != url || f.allowedRepo != "" {
			t.Errorf("%s: repoURL=%q allowedRepo=%q", url, f.repoURL, f.allowedRepo)
		}
	}
	f, err := parseSandboxCreateArgs([]string{"p/n", "--repo", "a/b"})
	if err != nil {
		t.Fatal(err)
	}
	if f.repoURL != "" || f.allowedRepo != "a/b" {
		t.Errorf("owner/name: repoURL=%q allowedRepo=%q", f.repoURL, f.allowedRepo)
	}
}

func TestSpritesRepo(t *testing.T) {
	cases := []struct {
		name string
		f    sandboxCreateFlags
		want string
		err  bool
	}{
		{"url", sandboxCreateFlags{repoURL: "git@github.com:a/b.git"}, "git@github.com:a/b.git", false},
		{"owner/name", sandboxCreateFlags{allowedRepo: "owner/name"}, "https://github.com/owner/name.git", false},
		{"owner/name.git", sandboxCreateFlags{allowedRepo: "owner/name.git"}, "https://github.com/owner/name.git", false},
		{"neither", sandboxCreateFlags{}, "", true},
	}
	for _, c := range cases {
		got, err := spritesRepo(c.f)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s: got %q err=%v, want %q err=%v", c.name, got, err, c.want, c.err)
		}
	}
}

func TestSpritesCreateRejectsVMOnlyFlags(t *testing.T) {
	base := func() sandboxCreateFlags {
		return sandboxCreateFlags{positionals: []string{"p/n"}, repoURL: "https://github.com/a/b.git"}
	}
	cases := map[string]func(*sandboxCreateFlags){
		"--image":       func(f *sandboxCreateFlags) { f.imageRef = "x" },
		"--rootfs":      func(f *sandboxCreateFlags) { f.rootfsPath = "x" },
		"--file":        func(f *sandboxCreateFlags) { f.filePath = "x" },
		"--dockerfile":  func(f *sandboxCreateFlags) { f.dockerfilePath = "x" },
		"--mount":       func(f *sandboxCreateFlags) { f.mountLive = []string{"/a:/b"} },
		"--mount-named": func(f *sandboxCreateFlags) { f.mountNamed = []string{"v:/b"} },
		"--memory":      func(f *sandboxCreateFlags) { f.memoryMiB = 512 },
		"--vcpus":       func(f *sandboxCreateFlags) { f.vcpus = 2 },
		"--nested-virt": func(f *sandboxCreateFlags) { f.nestedVirt = true },
		"--workspace":   func(f *sandboxCreateFlags) { f.workspacePath = "/x" },
	}
	for flag, mut := range cases {
		f := base()
		mut(&f)
		out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)
		// nil svc: flag checks run before any svc/driver use; a miss would panic.
		err := runSpritesCreate(context.Background(), f, out, nil)
		if !errors.Is(err, registry.ErrUnsupported) {
			t.Errorf("%s: err=%v, want ErrUnsupported", flag, err)
		}
	}
}

func TestSpritesCreateUsageErrorOnPositionals(t *testing.T) {
	for _, pos := range [][]string{nil, {"p/a", "p/b"}} {
		out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)
		err := runSpritesCreate(context.Background(), sandboxCreateFlags{positionals: pos}, out, nil)
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Errorf("positionals %v: err=%v, want *UsageError", pos, err)
		}
	}
}

func TestSpritesCreatePushRequiresRepo(t *testing.T) {
	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)
	err := runSpritesCreate(context.Background(), sandboxCreateFlags{positionals: []string{"p/n"}, syncMode: "push"}, out, nil)
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("err=%v, want *UsageError", err)
	}
}

func TestSpritesEgress(t *testing.T) {
	cfg := config.Config{}
	h, open := spritesEgress(cfg, sandboxCreateFlags{allowHosts: []string{"B.com", "a.com", "b.com", " "}})
	if !reflect.DeepEqual(h, []string{"a.com", "b.com"}) || open {
		t.Errorf("got %v open=%v, want [a.com b.com] closed", h, open)
	}
	if h, open := spritesEgress(cfg, sandboxCreateFlags{}); len(h) != 0 || open {
		t.Errorf("default: got %v open=%v, want deny-all", h, open)
	}
	if _, open := spritesEgress(cfg, sandboxCreateFlags{egressExplicit: true}); !open {
		t.Error("explicit --egress open must be open")
	}
	if _, open := spritesEgress(cfg, sandboxCreateFlags{egressExplicit: true, egressClosed: true}); open {
		t.Error("explicit --egress closed must stay closed")
	}
}

func TestSpritesBackendGatesVMOnlyFeatures(t *testing.T) {
	t.Setenv("NEXUS_BACKEND", "sprites")
	for _, feat := range []string{"volume", "disk"} {
		if err := requireBackend(feat); !errors.Is(err, registry.ErrUnsupported) {
			t.Errorf("requireBackend(%q)=%v, want ErrUnsupported", feat, err)
		}
	}
}

func TestSpritesGuestArgv(t *testing.T) {
	got := SpritesGuestArgv()
	if !reflect.DeepEqual(got, sprites.ShellArgv) || got[0] != "/bin/sh" {
		t.Errorf("got %v", got)
	}
}

func TestSpritesPresetFlag(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{"p/n", "--repo", "a/b", "--preset", "docker"})
	if err != nil || len(f.presets) != 1 || f.presets[0] != "docker" {
		t.Fatalf("presets=%v err=%v", f.presets, err)
	}
	if _, err := parseSandboxCreateArgs([]string{"p/n", "--preset"}); err == nil {
		t.Fatal("missing value must fail")
	}
}

func TestSpritesCreateUnknownPreset(t *testing.T) {
	f := sandboxCreateFlags{positionals: []string{"p/n"}, repoURL: "https://github.com/a/b.git", presets: []string{"podman"}}
	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)
	err := runSpritesCreate(context.Background(), f, out, nil)
	if !errors.Is(err, sprites.ErrUnknownPreset) && (err == nil || !strings.Contains(err.Error(), "podman")) {
		t.Fatalf("err = %v", err)
	}
}

func TestHerdrSpritesCreateOrigin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := herdrExecCommandContext
	t.Cleanup(func() { herdrExecCommandContext = old })
	run := func(sync string) (argv []string, err error) {
		herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if name == "git" {
				return exec.CommandContext(ctx, "false")
			}
			argv = args
			return exec.CommandContext(ctx, "true")
		}
		ctx := context.WithValue(context.Background(), herdrSyncKey{}, sync)
		err = herdrSpritesCreate(ctx, &bytes.Buffer{}, "p/n", t.TempDir())
		return
	}
	argv, err := run("")
	if err == nil {
		f, perr := parseSandboxCreateArgs(argv[1:])
		if perr != nil || f.agentName != herdrPrimaryAgent() {
			t.Fatalf("create argv %v: agent=%q err=%v, want %q", argv, f.agentName, perr, herdrPrimaryAgent())
		}
		if names, nerr := spritesSecretNames(config.Config{}, f); nerr != nil || !slices.Contains(names, sprites.SecretClaudeOAuth) {
			t.Fatalf("secret names %v err=%v lack %s", names, nerr, sprites.SecretClaudeOAuth)
		}
	}
	if err != nil || slices.Contains(argv, "--repo") {
		t.Fatalf("bundle no-origin: argv=%v err=%v", argv, err)
	}
	if _, err := run("push"); err == nil {
		t.Fatal("push no-origin: want error")
	}
}
