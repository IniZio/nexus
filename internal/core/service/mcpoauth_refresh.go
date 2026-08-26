package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// safeNameRe matches characters that are safe for filesystem paths in a
// per-server store filename. Everything else is replaced with an underscore.
var safeNameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// sanitizeForFS replaces characters unsafe for filesystem use with underscores.
// Returns "_" if the result would be empty.
func sanitizeForFS(name string) string {
	s := safeNameRe.ReplaceAllString(name, "_")
	if s == "" {
		return "_"
	}
	return s
}

// StartMCPOAuthRefreshers creates a DedicatedCredStore and a cred.Refresher for
// each MCPOAuthRefreshConfig, persisting each store at
// <storeRoot>/<serverName>.json. Non-fatal per-server: a config that cannot be
// initialised (missing access/refresh token, token endpoint, or a SaveStore
// failure) is logged and skipped; the remaining configs proceed.
//
// Registration and background goroutines are the caller's responsibility:
// the supervisor mirrors the Anthropic refresher pattern (step 5b: r.Register;
// step 5c: 1-minute ticker calling r.Token(ctx), stopped by ctx.Done()).
//
// storeRoot is typically DefaultMCPOAuthStoreRoot(). On success the returned
// slice contains only the Refreshers that were fully initialised.
func StartMCPOAuthRefreshers(
	_ context.Context,
	broker *cred.Broker,
	storeRoot string,
	configs []MCPOAuthRefreshConfig,
) ([]*cred.Refresher, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(storeRoot, 0o700); err != nil {
		return nil, fmt.Errorf("mcpoauth_refresh: mkdir %s: %w", storeRoot, err)
	}

	var refreshers []*cred.Refresher
	for _, cfg := range configs {
		r, err := buildMCPOAuthRefresher(cfg, broker, storeRoot)
		if err != nil {
			slog.Warn("mcpoauth_refresh: skipping server", "server", cfg.ServerName, "err", err)
			continue
		}
		refreshers = append(refreshers, r)
		slog.Info("mcpoauth_refresh: refresher ready", "server", cfg.ServerName, "host", cfg.Host)
	}
	return refreshers, nil
}

// buildMCPOAuthRefresher constructs a single cred.Refresher for cfg. It writes
// (or overwrites) the DedicatedCredStore at storePath, then calls NewRefresher.
// Returns a descriptive error when the config is unusable.
func buildMCPOAuthRefresher(cfg MCPOAuthRefreshConfig, broker *cred.Broker, storeRoot string) (*cred.Refresher, error) {
	if cfg.AccessToken == "" {
		return nil, fmt.Errorf("missing access_token")
	}
	if cfg.RefreshToken == "" {
		return nil, fmt.Errorf("missing refresh_token (unattended refresh requires one)")
	}
	if cfg.TokenEndpoint == "" {
		return nil, fmt.Errorf("missing token_endpoint")
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("missing host")
	}

	storePath := filepath.Join(storeRoot, sanitizeForFS(cfg.ServerName)+".json")
	store := &cred.DedicatedCredStore{
		AccessToken:   cfg.AccessToken,
		RefreshToken:  cfg.RefreshToken,
		ExpiresAt:     time.UnixMilli(cfg.ExpiresAtMs),
		TokenType:     "Bearer",
		ClientID:      cfg.ClientID,
		ClientSecret:  "", // public client; secret is empty
		TokenEndpoint: cfg.TokenEndpoint,
	}
	if err := cred.SaveStore(storePath, store); err != nil {
		return nil, fmt.Errorf("save store %s: %w", storePath, err)
	}

	r, err := cred.NewRefresher(storePath, cfg.Host, broker)
	if err != nil {
		return nil, fmt.Errorf("new refresher: %w", err)
	}
	return r, nil
}

// DefaultMCPOAuthStoreRoot returns the default host-side directory where
// per-server MCP OAuth credential stores are persisted.
// Typically ~/.config/nexus3/mcp-creds/.
func DefaultMCPOAuthStoreRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "nexus3", "mcp-creds")
}
