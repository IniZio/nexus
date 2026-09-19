package cred

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeOpencodeDir(t *testing.T, content string) (dataHome string, authPath string) {
	t.Helper()
	dir := t.TempDir()
	credDir := filepath.Join(dir, "opencode")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("makeOpencodeDir mkdir: %v", err)
	}
	p := filepath.Join(credDir, "auth.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("makeOpencodeDir write: %v", err)
	}
	return dir, p
}

func opencodeAuthJSON(t *testing.T, providers map[string]any) string {
	t.Helper()
	b, err := json.Marshal(providers)
	if err != nil {
		t.Fatalf("opencodeAuthJSON: %v", err)
	}
	return string(b)
}

func opencodeTestProfile() AgentProfile { return OpencodeProfile }

func TestImportOpencodeCredentials_EmptyKey(t *testing.T) {
	dataHome, _ := makeOpencodeDir(t, opencodeAuthJSON(t, map[string]any{
		OpencodeGoProviderID: map[string]string{"type": "api", "key": ""},
	}))
	t.Setenv("XDG_DATA_HOME", dataHome)

	store, err := ImportOpencodeCredentials(opencodeTestProfile())
	if err == nil {
		t.Fatalf("expected error for empty key; got nil (store=%+v)", store)
	}
	if store != nil {
		t.Errorf("expected nil store on error; got non-nil")
	}
	if !containsStr(err.Error(), "empty") {
		t.Errorf("error %q does not mention 'empty'", err)
	}
	if !containsStr(err.Error(), "unusable") {
		t.Errorf("error %q does not mention 'unusable'", err)
	}
}

func TestImportOpencodeCredentials_MissingFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	_, err := ImportOpencodeCredentials(opencodeTestProfile())
	if err == nil {
		t.Fatal("expected error for missing file; got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error does not wrap os.ErrNotExist: %v", err)
	}
}

func TestImportOpencodeCredentials_MissingProvider(t *testing.T) {
	dataHome, _ := makeOpencodeDir(t, opencodeAuthJSON(t, map[string]any{
		"anthropic": map[string]string{"type": "api", "key": "other-provider-key"},
	}))
	t.Setenv("XDG_DATA_HOME", dataHome)

	store, err := ImportOpencodeCredentials(opencodeTestProfile())
	if err == nil {
		t.Fatalf("expected error when %s is absent; got nil (store token hidden)", OpencodeGoProviderID)
	}
	if store != nil {
		t.Fatal("expected nil store when the provider is absent")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("absent provider must not be reported as a missing file: %v", err)
	}
	if !containsStr(err.Error(), OpencodeGoProviderID) {
		t.Errorf("error %q does not name %s", err, OpencodeGoProviderID)
	}
	if containsStr(err.Error(), "other-provider-key") {
		t.Error("error leaked another provider's key")
	}
}

func TestImportOpencodeCredentials_WrongType(t *testing.T) {
	dataHome, _ := makeOpencodeDir(t, opencodeAuthJSON(t, map[string]any{
		OpencodeGoProviderID: map[string]string{
			"type":    "oauth",
			"refresh": "r",
			"access":  "a",
			"key":     "oauth-grant-not-an-api-key",
		},
	}))
	t.Setenv("XDG_DATA_HOME", dataHome)

	store, err := ImportOpencodeCredentials(opencodeTestProfile())
	if err == nil {
		t.Fatal("expected error for oauth entry; got nil")
	}
	if store != nil {
		t.Fatal("expected nil store for non-api entry")
	}
	if !containsStr(err.Error(), "api") {
		t.Errorf("error %q does not say the wanted type", err)
	}
}

func TestImportOpencodeCredentials_Valid(t *testing.T) {
	const want = "oc-test-key-not-a-secret"
	dataHome, _ := makeOpencodeDir(t, opencodeAuthJSON(t, map[string]any{
		OpencodeGoProviderID: map[string]string{"type": "api", "key": want},
		"anthropic":          map[string]string{"type": "api", "key": "other-provider-key"},
	}))
	t.Setenv("XDG_DATA_HOME", dataHome)

	store, err := ImportOpencodeCredentials(opencodeTestProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.AccessToken != want {
		t.Error("AccessToken mismatch (value hidden)")
	}
	if store.AccessToken == "other-provider-key" {
		t.Error("imported another provider's key")
	}
	if store.RefreshToken != "" {
		t.Errorf("RefreshToken = %q; want empty", store.RefreshToken)
	}
	if store.TokenType != "Bearer" {
		t.Errorf("TokenType = %q; want Bearer", store.TokenType)
	}
	if !store.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v; want zero (static API key has no exp claim)", store.ExpiresAt)
	}
	if store.ClientID != "" || store.ClientSecret != "" || store.TokenEndpoint != "" {
		t.Error("static API key must not carry OAuth client plumbing")
	}
}

func TestImportOpencodeCredentials_JWTShapedKeyIsStillStatic(t *testing.T) {
	tok := makeSyntheticJWT(t, map[string]any{"exp": float64(1893456000)})
	dataHome, _ := makeOpencodeDir(t, opencodeAuthJSON(t, map[string]any{
		OpencodeGoProviderID: map[string]string{"type": "api", "key": tok},
	}))
	t.Setenv("XDG_DATA_HOME", dataHome)

	store, err := ImportOpencodeCredentials(opencodeTestProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.AccessToken != tok {
		t.Error("AccessToken mismatch (value hidden)")
	}
	if !store.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v; JWT-shaped key must not be parsed for exp", store.ExpiresAt)
	}
}

