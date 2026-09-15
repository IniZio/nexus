package gitssh_test

import (
	_ "embed"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/IniZio/nexus3/internal/core/gitssh"
)

//go:embed testdata/example-app-config.yaml
var exampleAppConfigYAML []byte

type appConfig struct {
	Egress struct {
		Policy []struct {
			Host  string   `yaml:"host"`
			Paths []string `yaml:"paths"`
		} `yaml:"policy"`
	} `yaml:"egress"`
}

func TestDeriveAllowlist_ExampleApp(t *testing.T) {
	var cfg appConfig
	if err := yaml.Unmarshal(exampleAppConfigYAML, &cfg); err != nil {
		t.Fatalf("unmarshal .nexus/config.yaml: %v", err)
	}

	policies := make([]gitssh.HostPolicy, 0, len(cfg.Egress.Policy))
	for _, p := range cfg.Egress.Policy {
		policies = append(policies, gitssh.HostPolicy{Host: p.Host, Paths: p.Paths})
	}

	allowlist := gitssh.DeriveAllowlist(policies)

	// Must contain the example-app repo.
	want := gitssh.AllowedRepo{SSHHost: "git@github.com", OwnerRepo: "example-org/example-app"}
	found := false
	for _, a := range allowlist {
		if a == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("allowlist does not contain %+v; got %+v", want, allowlist)
	}

	// api.github.com paths must NOT appear as SSH entries.
	for _, a := range allowlist {
		if a.SSHHost == "git@api.github.com" {
			t.Errorf("api.github.com path produced an SSH allowlist entry: %+v", a)
		}
	}

	// inizio/example-app (fork, out of policy) must NOT be in allowlist.
	notWant := gitssh.AllowedRepo{SSHHost: "git@github.com", OwnerRepo: "inizio/example-app"}
	for _, a := range allowlist {
		if a == notWant {
			t.Errorf("allowlist unexpectedly contains out-of-policy repo: %+v", a)
		}
	}
}

func TestDeriveAllowlist_Deduplication(t *testing.T) {
	policies := []gitssh.HostPolicy{
		{Host: "github.com", Paths: []string{"/owner/repo/**", "/owner/repo.git/**", "/owner/repo"}},
	}
	got := gitssh.DeriveAllowlist(policies)
	if len(got) != 1 {
		t.Errorf("want 1 deduplicated entry, got %d: %+v", len(got), got)
	}
	if len(got) > 0 && got[0].OwnerRepo != "owner/repo" {
		t.Errorf("want ownerRepo=owner/repo, got %q", got[0].OwnerRepo)
	}
}

func TestDeriveAllowlist_IgnoresNonGitHub(t *testing.T) {
	policies := []gitssh.HostPolicy{
		{Host: "gitlab.com", Paths: []string{"/owner/repo/**"}},
		{Host: "api.github.com", Paths: []string{"/repos/owner/repo/**"}},
	}
	got := gitssh.DeriveAllowlist(policies)
	if len(got) != 0 {
		t.Errorf("want empty allowlist for non-github.com hosts, got %+v", got)
	}
}
