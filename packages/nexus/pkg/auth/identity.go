// packages/nexus/pkg/auth/identity.go

package auth

import (
	"context"
	"fmt"
	"time"
)

// Identity represents an authenticated user across all auth methods
type Identity struct {
	// Core identity fields
	Subject string `json:"sub"`             // Unique user ID
	Email   string `json:"email,omitempty"` // Primary email
	Name    string `json:"name,omitempty"`  // Display name

	// Daemon context
	HomeDaemon    string `json:"home_daemon"`    // Daemon where user "lives"
	CurrentDaemon string `json:"current_daemon"` // Daemon user is connected to

	// Multi-tenancy (future)
	TenantID string `json:"tenant_id,omitempty"`
	OrgName  string `json:"org_name,omitempty"`

	// Token lifecycle (for future token refresh)
	TokenExpiry *time.Time `json:"token_expiry,omitempty"`
}

// IsLocal returns true if this is the synthetic local identity
func (i *Identity) IsLocal() bool {
	return i.Subject == "local"
}

// UserAddress returns the full user@daemon address
func (i *Identity) UserAddress() string {
	return fmt.Sprintf("%s@%s", i.Subject, i.CurrentDaemon)
}

type contextKey struct{}

var identityKey = &contextKey{}

// WithIdentity attaches identity to context
func WithIdentity(ctx context.Context, identity *Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}

// IdentityFromContext extracts identity from context
// Returns synthetic local identity if none found (backward compatible)
func IdentityFromContext(ctx context.Context) *Identity {
	if identity, ok := ctx.Value(identityKey).(*Identity); ok {
		return identity
	}
	// Return synthetic local identity for backward compatibility
	return &Identity{
		Subject: "local",
	}
}
