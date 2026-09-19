package cred

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func ohMyPiTestProfile() AgentProfile { return OhMyPiProfile }

func clearOhMyPiPathEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("PI_CONFIG_DIR", "")
	t.Setenv("OMP_PROFILE", "")
	os.Unsetenv("OMP_PROFILE")
	t.Setenv("PI_PROFILE", "")
	t.Setenv("XDG_DATA_HOME", "")
}

// TestOhMyPiCredPath_AgentDirEnv verifies PI_CODING_AGENT_DIR is the vault
// directory and wins over the XDG redirect.
func TestOhMyPiCredPath_AgentDirEnv(t *testing.T) {
	dir := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "omp"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("PI_CONFIG_DIR", "")
	os.Unsetenv("OMP_PROFILE")

	got, err := OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath: %v", err)
	}
	want := filepath.Join(dir, "agent.db")
	if got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

// TestOhMyPiCredPath_Default verifies the unset-env fallback ~/.omp/agent/agent.db.
func TestOhMyPiCredPath_Default(t *testing.T) {
	clearOhMyPiPathEnv(t)
	got, err := OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".omp", "agent", "agent.db")
	if got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

// TestOhMyPiCredPath_ConfigDirEnv verifies PI_CONFIG_DIR replaces the ".omp" segment.
func TestOhMyPiCredPath_ConfigDirEnv(t *testing.T) {
	clearOhMyPiPathEnv(t)
	t.Setenv("PI_CONFIG_DIR", "custom-omp")
	got, err := OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "custom-omp", "agent", "agent.db")
	if got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

// TestOhMyPiCredPath_XDGDataHomeOnlyWhenAppDirExists verifies the XDG redirect
// is conditional on $XDG_DATA_HOME/omp already existing. A set variable with
// no app directory must not move the vault.
func TestOhMyPiCredPath_XDGDataHomeOnlyWhenAppDirExists(t *testing.T) {
	clearOhMyPiPathEnv(t)
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)

	got, err := OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".omp", "agent", "agent.db")
	if got != want {
		t.Errorf("path without $XDG_DATA_HOME/omp = %q; want %q", got, want)
	}

	if err := os.MkdirAll(filepath.Join(xdg, "omp"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath: %v", err)
	}
	want = filepath.Join(xdg, "omp", "agent.db")
	if got != want {
		t.Errorf("path with $XDG_DATA_HOME/omp = %q; want %q", got, want)
	}
}

// TestOhMyPiCredPath_ProfileEnv verifies OMP_PROFILE nests the vault and an
// explicitly empty OMP_PROFILE does not inherit PI_PROFILE.
func TestOhMyPiCredPath_ProfileEnv(t *testing.T) {
	clearOhMyPiPathEnv(t)
	t.Setenv("OMP_PROFILE", "work")
	got, err := OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".omp", "profiles", "work", "agent", "agent.db")
	if got != want {
		t.Errorf("path = %q; want %q", got, want)
	}

	t.Setenv("OMP_PROFILE", "")
	os.Unsetenv("OMP_PROFILE")
	t.Setenv("PI_PROFILE", "legacy")
	got, err = OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath PI_PROFILE: %v", err)
	}
	want = filepath.Join(home, ".omp", "profiles", "legacy", "agent", "agent.db")
	if got != want {
		t.Errorf("PI_PROFILE path = %q; want %q", got, want)
	}

	t.Setenv("OMP_PROFILE", "")
	t.Setenv("PI_PROFILE", "legacy")
	got, err = OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath empty OMP_PROFILE: %v", err)
	}
	want = filepath.Join(home, ".omp", "agent", "agent.db")
	if got != want {
		t.Errorf("empty OMP_PROFILE path = %q; want default %q (must not inherit PI_PROFILE)", got, want)
	}
}

// TestOhMyPiCredPath_NamedProfileIgnoresAgentDir verifies a named profile
// derives its own agent directory. PI_CODING_AGENT_DIR is a default-profile
// override only (dirs.ts resolveActiveAgentDirOverride).
func TestOhMyPiCredPath_NamedProfileIgnoresAgentDir(t *testing.T) {
	clearOhMyPiPathEnv(t)
	dir := t.TempDir()
	t.Setenv("OMP_PROFILE", "work")
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	got, err := OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".omp", "profiles", "work", "agent", "agent.db")
	if got != want {
		t.Errorf("path = %q; want %q (named profile must ignore PI_CODING_AGENT_DIR %q)", got, want, dir)
	}
}

