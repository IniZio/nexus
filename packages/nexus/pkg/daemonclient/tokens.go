// packages/nexus/pkg/daemonclient/tokens.go

package daemonclient

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/auth"
)

// TokenSet represents stored authentication
type TokenSet struct {
	// For local mode: the token string
	// For future OIDC: access_token, refresh_token, expiry
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token,omitempty"`
	TokenType    string     `json:"token_type"` // "bearer", "local"
	Expiry       *time.Time `json:"expiry,omitempty"`

	// Identity snapshot at login time
	Identity *auth.Identity `json:"identity,omitempty"`
}

// TokenStore manages authentication tokens
type TokenStore interface {
	// Save stores tokens for a daemon endpoint
	Save(daemonEndpoint string, tokens *TokenSet) error

	// Load retrieves tokens for a daemon endpoint
	Load(daemonEndpoint string) (*TokenSet, error)

	// Clear removes tokens for a daemon endpoint
	Clear(daemonEndpoint string) error

	// List returns all stored daemon endpoints
	List() ([]string, error)
}

// FileTokenStore implements TokenStore with filesystem storage
type FileTokenStore struct {
	basePath string
}

// NewFileTokenStore creates a new file-based token store
func NewFileTokenStore(basePath string) *FileTokenStore {
	return &FileTokenStore{basePath: basePath}
}

// Save stores tokens to file
func (s *FileTokenStore) Save(daemonEndpoint string, tokens *TokenSet) error {
	// Ensure directory exists
	if err := os.MkdirAll(s.basePath, 0700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}

	// Encode endpoint for safe filename
	filename := encodeEndpoint(daemonEndpoint) + ".json"
	filepath := filepath.Join(s.basePath, filename)

	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tokens: %w", err)
	}

	// Write with restricted permissions
	if err := os.WriteFile(filepath, data, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}

	return nil
}

// Load retrieves tokens from file
func (s *FileTokenStore) Load(daemonEndpoint string) (*TokenSet, error) {
	filename := encodeEndpoint(daemonEndpoint) + ".json"
	filepath := filepath.Join(s.basePath, filename)

	data, err := os.ReadFile(filepath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no tokens for endpoint %s", daemonEndpoint)
		}
		return nil, fmt.Errorf("read token file: %w", err)
	}

	var tokens TokenSet
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("unmarshal tokens: %w", err)
	}

	return &tokens, nil
}

// Clear removes tokens for an endpoint
func (s *FileTokenStore) Clear(daemonEndpoint string) error {
	filename := encodeEndpoint(daemonEndpoint) + ".json"
	filepath := filepath.Join(s.basePath, filename)

	if err := os.Remove(filepath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove token file: %w", err)
	}

	return nil
}

// List returns all stored endpoints
func (s *FileTokenStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read token directory: %w", err)
	}

	var endpoints []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			// Decode filename back to endpoint
			name := entry.Name()
			endpoint := decodeEndpoint(name[:len(name)-5]) // Remove .json
			endpoints = append(endpoints, endpoint)
		}
	}

	return endpoints, nil
}

// encodeEndpoint creates a safe filename from an endpoint
func encodeEndpoint(endpoint string) string {
	// Simple encoding: replace special characters
	// In production, use proper URL encoding or hashing
	result := ""
	for _, c := range endpoint {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-' || c == '_' || c == '.':
			result += string(c)
		case c == ':':
			result += "_col_"
		case c == '/':
			result += "_sl_"
		default:
			result += fmt.Sprintf("_%d_", c)
		}
	}
	return result
}

// decodeEndpoint reverses encodeEndpoint
func decodeEndpoint(filename string) string {
	// Reverse the encoding
	result := ""
	i := 0
	for i < len(filename) {
		if i+5 <= len(filename) && filename[i:i+5] == "_col_" {
			result += ":"
			i += 5
		} else if i+4 <= len(filename) && filename[i:i+4] == "_sl_" {
			result += "/"
			i += 4
		} else if filename[i] == '_' {
			// Read until next '_'
			j := i + 1
			for j < len(filename) && filename[j] != '_' {
				j++
			}
			if j < len(filename) {
				var code int
				fmt.Sscanf(filename[i+1:j], "%d", &code)
				result += string(rune(code))
				i = j + 1
			} else {
				result += string(filename[i])
				i++
			}
		} else {
			result += string(filename[i])
			i++
		}
	}
	return result
}
