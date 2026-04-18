package vending

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/secrets/discovery"
)

// HostVendingService is a singleton that manages credential vending for all workspaces.
// It supports multi-tenant isolation (userID) for future multi-user deployments.
type HostVendingService struct {
	// Global config from host (populated once at startup)
	globalConfigs []discovery.ProviderConfig

	// Per-workspace token caches (workspaceID -> provider -> token)
	workspaceTokens map[string]map[string]*Token
	mu              sync.RWMutex

	// For future multi-tenant: userID -> configs
	userConfigs map[string][]discovery.ProviderConfig
}

var (
	hostInstance *HostVendingService
	hostOnce     sync.Once
	hostInitErr  error
)

// GetHostVendingService returns the singleton host vending service.
// Initializes once from host home directory on first call.
func GetHostVendingService() (*HostVendingService, error) {
	hostOnce.Do(func() {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			hostInitErr = fmt.Errorf("failed to get home dir: %w", err)
			return
		}

		configs, err := discovery.Discover(homeDir)
		if err != nil {
			hostInitErr = fmt.Errorf("credential discovery failed: %w", err)
			return
		}

		hostInstance = &HostVendingService{
			globalConfigs:   configs,
			workspaceTokens: make(map[string]map[string]*Token),
			userConfigs:     make(map[string][]discovery.ProviderConfig),
		}
	})

	return hostInstance, hostInitErr
}

// GetToken returns a token for a workspace and provider.
// For now, uses global configs. Future: will use user-scoped configs.
func (h *HostVendingService) GetToken(ctx context.Context, workspaceID, userID, provider string) (*Token, error) {
	// Check workspace token cache first
	h.mu.RLock()
	wsTokens, ok := h.workspaceTokens[workspaceID]
	if ok {
		token, exists := wsTokens[provider]
		if exists && !token.IsExpired() {
			h.mu.RUnlock()
			return token, nil
		}
	}
	h.mu.RUnlock()

	// Not cached or expired - get from global config
	// Future: use userID to select user-specific configs
	var config *discovery.ProviderConfig
	for _, cfg := range h.globalConfigs {
		if cfg.Name == provider {
			config = &cfg
			break
		}
	}

	if config == nil {
		return nil, fmt.Errorf("provider %s not configured for host", provider)
	}

	// Create token from config
	token, err := h.createTokenFromConfig(config)
	if err != nil {
		return nil, err
	}

	// Cache it for this workspace
	h.mu.Lock()
	if h.workspaceTokens[workspaceID] == nil {
		h.workspaceTokens[workspaceID] = make(map[string]*Token)
	}
	h.workspaceTokens[workspaceID][provider] = token
	h.mu.Unlock()

	return token, nil
}

// createTokenFromConfig creates a token from provider config
func (h *HostVendingService) createTokenFromConfig(cfg *discovery.ProviderConfig) (*Token, error) {
	switch cfg.Type {
	case discovery.ProviderTypeAPIKey:
		return &Token{
			Value:     cfg.AccessToken,
			ExpiresAt: time.Now().Add(24 * time.Hour),
			Provider:  cfg.Name,
		}, nil

	case discovery.ProviderTypeSession:
		return &Token{
			Value:     cfg.AccessToken,
			ExpiresAt: time.Now().Add(1 * time.Hour),
			Provider:  cfg.Name,
		}, nil

	case discovery.ProviderTypeOAuth:
		// Use existing access token (short TTL)
		expiresAt := cfg.ExpiresAt
		if expiresAt.IsZero() || time.Until(expiresAt) > 15*time.Minute {
			// If no expiry or >15min, cap at 15min for safety
			expiresAt = time.Now().Add(15 * time.Minute)
		}
		return &Token{
			Value:     cfg.AccessToken,
			ExpiresAt: expiresAt,
			Provider:  cfg.Name,
		}, nil

	default:
		return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
	}
}

// ListProviders returns available providers for this host/user
func (h *HostVendingService) ListProviders(userID string) []string {
	// Future: use userID to get user-specific providers
	// For now, return global providers

	providers := make([]string, 0, len(h.globalConfigs))
	for _, cfg := range h.globalConfigs {
		providers = append(providers, cfg.Name)
	}
	return providers
}

// CleanupWorkspace removes cached tokens for a workspace (call on workspace stop)
func (h *HostVendingService) CleanupWorkspace(workspaceID string) {
	h.mu.Lock()
	delete(h.workspaceTokens, workspaceID)
	h.mu.Unlock()
}

// RegisterUserConfigs (for future multi-tenant support)
// Allows registering per-user credentials at runtime
func (h *HostVendingService) RegisterUserConfigs(userID string, configs []discovery.ProviderConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.userConfigs[userID] = configs
}
