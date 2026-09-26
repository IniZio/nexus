package vault

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AppConfig holds per-connector OAuth client app overrides read from the nexus
// config directory. Missing fields fall back to connector defaults except for
// integrations that have no built-in default (Linear requires a client_id).
type AppConfig struct {
	GitHub struct {
		ClientID string `json:"client_id"`
	} `json:"github"`
	Linear struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	} `json:"linear"`
}

// LoadAppConfig reads vault-app.json from configDir.
// An absent file returns a zero AppConfig (use connector defaults).
func LoadAppConfig(configDir string) (AppConfig, error) {
	path := filepath.Join(configDir, "vault-app.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AppConfig{}, nil
		}
		return AppConfig{}, fmt.Errorf("vault: read app config: %w", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AppConfig{}, fmt.Errorf("vault: parse app config %s: %w", path, err)
	}
	return cfg, nil
}

// LinearClientID returns the Linear client_id or an error if not configured.
func (c AppConfig) LinearClientID() (string, error) {
	if c.Linear.ClientID == "" {
		return "", fmt.Errorf("vault: linear client_id is required in vault-app.json (no built-in default)")
	}
	return c.Linear.ClientID, nil
}
