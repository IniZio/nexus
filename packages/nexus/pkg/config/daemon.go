package config

import "encoding/json"

// AuthConfig holds authentication configuration
type AuthConfig struct {
	// Mode: "personal" (default, current behavior)
	// Future: "pool" for multi-user mode
	Mode string `json:"mode"`

	// Provider configuration (deferred parsing for future extensibility)
	// In personal mode, this is ignored
	// In future pool mode, this configures OIDC/SAML
	Provider json.RawMessage `json:"provider,omitempty"`
}

// DefaultAuthConfig returns auth config for personal mode
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{
		Mode: "personal",
	}
}

type DaemonConfig struct {
	Port        int    `json:"port"`
	DataDir     string `json:"data_dir"`
	TokenSecret string `json:"token_secret,omitempty"`

	// Future-proof auth configuration
	Auth AuthConfig `json:"auth,omitempty"`
}

func DefaultConfig() *DaemonConfig {
	return &DaemonConfig{
		Port:    8080,
		DataDir: "~/.local/share/nexus",
		Auth:    DefaultAuthConfig(),
	}
}