// TestOhMyPiCredPath_BypassedProfileDirIsNotAnOverride verifies that an
// explicitly empty OMP_PROFILE does not treat PI_CODING_AGENT_DIR as an
// override when that value is exactly the bypassed PI_PROFILE agent dir.
func TestOhMyPiCredPath_BypassedProfileDirIsNotAnOverride(t *testing.T) {
	clearOhMyPiPathEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	derived := filepath.Join(home, ".omp", "profiles", "legacy", "agent")
	t.Setenv("OMP_PROFILE", "")
	t.Setenv("PI_PROFILE", "legacy")
	t.Setenv("PI_CODING_AGENT_DIR", derived)

	got, err := OhMyPiCredPath()
	if err != nil {
		t.Fatalf("OhMyPiCredPath: %v", err)
	}
	want := filepath.Join(home, ".omp", "agent", "agent.db")
	if got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

func TestOhMyPiCredPath_InvalidProfile(t *testing.T) {
	clearOhMyPiPathEnv(t)
	t.Setenv("OMP_PROFILE", "../escape")
	_, err := OhMyPiCredPath()
	if err == nil {
		t.Fatal("expected error for invalid OMP_PROFILE; got nil")
	}
}

func TestImportOhMyPiCredentials_MissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	_, err := ImportOhMyPiCredentials(ohMyPiTestProfile())
	if err == nil {
		t.Fatal("expected error for missing vault; got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error does not wrap os.ErrNotExist: %v", err)
	}
}

func TestImportOhMyPiCredentials_RefusesSQLiteVault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	path := filepath.Join(dir, "agent.db")
	body := append([]byte("SQLite format 3\x00"), []byte("provider-row-must-not-be-guessed")...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := ImportOhMyPiCredentials(ohMyPiTestProfile())
	if store != nil {
		t.Fatalf("import returned a store for a SQLite vault; want nil (refusing to guess a provider row)")
	}
	if !errors.Is(err, ErrOhMyPiVaultNotImportable) {
		t.Fatalf("err = %v; want ErrOhMyPiVaultNotImportable", err)
	}
}

