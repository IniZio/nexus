package cred

import (
	"errors"
	"slices"
	"testing"
)

func TestKiroProfile_Registered(t *testing.T) {
	p, ok := ProfileByName(KiroProfileName)
	if !ok {
		t.Fatal("KiroProfileName is not registered in ProfileByName")
	}
	if p.Name != KiroProfileName {
		t.Errorf("registered profile Name = %q, want %q", p.Name, KiroProfileName)
	}
	names := ProfileNames()
	found := false
	for _, n := range names {
		if n == KiroProfileName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ProfileNames() = %v, missing %q", names, KiroProfileName)
	}
}

func TestKiroProfile_NoCredentialSwap(t *testing.T) {
	p := MustProfileByName(KiroProfileName)

	if p.CredentialFormat != CredentialFormatNone {
		t.Errorf("CredentialFormat = %q, want CredentialFormatNone — kiro has no file the JSON seeder can swap", p.CredentialFormat)
	}
	if p.APIKeyEnvVar != "" {
		t.Errorf("APIKeyEnvVar = %q, want empty — %s is a Bearer on more than one host; the env placeholder is registered only for CredentialedHost", p.APIKeyEnvVar, KiroAPIKeyEnv)
	}
	if p.PlaceholderEnvVar != "" {
		t.Errorf("PlaceholderEnvVar = %q, want empty", p.PlaceholderEnvVar)
	}
	if p.CredentialFile != "" {
		t.Errorf("CredentialFile = %q, want empty — kiro does not read a JSON credential file", p.CredentialFile)
	}
	if p.CredentialedHost != "management.us-east-1.kiro.dev" {
		t.Errorf("CredentialedHost = %q, want management.us-east-1.kiro.dev — first host that carried Authorization: Bearer on GetProfile", p.CredentialedHost)
	}
	if p.CredentialedHostSuffix != "" {
		t.Errorf("CredentialedHostSuffix = %q, want empty — a .kiro.dev suffix would also cover download and telemetry hosts", p.CredentialedHostSuffix)
	}
	if !slices.Contains(p.EgressHosts, p.CredentialedHost) {
		t.Errorf("EgressHosts %v must contain CredentialedHost %q", p.EgressHosts, p.CredentialedHost)
	}
	if len(p.EgressHosts) != 1 {
		t.Errorf("EgressHosts = %v, want only the one observed management host", p.EgressHosts)
	}
	src, err := NewCredentialSourceForProfile(p)
	if err != nil {
		t.Fatalf("NewCredentialSourceForProfile: %v", err)
	}
	if src != nil {
		t.Errorf("NewCredentialSourceForProfile returned %T, want nil", src)
	}
}

func TestImportKiroCredentials_Refuses(t *testing.T) {
	store, err := ImportKiroCredentials(MustProfileByName(KiroProfileName))
	if err == nil {
		t.Fatal("ImportKiroCredentials returned nil error; want ErrKiroNoBroker")
	}
	if !errors.Is(err, ErrKiroNoBroker) {
		t.Errorf("ImportKiroCredentials err = %v, want errors.Is ErrKiroNoBroker", err)
	}
	if store != nil {
		t.Errorf("ImportKiroCredentials store = %+v, want nil", store)
	}
}

func TestKiroProfile_ToolRecipeShape(t *testing.T) {
	r := MustProfileByName(KiroProfileName).ToolRecipe
	if err := r.Validate(); err != nil {
		t.Fatalf("ToolRecipe.Validate: %v", err)
	}
	if r.BinPath != "/usr/local/bin/kiro-cli" {
		t.Errorf("BinPath = %q, want /usr/local/bin/kiro-cli", r.BinPath)
	}
	if len(r.Packages) != 1 {
		t.Fatalf("Packages has %d entries; want 1 tarball", len(r.Packages))
	}
	pkg := r.Packages[0]
	if pkg.Kind != RecipeKindTarball {
		t.Errorf("Kind = %q, want tarball (official artifact is kirocli-*-linux.tar.gz, not npm or OCI)", pkg.Kind)
	}
	if pkg.Name != "kiro-cli" {
		t.Errorf("Name = %q, want kiro-cli", pkg.Name)
	}
	if pkg.Version != KiroCLIVersion {
		t.Errorf("Version = %q, want %s", pkg.Version, KiroCLIVersion)
	}
	if pkg.IsFloating() {
		t.Error("Version must be pinned — tarball recipes cannot use FloatingVersion")
	}
	const wantURL = "https://prod.download.cli.kiro.dev/stable/{VERSION}/kirocli-x86_64-linux.tar.gz"
	if pkg.URLTemplate != wantURL {
		t.Errorf("URLTemplate = %q, want %q", pkg.URLTemplate, wantURL)
	}
	if got := pkg.SHA256ByArch["x64"]; got != KiroTarballSHA256X64 {
		t.Errorf("SHA256ByArch[x64] = %q, want %s", got, KiroTarballSHA256X64)
	}
	if _, ok := pkg.SHA256ByArch["arm64"]; ok {
		t.Error("SHA256ByArch must not contain arm64 — the URL is the x86_64 tarball; an arm64 hash would verify the wrong file")
	}
	if pkg.InstallDir != "/usr/local/share/kiro-cli/{VERSION}" {
		t.Errorf("InstallDir = %q, want /usr/local/share/kiro-cli/{VERSION}", pkg.InstallDir)
	}
	wantLinks := map[string]string{
		"/usr/local/bin/kiro-cli":      "/usr/local/share/kiro-cli/{VERSION}/bin/kiro-cli",
		"/usr/local/bin/kiro-cli-chat": "/usr/local/share/kiro-cli/{VERSION}/bin/kiro-cli-chat",
	}
	if len(pkg.Symlinks) != len(wantLinks) {
		t.Fatalf("Symlinks = %+v, want %d entries", pkg.Symlinks, len(wantLinks))
	}
	for _, sl := range pkg.Symlinks {
		target, ok := wantLinks[sl.LinkPath]
		if !ok {
			t.Errorf("unexpected symlink %q", sl.LinkPath)
			continue
		}
		if sl.TargetPath != target {
			t.Errorf("symlink %s target = %q, want %q", sl.LinkPath, sl.TargetPath, target)
		}
	}
}
