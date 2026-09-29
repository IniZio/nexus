package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
)

// mcpIntegrationID maps a server name and URL to its vault integration identifier.
// Returns "linear" for mcp.linear.app and api.linear.app, else the server name.
func mcpIntegrationID(serverName, serverURL string) string {
	host := hostFromURL(serverURL)
	if host == "mcp.linear.app" || host == "api.linear.app" {
		return "linear"
	}
	return serverName
}

// readRawMCPOAuthEntries parses credentials.json and returns entries sorted by
// key. Returns nil when the file is missing or the mcpOAuth map is empty.
func readRawMCPOAuthEntries(credPath string) ([]rawMCPOAuthEntry, error) {
	if credPath == "" {
		credPath = filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json")
	}
	data, err := os.ReadFile(credPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var raw rawCredentials
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Warn("mcpoauth: failed to parse credentials.json", "path", credPath, "err", err)
		return nil, nil
	}
	if len(raw.MCPOAuth) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(raw.MCPOAuth))
	for k := range raw.MCPOAuth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]rawMCPOAuthEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, raw.MCPOAuth[k])
	}
	return out, nil
}

// ImportMCPOAuthIntoVault reads the mcpOAuth map from credPath (defaulting to
// ~/.claude/.credentials.json when empty) and imports each entry into v as a
// vault record under the given principal. Existing records are never overwritten
// (one-shot import). Entries missing accessToken or a parseable URL are skipped.
func ImportMCPOAuthIntoVault(ctx context.Context, v vault.Vault, principal, credPath string) error {
	entries, err := readRawMCPOAuthEntries(credPath)
	if err != nil || entries == nil {
		return err
	}
	for _, e := range entries {
		if e.AccessToken == "" || hostFromURL(e.ServerURL) == "" {
			continue
		}
		intID := mcpIntegrationID(e.ServerName, e.ServerURL)
		key := vault.Key{Principal: principal, Integration: intID}

		_, getErr := v.Get(ctx, key)
		if getErr == nil {
			continue
		}
		if !errors.Is(getErr, vault.ErrUnlinked) {
			return getErr
		}
		if err := v.Put(ctx, key, vault.Record{
			AccessToken:     e.AccessToken,
			RefreshToken:    e.RefreshToken,
			Expiry:          time.UnixMilli(e.ExpiresAt),
			AllowedProjects: []string{"*"},
		}); err != nil {
			return err
		}
	}
	return nil
}

// BuildMCPOAuthBindsFromVault reads the server list from credPath and resolves
// each bearer token via vault.Source. Returns ErrUnlinked when the principal is
// not linked for a required integration (fail closed).
func BuildMCPOAuthBindsFromVault(ctx context.Context, v vault.Vault, principal, project, credPath string) ([]MCPOAuthBind, error) {
	entries, err := readRawMCPOAuthEntries(credPath)
	if err != nil || entries == nil {
		return nil, err
	}
	var binds []MCPOAuthBind
	for _, e := range entries {
		host := hostFromURL(e.ServerURL)
		if host == "" {
			continue
		}
		intID := mcpIntegrationID(e.ServerName, e.ServerURL)
		key := vault.Key{Principal: principal, Integration: intID}

		src, srcErr := v.Source(key, project)
		if srcErr != nil {
			return nil, srcErr
		}
		tok, _, tokErr := src.Token(ctx)
		if tokErr != nil {
			return nil, tokErr
		}
		if tok == "" {
			continue
		}
		bearerTok := tok
		if !strings.HasPrefix(strings.ToLower(bearerTok), "bearer ") {
			bearerTok = "Bearer " + bearerTok
		}
		binds = append(binds, MCPOAuthBind{
			ServerName: e.ServerName,
			ServerURL:  e.ServerURL,
			Bind: SecretBind{
				Env:   syntheticMCPVar(e.ServerName, "Authorization"),
				Hosts: []string{host},
				Token: bearerTok,
			},
			Header: "Authorization",
		})
	}
	return binds, nil
}
