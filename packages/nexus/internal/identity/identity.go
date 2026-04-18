package identity

import (
	"context"
	"fmt"
	"time"
)

// Identity represents an authenticated principal connected to the daemon.
type Identity struct {
	Subject       string     `json:"sub"`
	Email         string     `json:"email,omitempty"`
	Name          string     `json:"name,omitempty"`
	HomeDaemon    string     `json:"home_daemon"`
	CurrentDaemon string     `json:"current_daemon"`
	TenantID      string     `json:"tenant_id,omitempty"`
	OrgName       string     `json:"org_name,omitempty"`
	TokenExpiry   *time.Time `json:"token_expiry,omitempty"`
}

// IsLocal reports whether this is the synthetic local identity.
func (i *Identity) IsLocal() bool {
	return i.Subject == "local"
}

// UserAddress returns the full user@daemon address.
func (i *Identity) UserAddress() string {
	return fmt.Sprintf("%s@%s", i.Subject, i.CurrentDaemon)
}

type contextKey struct{}

var key = &contextKey{}

// WithIdentity attaches identity to ctx.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, key, id)
}

// FromContext extracts identity from ctx.
// Returns the synthetic local identity if none is present.
func FromContext(ctx context.Context) *Identity {
	if id, ok := ctx.Value(key).(*Identity); ok {
		return id
	}
	return &Identity{Subject: "local"}
}

// Local returns the synthetic local identity for a given daemon address.
func Local(daemonAddr string) *Identity {
	return &Identity{
		Subject:       "local",
		Email:         "local@nexus.local",
		Name:          "Local User",
		HomeDaemon:    daemonAddr,
		CurrentDaemon: daemonAddr,
	}
}
