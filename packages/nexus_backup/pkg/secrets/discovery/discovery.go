package discovery

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ProviderType indicates the authentication mechanism
type ProviderType string

const (
	ProviderTypeOAuth       ProviderType = "oauth"
	ProviderTypeAPIKey      ProviderType = "api_key"
	ProviderTypeSession     ProviderType = "session"
)

// ProviderConfig represents a detected OAuth/API credential from host
type ProviderConfig struct {
	Name         string       // "codex", "opencode", "claude"
	Type         ProviderType // oauth, api_key, session
	RefreshToken string       // For OAuth
	AccessToken  string       // Short-lived or API key
	ExpiresAt    time.Time
	Scopes       []string
	ConfigPath   string // Where it was found on host
}

var commandOutput = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Discover scans host home directory for agent OAuth/API configurations
func Discover(homeDir string) ([]ProviderConfig, error) {
	var configs []ProviderConfig

	// Check each provider
	detectors := []func(string) (*ProviderConfig, error){
		detectCodex,
		detectOpenCode,
		detectClaude,
		detectOpenAI,
		detectGitHubCLI,
		detectGeminiCLI,
		detectContinueCLI,
		detectPiAgent,
		detectGooseCLI,
		detectAider,
		detectKiroCLI,
	}

	for _, detector := range detectors {
		if cfg, err := detector(homeDir); err == nil && cfg != nil {
			configs = append(configs, *cfg)
		}
	}

	return configs, nil
}

func detectCodex(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".config", "codex", "auth.json"),
		filepath.Join(home, ".codex", "auth.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var auth struct {
			RefreshToken string    `json:"refresh_token"`
			AccessToken  string    `json:"access_token"`
			ExpiresAt    time.Time `json:"expires_at"`
			Account      string    `json:"account"`
		}

		if err := json.Unmarshal(data, &auth); err != nil {
			continue
		}

		if auth.RefreshToken == "" {
			continue
		}

		return &ProviderConfig{
			Name:         "codex",
			Type:         ProviderTypeOAuth,
			RefreshToken: auth.RefreshToken,
			AccessToken:  auth.AccessToken,
			ExpiresAt:    auth.ExpiresAt,
			Scopes:       []string{"repo", "read:org"},
			ConfigPath:   path,
		}, nil
	}

	return nil, nil
}

func detectOpenCode(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".config", "opencode", "auth.json"),
		filepath.Join(home, ".opencode", "auth.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var auth struct {
			APIKey      string `json:"api_key"`
			AccessToken string `json:"access_token"`
			Provider    string `json:"provider"`
		}

		if err := json.Unmarshal(data, &auth); err != nil {
			continue
		}

		// OpenCode can use either API key or OAuth token
		if auth.APIKey != "" {
			return &ProviderConfig{
				Name:       "opencode",
				Type:       ProviderTypeAPIKey,
				AccessToken: auth.APIKey,
				ConfigPath: path,
			}, nil
		}

		if auth.AccessToken != "" {
			return &ProviderConfig{
				Name:        "opencode",
				Type:        ProviderTypeSession,
				AccessToken: auth.AccessToken,
				ConfigPath:  path,
			}, nil
		}
	}

	return nil, nil
}

func detectClaude(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".config", "claude", "settings.json"),
		filepath.Join(home, ".claude", "auth.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var auth struct {
			SessionToken string `json:"sessionToken"`
			APIKey       string `json:"apiKey"`
		}

		if err := json.Unmarshal(data, &auth); err != nil {
			continue
		}

		if auth.SessionToken != "" {
			return &ProviderConfig{
				Name:        "claude",
				Type:        ProviderTypeSession,
				AccessToken: auth.SessionToken,
				ConfigPath:  path,
			}, nil
		}
	}

	return nil, nil
}

func detectOpenAI(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".config", "openai", "auth.json"),
		filepath.Join(home, ".openai", "api_key"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		// Try JSON first
		var auth struct {
			APIKey string `json:"api_key"`
		}
		if err := json.Unmarshal(data, &auth); err == nil && auth.APIKey != "" {
			return &ProviderConfig{
				Name:        "openai",
				Type:        ProviderTypeAPIKey,
				AccessToken: auth.APIKey,
				ConfigPath:  path,
			}, nil
		}

		// Try plain text
		if len(data) > 0 && len(data) < 256 {
			return &ProviderConfig{
				Name:        "openai",
				Type:        ProviderTypeAPIKey,
				AccessToken: string(data),
				ConfigPath:  path,
			}, nil
		}
	}

	return nil, nil
}

func detectGitHubCLI(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".copilot", "config.json"),
		filepath.Join(home, ".config", "github-copilot", "hosts.json"),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		token := readJSONToken(data, []string{"oauth_token", "access_token", "token", "github_token"})
		if token == "" {
			continue
		}
		return &ProviderConfig{
			Name:        "copilot",
			Type:        ProviderTypeSession,
			AccessToken: token,
			ConfigPath:  path,
		}, nil
	}
	if token, source, ok := readCopilotTokenFromKeychain(); ok {
		return &ProviderConfig{
			Name:        "copilot",
			Type:        ProviderTypeSession,
			AccessToken: token,
			ConfigPath:  source,
		}, nil
	}
	return nil, nil
}

