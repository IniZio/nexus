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

// ErrProjectNotAllowed is returned when the requested project is not in the
// record's AllowedProjects list.
var ErrProjectNotAllowed = errors.New("vault: project not allowed")

// Key identifies a credential record by the principal that owns it and the
// integration it grants access to.
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
	// AllowedProjects is the list of project identifiers this record may be
	// used for. The special value "*" permits all projects.
	AllowedProjects []string
}

// projectAllowed reports whether project is in the allowed set.
func (r Record) projectAllowed(project string) bool {
	for _, p := range r.AllowedProjects {
		if p == "*" || strings.EqualFold(p, project) {
			return true
		}
	}
	return false
}

// Store is a low-level key/value store for vault Records. V1 implements this
// as an encrypted file store; this interface lets tests substitute a MemStore.
type Store interface {
	Get(ctx context.Context, key Key) (Record, error)
	Put(ctx context.Context, key Key, record Record) error
	Delete(ctx context.Context, key Key) error
	List(ctx context.Context) ([]Key, error)
}

// Vault is the high-level credential store. It provides the same CRUD surface
// as Store plus Source, which returns a cred.CredentialSource scoped to a
// specific project. V2 implements the real vault with lazy refresh.
type Vault interface {
	Get(ctx context.Context, key Key) (Record, error)
	Put(ctx context.Context, key Key, record Record) error
	Delete(ctx context.Context, key Key) error
	List(ctx context.Context) ([]Key, error)
	// Source returns a cred.CredentialSource for key scoped to project.
	// It returns ErrUnlinked when no record exists for key, and
	// ErrProjectNotAllowed when project is not in AllowedProjects.
	Source(key Key, project string) (cred.CredentialSource, error)
}
