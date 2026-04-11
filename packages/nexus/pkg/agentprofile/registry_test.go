package agentprofile

import (
	"testing"
)

func TestLookupByCanonicalName(t *testing.T) {
	p := Lookup("claude")
	if p == nil {
		t.Fatal("expected claude profile, got nil")
	}
	if p.Name != "claude" {
		t.Fatalf("expected name claude, got %q", p.Name)
	}
}

func TestLookupByAlias(t *testing.T) {
	p := Lookup("anthropic")
	if p == nil {
		t.Fatal("expected claude profile via alias anthropic, got nil")
	}
	if p.Name != "claude" {
		t.Fatalf("expected canonical name claude, got %q", p.Name)
	}
}

func TestLookupCaseInsensitive(t *testing.T) {
	p := Lookup("CLAUDE")
	if p == nil {
		t.Fatal("expected claude profile for uppercase CLAUDE, got nil")
	}
}

func TestLookupUnknownReturnsNil(t *testing.T) {
	if Lookup("nope-does-not-exist") != nil {
		t.Fatal("expected nil for unknown binding")
	}
}

func TestLookupEmptyReturnsNil(t *testing.T) {
	if Lookup("") != nil {
		t.Fatal("expected nil for empty binding")
	}
}

func TestAllBinariesNonEmpty(t *testing.T) {
	bins := AllBinaries()
	if len(bins) == 0 {
		t.Fatal("expected at least one binary")
	}
	for _, b := range bins {
		if b == "" {
			t.Fatal("AllBinaries must not return empty strings")
		}
	}
}

func TestAllCredFilesNoDuplicates(t *testing.T) {
	files := AllCredFiles()
	seen := make(map[string]struct{})
	for _, f := range files {
		if f == "" {
			t.Fatal("AllCredFiles must not return empty strings")
		}
		if _, ok := seen[f]; ok {
			t.Fatalf("duplicate cred file: %q", f)
		}
		seen[f] = struct{}{}
	}
}

func TestAllInstallPkgsNonEmpty(t *testing.T) {
	pkgs := AllInstallPkgs()
	if len(pkgs) == 0 {
		t.Fatal("expected at least one install package")
	}
	seen := make(map[string]struct{})
	for _, p := range pkgs {
		if p == "" {
			t.Fatal("AllInstallPkgs must not return empty strings")
		}
		if _, ok := seen[p]; ok {
			t.Fatalf("duplicate install pkg: %q", p)
		}
		seen[p] = struct{}{}
	}
}

func TestCodexHasAPIKeyPrefix(t *testing.T) {
	p := Lookup("codex")
	if p == nil {
		t.Fatal("codex profile missing")
	}
	if p.APIKeyPrefix == "" {
		t.Fatal("codex profile must have APIKeyPrefix (distinguishes OAuth tokens from API keys)")
	}
}

func TestProfilesWithEnvVarsHaveAtLeastOneVar(t *testing.T) {
	for _, p := range registry {
		if len(p.EnvVars) == 0 {
			t.Fatalf("profile %q has no EnvVars — every profile must map to at least one env var", p.Name)
		}
	}
}
