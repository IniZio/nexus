package cred

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
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
