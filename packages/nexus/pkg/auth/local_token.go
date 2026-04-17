// packages/nexus/pkg/auth/local_token.go

package auth

import (
	"context"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// LocalTokenProvider validates local daemon tokens
type LocalTokenProvider struct {
	tokenSecret string
}

// NewLocalTokenProvider creates a new local token provider
func NewLocalTokenProvider(secret string) *LocalTokenProvider {
	return &LocalTokenProvider{tokenSecret: secret}
}

// ProviderType returns "local"
func (p *LocalTokenProvider) ProviderType() string {
	return "local"
}

// ProviderName returns "local"
func (p *LocalTokenProvider) ProviderName() string {
	return "local"
}

// ValidateToken validates a local token
func (p *LocalTokenProvider) ValidateToken(ctx context.Context, token string) (*Identity, error) {
	// Check direct token match (legacy simple auth)
	if token == p.tokenSecret {
		return p.localIdentity(), nil
	}

	// Check JWT signed with tokenSecret (existing JWT validation)
	if p.validateJWT(token) {
		return p.localIdentity(), nil
	}

	return nil, ErrInvalidToken
}

// validateJWT checks if token is a valid JWT signed with our secret
func (p *LocalTokenProvider) validateJWT(token string) bool {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(p.tokenSecret), nil
	})
	return err == nil && parsed.Valid
}

// localIdentity returns the synthetic local identity
func (p *LocalTokenProvider) localIdentity() *Identity {
	return &Identity{
		Subject:       "local",
		Email:         "local@nexus.local",
		Name:          "Local User",
		HomeDaemon:    "localhost",
		CurrentDaemon: "localhost",
	}
}