func detectGeminiCLI(home string) (*ProviderConfig, error) {
	envPath := filepath.Join(home, ".gemini", ".env")
	if token, ok := readEnvFileValue(envPath, "GEMINI_API_KEY"); ok {
		return &ProviderConfig{
			Name:        "gemini",
			Type:        ProviderTypeAPIKey,
			AccessToken: token,
			ConfigPath:  envPath,
		}, nil
	}
	return nil, nil
}

func detectContinueCLI(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".continue", ".env"),
		filepath.Join(home, ".continue", "config.yaml"),
	}
	for _, path := range paths {
		if token, ok := readEnvFileValue(path, "CONTINUE_API_KEY"); ok {
			return &ProviderConfig{
				Name:        "continue",
				Type:        ProviderTypeAPIKey,
				AccessToken: token,
				ConfigPath:  path,
			}, nil
		}
	}
	return nil, nil
}

func detectPiAgent(home string) (*ProviderConfig, error) {
	path := filepath.Join(home, ".pi", "agent", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	token := readJSONToken(data, []string{"access_token", "api_key", "key", "token"})
	if token == "" {
		return nil, nil
	}
	return &ProviderConfig{
		Name:        "pi",
		Type:        ProviderTypeSession,
		AccessToken: token,
		ConfigPath:  path,
	}, nil
}

func detectGooseCLI(home string) (*ProviderConfig, error) {
	path := filepath.Join(home, ".config", "goose", "secrets.yaml")
	if token, ok := readEnvFileValue(path, "OPENAI_API_KEY"); ok {
		return &ProviderConfig{
			Name:        "goose",
			Type:        ProviderTypeAPIKey,
			AccessToken: token,
			ConfigPath:  path,
		}, nil
	}
	if token, ok := readEnvFileValue(path, "ANTHROPIC_API_KEY"); ok {
		return &ProviderConfig{
			Name:        "goose",
			Type:        ProviderTypeAPIKey,
			AccessToken: token,
			ConfigPath:  path,
		}, nil
	}
	return nil, nil
}

func detectAider(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".aider.conf.yml"),
		filepath.Join(home, ".env"),
	}
	for _, path := range paths {
		if token, ok := readEnvFileValue(path, "OPENAI_API_KEY"); ok {
			return &ProviderConfig{
				Name:        "aider",
				Type:        ProviderTypeAPIKey,
				AccessToken: token,
				ConfigPath:  path,
			}, nil
		}
		if token, ok := readEnvFileValue(path, "ANTHROPIC_API_KEY"); ok {
			return &ProviderConfig{
				Name:        "aider",
				Type:        ProviderTypeAPIKey,
				AccessToken: token,
				ConfigPath:  path,
			}, nil
		}
	}
	return nil, nil
}

func detectKiroCLI(home string) (*ProviderConfig, error) {
	paths := []string{
		filepath.Join(home, ".kiro", "settings", "cli.json"),
		filepath.Join(home, "Library", "Application Support", "kiro-cli", "data.sqlite3"),
		filepath.Join(home, ".local", "share", "kiro-cli", "data.sqlite3"),
	}
	for _, path := range paths {
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}
		if stat.IsDir() {
			continue
		}
		if strings.HasSuffix(path, "data.sqlite3") {
			token, ok := readKiroTokenFromSQLite(path)
			if !ok {
				continue
			}
			return &ProviderConfig{
				Name:        "kiro",
				Type:        ProviderTypeSession,
				AccessToken: token,
				ConfigPath:  path,
			}, nil
		}
		return &ProviderConfig{
			Name:       "kiro",
			Type:       ProviderTypeSession,
			ConfigPath: path,
		}, nil
	}
	return nil, nil
}

func readCopilotTokenFromKeychain() (string, string, bool) {
	switch runtime.GOOS {
	case "darwin":
		token, err := commandOutput("security", "find-generic-password", "-s", "copilot-cli", "-w")
		if err == nil && token != "" {
			return token, "keychain:copilot-cli", true
		}
	case "linux":
		token, err := commandOutput("secret-tool", "lookup", "service", "copilot-cli")
		if err == nil && token != "" {
			return token, "keyring:copilot-cli", true
		}
	}
	return "", "", false
}

func readKiroTokenFromSQLite(path string) (string, bool) {
	query := "SELECT value FROM auth_kv WHERE key='kirocli:odic:token' LIMIT 1;"
	row, err := commandOutput("sqlite3", path, query)
	if err != nil || strings.TrimSpace(row) == "" {
		return "", false
	}
	token := readJSONToken([]byte(row), []string{"access_token", "token"})
	if token == "" {
		return "", false
	}
	return token, true
}

func readJSONToken(data []byte, keys []string) string {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	return lookupTokenInMap(obj, keys)
}

func lookupTokenInMap(input map[string]any, keys []string) string {
	for _, key := range keys {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	for _, v := range input {
		if nested, ok := v.(map[string]any); ok {
			if token := lookupTokenInMap(nested, keys); token != "" {
				return token
			}
		}
	}
	return ""
}

func readEnvFileValue(path, key string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	prefix := key + "="
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		value = strings.Trim(value, `"'`)
		if value == "" {
			return "", false
		}
		return value, true
	}
	return "", false
}

// FormatStatus returns human-readable discovery status
func FormatStatus(configs []ProviderConfig) string {
	if len(configs) == 0 {
		return "No agent credentials detected on host"
	}

	result := fmt.Sprintf("Detected %d provider(s):\n", len(configs))
	for _, cfg := range configs {
		result += fmt.Sprintf("  - %s (%s) from %s\n", cfg.Name, cfg.Type, cfg.ConfigPath)
	}
	return result
}
