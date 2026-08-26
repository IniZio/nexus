package service

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MCPOAuthBind pairs one OAuth MCP server with its host-side SecretBind so the
// MITM proxy can inject Authorization: Bearer <token> for that server's host.
// The Bind.Env name is produced by syntheticMCPVar(ServerName, "Authorization"),
// matching the same naming convention used by sanitizeHTTPEntry for literal
// credential headers.
type MCPOAuthBind struct {
	// ServerName is the logical MCP server name (e.g. "linear-server").
	ServerName string
	// ServerURL is the full MCP endpoint URL from the mcpOAuth entry
	// (e.g. "https://mcp.linear.app/mcp"), including any path. It is used to
	// synthesize the guest config entry when the server is absent from the
	// top-level mcpServers map; reconstructing from host alone would drop the
	// path and break servers whose endpoint is not the host root.
	ServerURL string
	// Bind carries the synthetic env-var name, the target host, and the real
	// bearer token (host-side only; never written to the guest).
	Bind SecretBind
	// Header is always "Authorization".
	Header string
}

// MCPOAuthVarName returns the synthetic guest env-var name for the given MCP
// server and header name. It matches the naming convention in sanitizeHTTPEntry.
// Example: MCPOAuthVarName("linear-server", "Authorization") →
// "NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION".
func MCPOAuthVarName(serverName, headerName string) string {
	return syntheticMCPVar(serverName, headerName)
}

// MCPOAuthRefreshConfig carries the token material needed for a later slice to
// construct a DedicatedCredStore and spin up a host-side token refresher.
type MCPOAuthRefreshConfig struct {
	ServerName    string
	Host          string
	AccessToken   string
	RefreshToken  string
	TokenEndpoint string
	ClientID      string
	ExpiresAtMs   int64
}

// discoverTokenEndpoint resolves the token endpoint for an OAuth authorization
// server by fetching its RFC 8414 metadata document. The default implementation
// performs a real HTTP GET; tests override it to avoid network calls.
var discoverTokenEndpoint = func(authServerURL string) (string, error) {
	metaURL := strings.TrimRight(authServerURL, "/") + "/.well-known/oauth-authorization-server"
	resp, err := http.Get(metaURL) //nolint:noctx
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var meta struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return "", err
	}
	if meta.TokenEndpoint == "" {
		return "", io.ErrUnexpectedEOF
	}
	return meta.TokenEndpoint, nil
}

// rawCredentials is the on-disk shape of ~/.claude/.credentials.json that
// contains the mcpOAuth map.
type rawCredentials struct {
	MCPOAuth map[string]rawMCPOAuthEntry `json:"mcpOAuth"`
}

// rawMCPOAuthEntry is one entry in the mcpOAuth map.
type rawMCPOAuthEntry struct {
	ServerName    string `json:"serverName"`
	ServerURL     string `json:"serverUrl"`
	AccessToken   string `json:"accessToken"`
	RefreshToken  string `json:"refreshToken"`
	ExpiresAt     int64  `json:"expiresAt"` // ms since epoch
	Scope         string `json:"scope"`
	ClientID      string `json:"clientId"`
	DiscoveryState struct {
		AuthorizationServerURL string `json:"authorizationServerUrl"`
		OAuthMetadataFound     bool   `json:"oauthMetadataFound"`
	} `json:"discoveryState"`
}

// BuildMCPOAuthBinds reads the mcpOAuth map from credPath (defaulting to
// ~/.claude/.credentials.json when empty) and returns one MCPOAuthBind and
// one MCPOAuthRefreshConfig per OAuth MCP server.
//
// A missing file or absent/empty mcpOAuth map is non-fatal: nil, nil, nil is
// returned. Parse errors for individual entries are logged and skipped.
func BuildMCPOAuthBinds(credPath string) (binds []MCPOAuthBind, refresh []MCPOAuthRefreshConfig, err error) {
	if credPath == "" {
		credPath = filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json")
	}

	data, err := os.ReadFile(credPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var raw rawCredentials
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Warn("mcpoauth: failed to parse credentials.json", "path", credPath, "err", err)
		return nil, nil, nil
	}
	if len(raw.MCPOAuth) == 0 {
		return nil, nil, nil
	}

	// Sort entry keys for deterministic output.
	keys := make([]string, 0, len(raw.MCPOAuth))
	for k := range raw.MCPOAuth {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		e := raw.MCPOAuth[key]
		if e.ServerName == "" {
			slog.Warn("mcpoauth: entry missing serverName, skipping", "key", key)
			continue
		}
		if e.AccessToken == "" {
			slog.Warn("mcpoauth: entry missing accessToken, skipping", "serverName", e.ServerName)
			continue
		}

		host := hostFromURL(e.ServerURL)
		if host == "" {
			slog.Warn("mcpoauth: entry has unparseable serverUrl, skipping", "serverName", e.ServerName, "serverUrl", e.ServerURL)
			continue
		}

		// Env var name matches sanitizeHTTPEntry's syntheticMCPVar for Authorization.
		envVar := syntheticMCPVar(e.ServerName, "Authorization")

		binds = append(binds, MCPOAuthBind{
			ServerName: e.ServerName,
			ServerURL:  e.ServerURL,
			Bind: SecretBind{
				Env:   envVar,
				Hosts: []string{host},
				Token: "Bearer " + e.AccessToken,
			},
			Header: "Authorization",
		})

		// Resolve token endpoint via discovery; fall back to <authServerUrl>/token.
		authServerURL := e.DiscoveryState.AuthorizationServerURL
		tokenEndpoint := ""
		if authServerURL != "" {
			te, discErr := discoverTokenEndpoint(authServerURL)
			if discErr != nil {
				slog.Warn("mcpoauth: token endpoint discovery failed, using fallback",
					"serverName", e.ServerName, "authServerUrl", authServerURL, "err", discErr)
				tokenEndpoint = strings.TrimRight(authServerURL, "/") + "/token"
			} else {
				tokenEndpoint = te
			}
		}

		refresh = append(refresh, MCPOAuthRefreshConfig{
			ServerName:    e.ServerName,
			Host:          host,
			AccessToken:   e.AccessToken,
			RefreshToken:  e.RefreshToken,
			TokenEndpoint: tokenEndpoint,
			ClientID:      e.ClientID,
			ExpiresAtMs:   e.ExpiresAt,
		})
	}

	return binds, refresh, nil
}
