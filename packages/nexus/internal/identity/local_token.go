package identity

import (
	"context"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// LocalTokenProvider validates tokens issued by the local daemon.
type LocalTokenProvider struct {
	secret     string
	daemonAddr string
}

// NewLocalTokenProvider returns a provider that accepts secret-matched tokens
// and JWTs signed with secret.
func NewLocalTokenProvider(secret, daemonAddr string) *LocalTokenProvider {
	return &LocalTokenProvider{secret: secret, daemonAddr: daemonAddr}
}

// ProviderType returns "local".
func (p *LocalTokenProvider) ProviderType() string { return "local" }

// ProviderName returns "local".
func (p *LocalTokenProvider) ProviderName() string { return "local" }

// ValidateToken validates a local daemon token and returns the local identity.
func (p *LocalTokenProvider) ValidateToken(_ context.Context, token string) (*Identity, error) {
	if token == p.secret {
		return Local(p.daemonAddr), nil
	}
	if p.validateJWT(token) {
		return Local(p.daemonAddr), nil
	}
	return nil, ErrInvalidToken
}

func (p *LocalTokenProvider) validateJWT(token string) bool {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(p.secret), nil
	})
	return err == nil && parsed.Valid
}
