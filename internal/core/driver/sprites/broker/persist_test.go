package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func persistCfg(dir string, id domain.SandboxID) CredsConfig {
	return CredsConfig{
		SandboxID:  id,
		PersistDir: dir,
		Secrets: []Secret{
			{Name: "GH_TOKEN", Value: realGH, Hosts: []string{"github.com", "api.github.com"}, GitHubRepo: "acme/widgets"},
			{Name: "CLAUDE_CODE_OAUTH_TOKEN", Value: realClaude, Hosts: []string{"api.anthropic.com"}},
		},
	}
}

func TestNewCreds_PersistStableAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()
	a, err := NewCreds(persistCfg(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewCreds(persistCfg(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range a.Env {
		if b.Env[k] != v {
			t.Fatalf("placeholder %s changed across restart", k)
		}
	}
	if len(a.Env) != 2 || len(b.Env) != 2 {
		t.Fatalf("env: %v %v", a.Env, b.Env)
	}
	if hexSHA(a.CACertPEM) != hexSHA(b.CACertPEM) {
		t.Fatal("CA fingerprint changed across restart")
	}

	c, err := NewCreds(persistCfg(t.TempDir(), id))
	if err != nil {
		t.Fatal(err)
	}
	if hexSHA(c.CACertPEM) == hexSHA(a.CACertPEM) {
		t.Fatal("fresh dir reused CA")
	}
	for k, v := range a.Env {
		if c.Env[k] == v {
			t.Fatalf("fresh dir reused placeholder %s", k)
		}
	}
}

func TestNewCreds_PersistedPlaceholderResolves(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()
	if _, err := NewCreds(persistCfg(dir, id)); err != nil {
		t.Fatal(err)
	}
	c, up, cl := fixture(t, func(c *CredsConfig) { c.PersistDir = dir; c.SandboxID = id })
	status, err := do(t, cl, "https://api.github.com/repos/acme/widgets", "Bearer "+c.Env["GH_TOKEN"])
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if got := up.last(); got != "Bearer "+realGH {
		t.Fatalf("upstream auth not swapped: %q", got)
	}
}

func TestNewCreds_PersistFilesPrivateAndTokenFree(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewCreds(persistCfg(dir, domain.NewSandboxID())); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		t.Fatalf("no persisted files: %v", err)
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", e.Name(), fi.Mode().Perm())
		}
		data, _ := os.ReadFile(p)
		if strings.Contains(string(data), realGH) || strings.Contains(string(data), realClaude) {
			t.Fatalf("%s contains a real token", e.Name())
		}
	}
}

func TestNewCreds_CorruptPersistRegenerates(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()
	a, err := NewCreds(persistCfg(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, placeholdersFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := NewCreds(persistCfg(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if b.Env["GH_TOKEN"] == a.Env["GH_TOKEN"] || len(b.Env["GH_TOKEN"]) != 64 {
		t.Fatalf("expected fresh valid placeholder, got %q", b.Env["GH_TOKEN"])
	}
}

func TestNewCreds_UppercaseHexPersistRegenerates(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()
	a, err := NewCreds(persistCfg(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	up := strings.ToUpper(a.Env["GH_TOKEN"])
	if err := os.WriteFile(filepath.Join(dir, placeholdersFile), []byte(`{"GH_TOKEN":"`+up+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadPlaceholders(dir); len(got) != 0 {
		t.Fatalf("uppercase hex accepted: %v", got)
	}
	b, err := NewCreds(persistCfg(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if b.Env["GH_TOKEN"] == up || b.Env["GH_TOKEN"] != strings.ToLower(b.Env["GH_TOKEN"]) {
		t.Fatalf("not regenerated: %q", b.Env["GH_TOKEN"])
	}
}
