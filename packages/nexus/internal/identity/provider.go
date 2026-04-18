package identity

import (
	"context"
	"errors"
)

var (
	ErrInvalidToken = errors.New("invalid authentication token")
	ErrTokenExpired = errors.New("authentication token expired")
	ErrAccessDenied = errors.New("access denied")
)

// Provider validates tokens and returns caller identity.
type Provider interface {
	ValidateToken(ctx context.Context, token string) (*Identity, error)
	ProviderType() string
	ProviderName() string
}

// ProviderRegistry maps provider names to Provider implementations.
type ProviderRegistry struct {
	providers map[string]Provider
}

// NewProviderRegistry returns an empty ProviderRegistry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[string]Provider)}
}

// Register adds p under name.
func (r *ProviderRegistry) Register(name string, p Provider) {
	r.providers[name] = p
}

// Get returns the provider registered under name.
func (r *ProviderRegistry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// GetDefault returns the "local" provider.
func (r *ProviderRegistry) GetDefault() (Provider, bool) {
	return r.Get("local")
}