func TestNewOpencodeCredentialSource(t *testing.T) {
	const want = "oc-test-key-not-a-secret"
	dataHome, _ := makeOpencodeDir(t, opencodeAuthJSON(t, map[string]any{
		OpencodeGoProviderID: map[string]string{"type": "api", "key": want},
	}))
	t.Setenv("XDG_DATA_HOME", dataHome)

	src, err := NewOpencodeCredentialSource(opencodeTestProfile())
	if err != nil {
		t.Fatalf("NewOpencodeCredentialSource: %v", err)
	}
	got, exp, err := src.Token(nil)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != want {
		t.Error("Token mismatch (value hidden)")
	}
	if !exp.IsZero() {
		t.Errorf("ExpiresAt = %v; want zero", exp)
	}
}

func TestNewCredentialSourceForProfile_Opencode(t *testing.T) {
	const want = "oc-test-key-not-a-secret"
	dataHome, _ := makeOpencodeDir(t, opencodeAuthJSON(t, map[string]any{
		OpencodeGoProviderID: map[string]string{"type": "api", "key": want},
	}))
	t.Setenv("XDG_DATA_HOME", dataHome)

	src, err := NewCredentialSourceForProfile(opencodeTestProfile())
	if err != nil {
		t.Fatalf("NewCredentialSourceForProfile: %v", err)
	}
	if src == nil {
		t.Fatal("NewCredentialSourceForProfile returned nil source")
	}
	got, _, err := src.Token(nil)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != want {
		t.Error("Token mismatch (value hidden)")
	}
}

func TestCheckCred_OpencodeAbsent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	got := CheckCred(opencodeTestProfile())
	if got.Reason != PreflightAbsent {
		t.Fatalf("Reason = %v, want PreflightAbsent", got.Reason)
	}
}

func TestOpencodeCredPath_EnvVar(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/xdg")
	p, err := OpencodeCredPath(opencodeTestProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/custom/xdg/opencode/auth.json"
	if p != want {
		t.Errorf("path = %q; want %q", p, want)
	}
}

func TestOpencodeCredPath_DefaultIsDataDirNotConfig(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	p, err := OpencodeCredPath(opencodeTestProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(p, "/.local/share/opencode/auth.json") {
		t.Errorf("path %q does not end with /.local/share/opencode/auth.json", p)
	}
	if strings.Contains(p, "/.config/opencode/auth.json") {
		t.Errorf("path %q used the config dir; OpenCode stores auth.json under the data dir", p)
	}
}

func TestOpencodeProfile_Registered(t *testing.T) {
	p, ok := ProfileByName(OpencodeProfileName)
	if !ok {
		t.Fatal("OpencodeProfileName is not registered")
	}
	if p.Name != OpencodeProfileName {
		t.Errorf("Name = %q, want %q", p.Name, OpencodeProfileName)
	}
	if p.PlaceholderEnvVar != "OPENCODE_API_KEY" {
		t.Errorf("PlaceholderEnvVar = %q, want OPENCODE_API_KEY", p.PlaceholderEnvVar)
	}
	if p.APIKeyEnvVar != "OPENCODE_API_KEY" {
		t.Errorf("APIKeyEnvVar = %q, want OPENCODE_API_KEY", p.APIKeyEnvVar)
	}
	if p.PlaceholderIsJWT {
		t.Error("PlaceholderIsJWT must be false — opencode forwards the key as an API key and does not JWT-parse it")
	}
	if p.CredentialedHost != "opencode.ai" {
		t.Errorf("CredentialedHost = %q, want opencode.ai", p.CredentialedHost)
	}
	found := false
	for _, h := range p.EgressHosts {
		if h == p.CredentialedHost {
			found = true
		}
	}
	if !found {
		t.Errorf("EgressHosts %v must contain CredentialedHost %q", p.EgressHosts, p.CredentialedHost)
	}
	if p.CredentialFormat != CredentialFormatOpencodeAPIKey {
		t.Errorf("CredentialFormat = %q, want %q", p.CredentialFormat, CredentialFormatOpencodeAPIKey)
	}
	if p.CredDirEnvVar != "XDG_DATA_HOME" {
		t.Errorf("CredDirEnvVar = %q, want XDG_DATA_HOME", p.CredDirEnvVar)
	}
	if p.CredentialFile != "opencode/auth.json" {
		t.Errorf("CredentialFile = %q, want opencode/auth.json", p.CredentialFile)
	}
}

func TestOpencodeProfile_ToolRecipePinsNPM(t *testing.T) {
	r := OpencodeProfile.ToolRecipe
	if r.BinPath != "/usr/local/bin/opencode" {
		t.Errorf("BinPath = %q, want /usr/local/bin/opencode", r.BinPath)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	var npm RecipePackage
	found := false
	for _, pkg := range r.Packages {
		if pkg.Kind == RecipeKindNPM {
			npm = pkg
			found = true
			break
		}
	}
	if !found {
		t.Fatal("ToolRecipe has no npm package")
	}
	if npm.Name != "opencode-ai" {
		t.Errorf("npm Name = %q, want opencode-ai", npm.Name)
	}
	if npm.Version == "" || npm.Version == FloatingVersion {
		t.Errorf("npm Version = %q; want an exact pin", npm.Version)
	}
	if npm.Version != "1.18.31" {
		t.Errorf("npm Version = %q, want 1.18.31", npm.Version)
	}
}