func TestImportOhMyPiCredentials_RejectsNonSQLite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	path := filepath.Join(dir, "agent.db")
	if err := os.WriteFile(path, []byte(`{"accessToken":"not-the-omp-shape"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ImportOhMyPiCredentials(ohMyPiTestProfile())
	if store != nil {
		t.Fatal("import returned a store for a non-SQLite file")
	}
	if err == nil {
		t.Fatal("expected error for non-SQLite file")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("non-SQLite file reported as missing: %v", err)
	}
	if errors.Is(err, ErrOhMyPiVaultNotImportable) {
		t.Errorf("non-SQLite file reported as a vault: %v", err)
	}
	if !containsStr(err.Error(), "not a SQLite vault") {
		t.Errorf("error %q does not say the file is not a SQLite vault", err)
	}
}

func TestOhMyPiProfile_Registered(t *testing.T) {
	p, ok := ProfileByName(OhMyPiProfileName)
	if !ok {
		t.Fatal("OhMyPiProfileName is not registered")
	}
	if p.Name != OhMyPiProfileName {
		t.Errorf("Name = %q, want %q", p.Name, OhMyPiProfileName)
	}
	found := false
	for _, n := range ProfileNames() {
		if n == OhMyPiProfileName {
			found = true
		}
	}
	if !found {
		t.Errorf("ProfileNames() = %v, missing %q", ProfileNames(), OhMyPiProfileName)
	}
}

func TestOhMyPiProfile_ToolRecipePinnedNPM(t *testing.T) {
	r := OhMyPiProfile.ToolRecipe
	if r.BinPath != "/usr/local/bin/omp" {
		t.Errorf("BinPath = %q; want /usr/local/bin/omp", r.BinPath)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(r.Packages) != 3 {
		t.Fatalf("packages = %+v; want node tarball, bun, and the agent", r.Packages)
	}
	node := r.Packages[0]
	if node.Kind != RecipeKindTarball || node.Name != "node" || node.Version != "22.23.2" {
		t.Fatalf("packages[0] = %+v; want node tarball 22.23.2 ahead of the npm installs", node)
	}
	if node.InstallDir != "/usr/local" || node.VersionCmd != "node --version" {
		t.Errorf("node install = dir %q cmd %q; want /usr/local and node --version", node.InstallDir, node.VersionCmd)
	}
	if node.SHA256ByArch["x64"] != "b294a556e639d64338823920e5866c21c02741742d2e1529ee1a225c1ec9252a" {
		t.Errorf("node x64 hash = %q", node.SHA256ByArch["x64"])
	}
	if node.SHA256ByArch["arm64"] != "013b59cfd2819703a6f4a14ab891fc46fc2a4e3f5bcd92de3fb4929b43e35b30" {
		t.Errorf("node arm64 hash = %q", node.SHA256ByArch["arm64"])
	}
	var sawAgent, sawBun bool
	for _, pkg := range r.Packages[1:] {
		if pkg.Kind != RecipeKindNPM {
			t.Errorf("package %q kind = %q; want npm", pkg.Name, pkg.Kind)
		}
		if pkg.Version == "" || pkg.Version == FloatingVersion {
			t.Errorf("package %q version = %q; want an exact pin", pkg.Name, pkg.Version)
		}
		switch pkg.Name {
		case "@oh-my-pi/pi-coding-agent":
			sawAgent = true
			if pkg.Version != "18.2.6" {
				t.Errorf("@oh-my-pi/pi-coding-agent version = %q; want 18.2.6", pkg.Version)
			}
		case "bun":
			sawBun = true
			if pkg.Version != "1.4.2" {
				t.Errorf("bun version = %q; want 1.4.2", pkg.Version)
			}
		default:
			t.Errorf("unexpected package %q", pkg.Name)
		}
	}
	if !sawAgent || !sawBun {
		t.Fatalf("recipe packages = %+v; want bun@1.4.2 and @oh-my-pi/pi-coding-agent@18.2.6", r.Packages)
	}
}

func TestOhMyPiProfile_CredentialFields(t *testing.T) {
	p := OhMyPiProfile
	if p.CredentialFormat != CredentialFormatOhMyPiVault {
		t.Errorf("CredentialFormat = %q; want %q", p.CredentialFormat, CredentialFormatOhMyPiVault)
	}
	if p.CredentialFile != "" {
		t.Errorf("CredentialFile = %q; want empty — seeding JSON over agent.db would not be a vault omp reads", p.CredentialFile)
	}
	if p.PlaceholderEnvVar != "ANTHROPIC_OAUTH_TOKEN" {
		t.Errorf("PlaceholderEnvVar = %q; want ANTHROPIC_OAUTH_TOKEN", p.PlaceholderEnvVar)
	}
	if p.APIKeyEnvVar != "ANTHROPIC_API_KEY" {
		t.Errorf("APIKeyEnvVar = %q; want ANTHROPIC_API_KEY", p.APIKeyEnvVar)
	}
	if p.CredentialedHost != "api.anthropic.com" {
		t.Errorf("CredentialedHost = %q; want api.anthropic.com", p.CredentialedHost)
	}
	if len(p.EgressHosts) != 3 || p.EgressHosts[0] != "api.anthropic.com" || p.EgressHosts[1] != "api.openai.com" || p.EgressHosts[2] != "auth.openai.com" {
		t.Errorf("EgressHosts = %v; want api.anthropic.com, api.openai.com, auth.openai.com", p.EgressHosts)
	}
	if p.CredDirEnvVar != "PI_CODING_AGENT_DIR" {
		t.Errorf("CredDirEnvVar = %q; want PI_CODING_AGENT_DIR", p.CredDirEnvVar)
	}
}

func TestOhMyPiProfile_CheckCredOKWithoutVault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	got := CheckCred(OhMyPiProfile)
	if !got.OK() {
		t.Fatalf("CheckCred = %v (%q); want OK so sandbox create is not blocked by a missing vault", got.Reason, got.Sentence())
	}
}

func TestOhMyPiProfile_SourceYieldsNoToken(t *testing.T) {
	src, err := NewCredentialSourceForProfile(OhMyPiProfile)
	if err != nil {
		t.Fatalf("NewCredentialSourceForProfile: %v", err)
	}
	if src != nil {
		t.Fatal("source must be nil; the SQLite vault is not a single bearer the broker can swap")
	}
}

func TestOhMyPiProfile_ImportIsNotClaude(t *testing.T) {
	_, fn, ok := OAuthImportReg(OhMyPiProfile)
	if !ok || fn == nil {
		t.Fatal("OAuthImportReg returned no importer; CredentialFormatNone would share Claude's importer")
	}
	_, err := fn(filepath.Join(t.TempDir(), "no-such-vault"))
	if err == nil {
		t.Fatal("expected error for a missing vault")
	}
	if containsStr(err.Error(), "ImportClaudeCredentials") {
		t.Fatalf("oh-my-pi import dispatched to Claude Code: %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing vault: %v", err)
	}
}
