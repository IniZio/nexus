package vending

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/secrets/discovery"
)

// Token represents a short-lived access token
type Token struct {
	Value     string
	ExpiresAt time.Time
	Provider  string
}

// IsExpired checks if token is expired (with 60s buffer)
func (t *Token) IsExpired() bool {
	return time.Now().After(t.ExpiresAt.Add(-60 * time.Second))
}

// Broker handles token vending for a specific provider
type Broker interface {
	Name() string
	GetToken(ctx context.Context) (*Token, error)
}

// Service provides credential vending for workspaces
type Service struct {
	brokers map[string]Broker // provider -> broker
	mu      sync.RWMutex
}

// NewService creates a vending service from discovered provider configs
func NewService(configs []discovery.ProviderConfig) *Service {
	s := &Service{
		brokers: make(map[string]Broker),
	}

	for _, cfg := range configs {
		broker := createBroker(cfg)
		if broker != nil {
			s.brokers[cfg.Name] = broker
		}
	}

	return s
}

// GetToken returns a short-lived token for the specified provider
func (s *Service) GetToken(ctx context.Context, provider string) (*Token, error) {
	s.mu.RLock()
	broker, ok := s.brokers[provider]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("provider %s not configured", provider)
	}

	return broker.GetToken(ctx)
}

// ListProviders returns all available providers
func (s *Service) ListProviders() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	providers := make([]string, 0, len(s.brokers))
	for name := range s.brokers {
		providers = append(providers, name)
	}
	return providers
}

// createBroker creates the appropriate broker for a provider config
func createBroker(cfg discovery.ProviderConfig) Broker {
	switch cfg.Type {
	case discovery.ProviderTypeAPIKey:
		return &staticBroker{
			provider: cfg.Name,
			token: &Token{
				Value:     cfg.AccessToken,
				ExpiresAt: time.Now().Add(24 * time.Hour), // Long-lived API keys
				Provider:  cfg.Name,
			},
		}

	case discovery.ProviderTypeSession:
		return &staticBroker{
			provider: cfg.Name,
			token: &Token{
				Value:     cfg.AccessToken,
				ExpiresAt: time.Now().Add(1 * time.Hour), // Session tokens
				Provider:  cfg.Name,
			},
		}

	case discovery.ProviderTypeOAuth:
		// For minimal prototype, use static broker
		// Full implementation would handle refresh
		return &staticBroker{
			provider: cfg.Name,
			token: &Token{
				Value:     cfg.AccessToken, // Use existing access token
				ExpiresAt: cfg.ExpiresAt,
				Provider:  cfg.Name,
			},
		}

	default:
		return nil
	}
}

// staticBroker returns a pre-configured token (for API keys and simple cases)
type staticBroker struct {
	provider string
	token    *Token
}

func (b *staticBroker) Name() string {
	return b.provider
}

func (b *staticBroker) GetToken(ctx context.Context) (*Token, error) {
	if b.token.IsExpired() {
		return nil, fmt.Errorf("token expired for %s", b.provider)
	}
	return b.token, nil
}

// RefreshableBroker handles OAuth refresh (placeholder for full implementation)
type RefreshableBroker struct {
	provider     string
	refreshToken string
	currentToken *Token
	mu           sync.RWMutex
}

func (b *RefreshableBroker) Name() string {
	return b.provider
}

func (b *RefreshableBroker) GetToken(ctx context.Context) (*Token, error) {
	b.mu.RLock()
	token := b.currentToken
	b.mu.RUnlock()

	if token != nil && !token.IsExpired() {
		return token, nil
	}

	// Need refresh - placeholder
	return nil, fmt.Errorf("token refresh not implemented in prototype")
}
