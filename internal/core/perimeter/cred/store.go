package cred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corestore "github.com/IniZio/nexus3/internal/core/store"
)

// ErrStoreAbsent is returned by [LoadStore] when the store file does not exist.
// Callers that want to distinguish "not yet provisioned" from other errors
// can use errors.Is(err, ErrStoreAbsent).
var ErrStoreAbsent = errors.New("cred: dedicated credential store not found")

// storeSchema is the on-disk JSON representation of nexus3's own OAuth
// material. It is unexported; callers interact through [DedicatedCredStore].
type storeSchema struct {
	// Token fields.
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`

	// OAuth client / endpoint metadata needed by the host-side refresher (S1).
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	TokenEndpoint string `json:"token_endpoint"`
}

// DedicatedCredStore holds nexus3's own OAuth material for a single upstream
// credential. It is the in-memory representation loaded from a host-disk store
// file by [LoadStore].
//
// All fields are read after load; callers must not mutate them.
type DedicatedCredStore struct {
	// AccessToken is the current bearer token. May be expired; check ExpiresAt.
	AccessToken string

	// RefreshToken is the long-lived token used by the host-side refresher (S1)
	// to obtain a new AccessToken without user interaction.
	RefreshToken string

	// ExpiresAt is the wall-clock expiry of AccessToken.
	ExpiresAt time.Time

	// TokenType is the RFC 6749 token type (typically "Bearer").
	TokenType string

	// ClientID, ClientSecret, and TokenEndpoint are the OAuth client
	// registration details the host-side refresher needs to call the token
	// endpoint. ClientSecret may be empty for public clients.
	ClientID      string
	ClientSecret  string
	TokenEndpoint string
}

// LoadStore reads a [DedicatedCredStore] from the JSON file at path.
//
// A missing file returns [ErrStoreAbsent] (use errors.Is to detect it).
// A present-but-malformed file returns a descriptive error.
// A present-but-empty AccessToken returns an error; a store without a token is
// not usable and callers must not silently treat it as "no credential".
func LoadStore(path string) (*DedicatedCredStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrStoreAbsent, path)
		}
		return nil, fmt.Errorf("cred: reading store %s: %w", path, err)
	}

	var s storeSchema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("cred: parsing store %s: %w", path, err)
	}

	if s.AccessToken == "" {
		return nil, fmt.Errorf("cred: store %s has empty access_token; cannot use absent credential", path)
	}

	return &DedicatedCredStore{
		AccessToken:   s.AccessToken,
		RefreshToken:  s.RefreshToken,
		ExpiresAt:     s.ExpiresAt,
		TokenType:     s.TokenType,
		ClientID:      s.ClientID,
		ClientSecret:  s.ClientSecret,
		TokenEndpoint: s.TokenEndpoint,
	}, nil
}

// lockFilePath returns the path of the advisory lock file used to serialise
// cross-process reads and writes of the credential store at storePath.
func lockFilePath(storePath string) string {
	return storePath + ".lock"
}

// WithStoreLock serialises a read-modify-write of the credential store at
// storePath across OS processes by holding an exclusive flock. It:
//
//  1. Opens (or creates) storePath+".lock" as the advisory lock file.
//  2. Acquires an exclusive flock within a 30 s deadline using non-blocking
//     TryExclusive retries with a 5 ms backoff, so the context deadline has real
//     force (no kernel-park risk past the deadline). A stale lock cannot
//     deadlock forever: the kernel releases it automatically when the holding
//     process dies, including SIGKILL.
//  3. Re-reads storePath under the lock and passes the result to fn. fn
//     receives nil when the file is absent.
//  4. If fn returns a non-nil *DedicatedCredStore, atomically saves it to
//     storePath before releasing the lock.
//  5. Propagates fn's error without saving when fn returns a non-nil error.
//
// # Why hold the lock across a network call
//
// fn may make an OAuth HTTP refresh call while the lock is held. Holding the
// lock through the network call prevents two concurrent supervisor processes
// from simultaneously consuming the same refresh_token. Anthropic's token
// endpoint rotates the refresh_token on every grant — a second call with the
// now-invalidated token returns invalid_grant and permanently bricks the
// credential chain. The process that loses the race re-reads the freshly
// written token from disk and skips the HTTP call.
func WithStoreLock(ctx context.Context, storePath string, fn func(*DedicatedCredStore) (*DedicatedCredStore, error)) error {
	lkPath := lockFilePath(storePath)
	lk, err := corestore.OpenLock(lkPath)
	if err != nil {
		return fmt.Errorf("cred: open store lock %s: %w", lkPath, err)
	}
	defer lk.Close()

	const lockTimeout = 30 * time.Second
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	if err := lk.TryExclusive(lockCtx); err != nil {
		return fmt.Errorf("cred: acquire store lock %s (timeout %s): %w", lkPath, lockTimeout, err)
	}

	// Re-read under the lock so fn sees the freshest on-disk state.
	current, err := LoadStore(storePath)
	if err != nil {
		if !errors.Is(err, ErrStoreAbsent) {
			return fmt.Errorf("cred: read store under lock: %w", err)
		}
		current = nil // absent is fine; fn decides what to do
	}

	updated, fnErr := fn(current)
	if fnErr != nil {
		return fnErr
	}
	if updated != nil {
		if saveErr := SaveStore(storePath, updated); saveErr != nil {
			return fmt.Errorf("cred: save store under lock: %w", saveErr)
		}
	}
	return nil
}

// SaveStore atomically writes s to the JSON store file at path (mode 0600).
//
// The write is atomic: a temp file is written in the same directory as path
// (guaranteeing same-filesystem rename), chmod'd to 0600, then renamed over
// path. The temp file is removed on any error before the rename.
//
// SaveStore rejects an empty AccessToken; a store without a bearer token is
// not usable and must not be persisted.
func SaveStore(path string, s *DedicatedCredStore) error {
	if s.AccessToken == "" {
		return fmt.Errorf("cred: SaveStore %s: empty access_token; refusing to persist unusable credential", path)
	}

	schema := storeSchema{
		AccessToken:   s.AccessToken,
		RefreshToken:  s.RefreshToken,
		ExpiresAt:     s.ExpiresAt,
		TokenType:     s.TokenType,
		ClientID:      s.ClientID,
		ClientSecret:  s.ClientSecret,
		TokenEndpoint: s.TokenEndpoint,
	}

	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return fmt.Errorf("cred: SaveStore %s: marshalling store: %w", path, err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".store-*.tmp")
	if err != nil {
		return fmt.Errorf("cred: SaveStore %s: creating temp file: %w", path, err)
	}
	tmpName := tmp.Name()

	// Ensure temp file is cleaned up on any failure before rename.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cred: SaveStore %s: chmod temp file: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cred: SaveStore %s: writing temp file: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cred: SaveStore %s: closing temp file: %w", path, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("cred: SaveStore %s: renaming temp file: %w", path, err)
	}
	cleanup = false
	return nil
}
