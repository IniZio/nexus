package vault

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

// ErrUnlinked is returned when no credential record exists for the requested key.
var ErrUnlinked = errors.New("vault: principal not linked")

// ErrProjectNotAllowed is returned when the project is not in AllowedProjects.
var ErrProjectNotAllowed = errors.New("vault: project not allowed")

// Key identifies a credential record by principal and integration.
type Key struct {
	Principal   string
	Integration string
}

// Record holds the OAuth credential material for a single (principal, integration) pair.
type Record struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	Scopes       []string
	// AllowedProjects lists project identifiers this record may be used for; "*" permits all.
	AllowedProjects []string
}

// AllowsProject reports whether project is in the allowed set.
func (r Record) AllowsProject(project string) bool {
	for _, p := range r.AllowedProjects {
		if p == "*" || strings.EqualFold(p, project) {
			return true
		}
	}
	return false
}

// Store is the low-level key/value store for vault Records.
type Store interface {
	Get(ctx context.Context, key Key) (Record, error)
	Put(ctx context.Context, key Key, record Record) error
	Delete(ctx context.Context, key Key) error
	List(ctx context.Context) ([]Key, error)
}

// Vault is the high-level credential store with lazy refresh and project scoping.
type Vault interface {
	Get(ctx context.Context, key Key) (Record, error)
	Put(ctx context.Context, key Key, record Record) error
	Delete(ctx context.Context, key Key) error
	List(ctx context.Context) ([]Key, error)
	// Source returns a cred.CredentialSource for key scoped to project.
	Source(key Key, project string) (cred.CredentialSource, error)
	ForceRefresh(ctx context.Context, key Key) (Record, error)
}
