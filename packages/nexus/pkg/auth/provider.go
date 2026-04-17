// packages/nexus/pkg/auth/provider.go

package auth

import "context"

// Provider validates tokens and returns user identity
type Provider interface {
	// ValidateToken validates a bearer token and returns identity
	// Returns error if token is invalid or expired
	ValidateToken(ctx context.Context, token string) (*Identity, error)

	// ProviderType returns the type of auth ("local")
	ProviderType() string

	// ProviderName returns the specific provider ("local")
	ProviderName() string
}

// ProviderRegistry manages available auth providers
type ProviderRegistry struct {
	providers map[string]Provider
}

// NewProviderRegistry creates a new registry
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry
func (r *ProviderRegistry) Register(name string, provider Provider) {
	r.providers[name] = provider
}

// Get retrieves a provider by name
func (r *ProviderRegistry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// GetDefault returns the default provider (local for now)
func (r *ProviderRegistry) GetDefault() (Provider, bool) {
	return r.Get("local")
}
