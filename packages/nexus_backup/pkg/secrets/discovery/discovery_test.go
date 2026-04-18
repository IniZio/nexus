package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverCodexConfig(t *testing.T) {
	home := t.TempDir()

	// Create mock Codex config
	codexDir := filepath.Join(home, ".config", "codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}

	config := `{
		"refresh_token": "ghr_test123",
		"access_token": "ghu_test456",
		"account": "testuser"
	}`
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	// Test discovery
	configs, err := Discover(home)
	if err != nil {
		t.Fatal(err)
	}

	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}

	if configs[0].Name != "codex" {
		t.Errorf("expected provider 'codex', got %s", configs[0].Name)
	}

	if configs[0].RefreshToken != "ghr_test123" {
		t.Errorf("expected refresh_token 'ghr_test123', got %s", configs[0].RefreshToken)
	}
}

func TestDiscoverOpenCodeAPIKey(t *testing.T) {
	home := t.TempDir()

	// Create mock OpenCode config with API key
	opencodeDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		t.Fatal(err)
	}

	config := `{
		"api_key": "oc_apikey_12345",
		"provider": "github-copilot"
	}`
	if err := os.WriteFile(filepath.Join(opencodeDir, "auth.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	configs, err := Discover(home)
	if err != nil {
		t.Fatal(err)
	}

	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}

	if configs[0].Name != "opencode" {
		t.Errorf("expected provider 'opencode', got %s", configs[0].Name)
	}

	if configs[0].Type != ProviderTypeAPIKey {
		t.Errorf("expected type 'api_key', got %s", configs[0].Type)
	}

	if configs[0].AccessToken != "oc_apikey_12345" {
		t.Errorf("expected api_key 'oc_apikey_12345', got %s", configs[0].AccessToken)
	}
}

func TestDiscoverOpenCodeAccessToken(t *testing.T) {
	home := t.TempDir()

	// Create mock OpenCode config with access token
	opencodeDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		t.Fatal(err)
	}

	config := `{
		"access_token": "oc_token_67890",
		"provider": "github"
	}`
	if err := os.WriteFile(filepath.Join(opencodeDir, "auth.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	configs, err := Discover(home)
	if err != nil {
		t.Fatal(err)
	}

	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}

	if configs[0].Type != ProviderTypeSession {
		t.Errorf("expected type 'session', got %s", configs[0].Type)
	}
}

func TestDiscoverMultipleProviders(t *testing.T) {
	home := t.TempDir()

	// Create Codex config
	codexDir := filepath.Join(home, ".config", "codex")
	os.MkdirAll(codexDir, 0755)
	os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(`{"refresh_token": "ghr_test"}`), 0600)

	// Create OpenCode config
	opencodeDir := filepath.Join(home, ".config", "opencode")
	os.MkdirAll(opencodeDir, 0755)
	os.WriteFile(filepath.Join(opencodeDir, "auth.json"), []byte(`{"api_key": "oc_key"}`), 0600)

	// Create Claude config
	claudeDir := filepath.Join(home, ".config", "claude")
	os.MkdirAll(claudeDir, 0755)
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"sessionToken": "claude_sess"}`), 0600)

	configs, err := Discover(home)
	if err != nil {
		t.Fatal(err)
	}

	if len(configs) != 3 {
		t.Fatalf("expected 3 configs, got %d: %v", len(configs), configs)
	}

	// Check all providers found
	found := make(map[string]bool)
	for _, cfg := range configs {
		found[cfg.Name] = true
	}

	if !found["codex"] {
		t.Error("codex not found")
	}
	if !found["opencode"] {
		t.Error("opencode not found")
	}
	if !found["claude"] {
		t.Error("claude not found")
	}
}

func TestDiscoverNoConfig(t *testing.T) {
	home := t.TempDir()

	configs, err := Discover(home)
	if err != nil {
		t.Fatal(err)
	}

	if len(configs) != 0 {
		t.Fatalf("expected 0 configs, got %d", len(configs))
	}
}

func TestDiscoverGeminiFromDotEnv(t *testing.T) {
	home := t.TempDir()
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(geminiDir, ".env"), []byte("GEMINI_API_KEY=gm_key_123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configs, err := Discover(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cfg := range configs {
		if cfg.Name == "gemini" && cfg.AccessToken == "gm_key_123" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected gemini provider from ~/.gemini/.env")
	}
}

func TestDiscoverPiFromAuthJSON(t *testing.T) {
	home := t.TempDir()
	piDir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0755); err != nil {
		t.Fatal(err)
	}
	data := `{"anthropic":{"type":"api_key","key":"sk-ant-pi"}}`
	if err := os.WriteFile(filepath.Join(piDir, "auth.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	configs, err := Discover(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cfg := range configs {
		if cfg.Name == "pi" && cfg.AccessToken == "sk-ant-pi" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected pi provider from ~/.pi/agent/auth.json")
	}
}

func TestDiscoverCopilotFromKeychain(t *testing.T) {
	home := t.TempDir()
	original := commandOutput
	commandOutput = func(name string, args ...string) (string, error) {
		if name == "security" || name == "secret-tool" {
			return "ghu_from_keychain", nil
		}
		return "", os.ErrNotExist
	}
	defer func() { commandOutput = original }()

	configs, err := Discover(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cfg := range configs {
		if cfg.Name == "copilot" && cfg.AccessToken == "ghu_from_keychain" && strings.Contains(cfg.ConfigPath, "copilot-cli") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected copilot provider discovered from keychain")
	}
}

func TestDiscoverKiroFromSQLite(t *testing.T) {
	home := t.TempDir()
	kiroDB := filepath.Join(home, ".local", "share", "kiro-cli")
	if err := os.MkdirAll(kiroDB, 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(kiroDB, "data.sqlite3")
	if err := os.WriteFile(dbPath, []byte("not-a-real-db"), 0600); err != nil {
		t.Fatal(err)
	}
	original := commandOutput
	commandOutput = func(name string, args ...string) (string, error) {
		if name == "sqlite3" {
			return `{"access_token":"kiro_access_123","refresh_token":"kiro_refresh_456"}`, nil
		}
		return "", os.ErrNotExist
	}
	defer func() { commandOutput = original }()

	configs, err := Discover(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cfg := range configs {
		if cfg.Name == "kiro" && cfg.AccessToken == "kiro_access_123" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected kiro provider discovered from sqlite auth_kv")
	}
}

func TestFormatStatus(t *testing.T) {
	// Test with no configs
	status := FormatStatus([]ProviderConfig{})
	if status != "No agent credentials detected on host" {
		t.Errorf("unexpected status for empty configs: %s", status)
	}

	// Test with configs
	configs := []ProviderConfig{
		{Name: "codex", Type: ProviderTypeOAuth, ConfigPath: "/home/.config/codex/auth.json"},
		{Name: "opencode", Type: ProviderTypeAPIKey, ConfigPath: "/home/.config/opencode/auth.json"},
	}
	status = FormatStatus(configs)
	if status == "" {
		t.Error("expected non-empty status")
	}
	if status == "No agent credentials detected on host" {
		t.Error("expected status to show detected providers")
	}
}
