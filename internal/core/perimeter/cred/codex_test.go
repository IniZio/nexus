package cred

import (
	"errors"
	"slices"
	"testing"
)

func TestCodexProfile_Registered(t *testing.T) {
	p, ok := ProfileByName(CodexProfileName)
	if !ok {
		t.Fatal("CodexProfileName is not registered in ProfileByName")
	}
	if p.Name != CodexProfileName {
		t.Errorf("registered profile Name = %q, want %q", p.Name, CodexProfileName)
	}
	names := ProfileNames()
	if !slices.Contains(names, CodexProfileName) {
		t.Errorf("ProfileNames() = %v, missing %q", names, CodexProfileName)
	}
}

func TestCodexProfile_NoCredentialSwap(t *testing.T) {
	p := MustProfileByName(CodexProfileName)

	if p.CredentialFormat != CredentialFormatNone {
		t.Errorf("CredentialFormat = %q, want CredentialFormatNone — the seeder cannot write Codex's nested auth.json", p.CredentialFormat)
	}
	if p.APIKeyEnvVar != "" {
		t.Errorf("APIKeyEnvVar = %q, want empty — CODEX_API_KEY is a bearer on api.openai.com, not CredentialedHost", p.APIKeyEnvVar)
	}
	if p.PlaceholderEnvVar != "" {
		t.Errorf("PlaceholderEnvVar = %q, want empty — CODEX_ACCESS_TOKEN is sent to auth.openai.com and chatgpt.com", p.PlaceholderEnvVar)
	}
	if p.PlaceholderIsJWT {
		t.Error("PlaceholderIsJWT must be false — there is no placeholder to shape")
	}
	if p.CredentialFile != "" {
		t.Errorf("CredentialFile = %q, want empty — auth.json is nested tokens.*, not a flat key", p.CredentialFile)
	}
	if p.CredentialedHost != "chatgpt.com" {
		t.Errorf("CredentialedHost = %q, want chatgpt.com — ChatGPT inference base is https://chatgpt.com/backend-api/codex", p.CredentialedHost)
	}
	if p.CredentialedHostSuffix != "" {
		t.Errorf("CredentialedHostSuffix = %q, want empty — a suffix would also cover hosts that must not see the bearer", p.CredentialedHostSuffix)
	}
	if !slices.Contains(p.EgressHosts, p.CredentialedHost) {
		t.Errorf("EgressHosts %v must contain CredentialedHost %q", p.EgressHosts, p.CredentialedHost)
	}
	if len(p.EgressHosts) != 1 {
		t.Errorf("EgressHosts = %v, want only chatgpt.com — auth.openai.com and api.openai.com also receive bearers and are not swapped", p.EgressHosts)
	}
	if p.Capabilities.GuestNoSelfRefresh {
		t.Error("GuestNoSelfRefresh must be false — there is no brokered token for the guest to refrain from refreshing")
	}

	src, err := NewCredentialSourceForProfile(p)
	if err != nil {
		t.Fatalf("NewCredentialSourceForProfile: %v", err)
	}
	if src != nil {
		t.Errorf("NewCredentialSourceForProfile returned %T, want nil", src)
	}
}

func TestImportCodexCredentials_Refuses(t *testing.T) {
	store, err := ImportCodexCredentials(MustProfileByName(CodexProfileName))
	if err == nil {
		t.Fatal("ImportCodexCredentials returned nil error; want ErrCodexNoBroker")
	}
	if !errors.Is(err, ErrCodexNoBroker) {
		t.Errorf("ImportCodexCredentials err = %v, want errors.Is ErrCodexNoBroker", err)
	}
	if store != nil {
		t.Errorf("ImportCodexCredentials store = %+v, want nil", store)
	}
}

func TestCodexOAuthConstants_MatchUpstream(t *testing.T) {
	// MUTATION-PIN: these are copied from codex-rs/login/src/auth/manager.rs.
	// Pointing NewRefresher at them would still post the wrong body; the
	// constants stay so a later broker does not invent Claude Code's values.
	if CodexClientID != "app_EMoamEEZ73f0CkXaXp7hrann" {
		t.Errorf("CodexClientID = %q", CodexClientID)
	}
	if CodexTokenEndpoint != "https://auth.openai.com/oauth/token" {
		t.Errorf("CodexTokenEndpoint = %q", CodexTokenEndpoint)
	}
}

func TestCodexProfile_ToolRecipeShape(t *testing.T) {
	r := MustProfileByName(CodexProfileName).ToolRecipe
	if err := r.Validate(); err != nil {
		t.Fatalf("ToolRecipe.Validate: %v", err)
	}
	if r.BinPath != "/usr/local/bin/codex" {
		t.Errorf("BinPath = %q, want /usr/local/bin/codex", r.BinPath)
	}
	if len(r.Packages) != 1 {
		t.Fatalf("Packages has %d entries; want 1 OCI package", len(r.Packages))
	}
	pkg := r.Packages[0]
	if pkg.Kind != RecipeKindOCI {
		t.Errorf("Kind = %q, want oci — nexus copies the standalone binary the same way as claude-code and cursor", pkg.Kind)
	}
	if pkg.Image != "docker/sandbox-templates:codex-nightly" {
		t.Errorf("Image = %q", pkg.Image)
	}
	if pkg.SrcPath != "/home/agent/.codex/packages/standalone/releases/" {
		t.Errorf("SrcPath = %q", pkg.SrcPath)
	}
	if pkg.BinRel != "bin/codex" {
		t.Errorf("BinRel = %q, want bin/codex", pkg.BinRel)
	}
	if !pkg.IsFloating() {
		t.Error("Version must float — the nightly tag is resolved to a digest at create time")
	}
	if len(pkg.Symlinks) != 1 || pkg.Symlinks[0].LinkPath != "/usr/local/bin/codex" {
		t.Errorf("Symlinks = %+v", pkg.Symlinks)
	}
}
