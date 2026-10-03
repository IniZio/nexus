package herdrworktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, backend string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "version: 1\n"
	if backend != "" {
		body += "backend: " + backend + "\n"
	}
	if err := os.WriteFile(filepath.Join(repo, ".nexus", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestResolveBackendPrecedence(t *testing.T) {
	tests := []struct {
		name, arg, cfg, env, want string
	}{
		{"arg wins", " Sprites ", "cloud-hypervisor", "cloud-hypervisor", "sprites"},
		{"config beats env", "", "sprites", "cloud-hypervisor", "sprites"},
		{"env used", "", "", "sprites", "sprites"},
		{"none", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NEXUS_BACKEND", tc.env)
			got, err := ResolveBackend(tc.arg, writeCfg(t, tc.cfg))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestResolveBackendUnknown(t *testing.T) {
	t.Setenv("NEXUS_BACKEND", "")
	_, err := ResolveBackend("nope", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `unknown backend "nope" (known:`) {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("NEXUS_BACKEND", "bogus")
	if _, err := ResolveBackend("", t.TempDir()); err == nil {
		t.Fatal("want error for bogus env")
	}
}

func TestResolveBackendMissingConfig(t *testing.T) {
	t.Setenv("NEXUS_BACKEND", "")
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveBackend("", repo)
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}
