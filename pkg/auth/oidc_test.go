package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOIDCProvider(t *testing.T) {
	ctx := context.Background()

	// Create a mock OIDC server - must use consistent issuer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fmt.Sprintf("http://%s", r.Host)
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                                issuer,
				"authorization_endpoint":                issuer + "/oauth2/authorize",
				"token_endpoint":                        issuer + "/oauth2/token",
				"userinfo_endpoint":                     issuer + "/userinfo",
				"jwks_uri":                              issuer + "/jwks",
				"scopes_supported":                      []string{"openid", "profile", "email"},
				"response_types_supported":              []string{"code"},
				"subject_types_supported":               []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
				"claims_supported":                      []string{"iss", "aud", "iat", "exp", "sub", "email"},
			})
		case "/oauth2/jwks":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"keys": []map[string]interface{}{
					{
						"kty": "RSA",
						"use": "sig",
						"kid": "test-key-id",
						"alg": "RS256",
						"n":   "test-n",
						"e":   "AQAB",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config := &Config{
		Issuer:      server.URL,
		ClientID:    "test-client-id",
		RedirectURL: "http://localhost:8080/callback",
		Scopes:      "openid profile email",
	}

	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	assert.Equal(t, config.Issuer, provider.GetConfig().Issuer)
	assert.Equal(t, config.ClientID, provider.GetConfig().ClientID)
	assert.NotNil(t, provider.GetProvider())
}

func TestNewAuthgearProvider(t *testing.T) {
	ctx := context.Background()

	provider, err := NewAuthgearProvider(ctx, "http://localhost:8080/callback")
	// This will fail because Authgear is not reachable
	if err != nil {
		// Expected - Authgear server not available in test
		assert.Contains(t, err.Error(), "failed to create OIDC provider")
		return
	}

	assert.NotNil(t, provider)
	assert.Equal(t, AuthgearIssuer, provider.GetConfig().Issuer)
	assert.Equal(t, AuthgearClientID, provider.GetConfig().ClientID)
}

func TestOIDCProvider_AuthCodeURL(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fmt.Sprintf("http://%s", r.Host)
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 issuer,
				"authorization_endpoint": issuer + "/oauth2/authorize",
				"token_endpoint":         issuer + "/oauth2/token",
				"jwks_uri":               issuer + "/jwks",
			})
		case "/oauth2/jwks":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"keys": []map[string]interface{}{},
			})
		}
	}))
	defer server.Close()

	config := &Config{
		Issuer:      server.URL,
		ClientID:    "test-client",
		RedirectURL: "http://localhost:8080/callback",
	}

	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	authURL := provider.AuthCodeURL("test-state")
	assert.Contains(t, authURL, "state=test-state")
	assert.Contains(t, authURL, "client_id=test-client")
	assert.Contains(t, authURL, "redirect_uri=")
}

func TestOIDCProvider_Discovery(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fmt.Sprintf("http://%s", r.Host)
		if r.URL.Path == "/.well-known/openid-configuration" {
			// Return the expected discovery with matching issuer
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 issuer,
				"authorization_endpoint": issuer + "/oauth2/authorize",
				"token_endpoint":         issuer + "/oauth2/token",
				"userinfo_endpoint":      issuer + "/userinfo",
				"jwks_uri":               issuer + "/jwks",
				"scopes_supported":       []string{"openid", "profile", "email"},
			})
		}
	}))
	defer server.Close()

	config := &Config{
		Issuer: server.URL,
	}

	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	discovery, err := provider.Discovery(ctx)
	require.NoError(t, err)
	assert.Equal(t, server.URL, discovery.Issuer)
	assert.Contains(t, discovery.AuthorizationEndpoint, "/oauth2/authorize")
	assert.Contains(t, discovery.TokenEndpoint, "/oauth2/token")
}

func TestOIDCProvider_JWKSCache(t *testing.T) {
	ctx := context.Background()

	fetchCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fmt.Sprintf("http://%s", r.Host)
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 issuer,
				"authorization_endpoint": issuer + "/oauth2/authorize",
				"token_endpoint":         issuer + "/oauth2/token",
				"jwks_uri":               issuer + "/oauth2/jwks",
			})
		case "/oauth2/jwks":
			fetchCount++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"keys": []map[string]interface{}{
					{
						"kty": "RSA",
						"use": "sig",
						"kid": "key-1",
						"alg": "RS256",
						"n":   "test-n",
						"e":   "AQAB",
					},
				},
			})
		}
	}))
	defer server.Close()

	config := &Config{
		Issuer: server.URL,
	}

	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	// First fetch - should hit the server
	jwks1, err := provider.GetJWKS()
	require.NoError(t, err)
	assert.Len(t, jwks1.Keys, 1)
	assert.Equal(t, 1, fetchCount)

	// Second fetch - should use cache
	jwks2, err := provider.GetJWKS()
	require.NoError(t, err)
	assert.Equal(t, jwks1, jwks2)
	assert.Equal(t, 1, fetchCount) // Still 1, not 2
}

func TestOIDCProvider_EndSession(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fmt.Sprintf("http://%s", r.Host)
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 issuer,
				"authorization_endpoint": issuer + "/oauth2/authorize",
				"token_endpoint":         issuer + "/oauth2/token",
				"end_session_endpoint":   issuer + "/end_session",
				"jwks_uri":               issuer + "/jwks",
			})
		case "/oauth2/jwks":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"keys": []map[string]interface{}{},
			})
		}
	}))
	defer server.Close()

	config := &Config{
		Issuer: server.URL,
	}

	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	endSessionURL := provider.EndSession(ctx, "test-id-token", "http://localhost:8080/logout")
	assert.Contains(t, endSessionURL, "id_token_hint=test-id-token")
	assert.Contains(t, endSessionURL, "post_logout_redirect_uri=http://localhost:8080/logout")
}

func TestTokenClaims(t *testing.T) {
	claims := &TokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://test.example.com",
			Subject:   "user-123",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Email:         "test@example.com",
		EmailVerified: true,
		Phone:         "+1234567890",
		Username:      "testuser",
		Name:          "Test User",
		Picture:       "https://example.com/avatar.png",
	}

	assert.Equal(t, "test@example.com", claims.Email)
	assert.True(t, claims.EmailVerified)
	assert.Equal(t, "+1234567890", claims.Phone)
	assert.Equal(t, "testuser", claims.Username)
	assert.Equal(t, "Test User", claims.Name)
	assert.Equal(t, "https://example.com/avatar.png", claims.Picture)
}

func TestConfigDefaults(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fmt.Sprintf("http://%s", r.Host)
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 issuer,
				"authorization_endpoint": issuer + "/oauth2/authorize",
				"token_endpoint":         issuer + "/oauth2/token",
				"jwks_uri":               issuer + "/jwks",
			})
		case "/oauth2/jwks":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"keys": []map[string]interface{}{},
			})
		}
	}))
	defer server.Close()

	config := &Config{
		Issuer:      server.URL,
		RedirectURL: "http://localhost:8080/callback",
	}

	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	// Verify defaults were applied
	cfg := provider.GetConfig()
	assert.Equal(t, AuthgearClientID, cfg.ClientID)
	assert.Equal(t, AuthgearScopes, cfg.Scopes)
}

func TestIsTokenValid_Expired(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fmt.Sprintf("http://%s", r.Host)
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 issuer,
				"authorization_endpoint": issuer + "/oauth2/authorize",
				"token_endpoint":         issuer + "/oauth2/token",
				"jwks_uri":               issuer + "/jwks",
			})
		case "/oauth2/jwks":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"keys": []map[string]interface{}{
					{
						"kty": "RSA",
						"use": "sig",
						"kid": "test-key",
						"alg": "RS256",
						"n":   "test-n",
						"e":   "AQAB",
					},
				},
			})
		}
	}))
	defer server.Close()

	config := &Config{
		Issuer:      server.URL,
		ClientID:    "test-client",
		RedirectURL: "http://localhost:8080/callback",
	}

	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	// Create an expired token (invalid signature but that's OK for this test)
	expiredToken := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJodHRwczovL3Rlc3QuZXhhbXBsZS5jb20iLCJzdWIiOiJ1c2VyLTEyMyIsImV4cCI6MTYyMDAwMDAwMCwiaWF0IjoxNjIwMDAwMDAwLCJlbWFpbCI6InRlc3RAZXhhbXBsZS5jb20ifQ.invalid-signature"

	_, err = provider.IsTokenValid(ctx, expiredToken)
	assert.Error(t, err)
}

func TestCreateClientCredentialsToken(t *testing.T) {
	ctx := context.Background()

	tokenResponse := map[string]interface{}{
		"access_token": "test-access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fmt.Sprintf("http://%s", r.Host)
		if r.URL.Path == "/oauth2/token" && r.Method == "POST" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tokenResponse)
			return
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 issuer,
				"authorization_endpoint": issuer + "/oauth2/authorize",
				"token_endpoint":         issuer + "/oauth2/token",
				"jwks_uri":               issuer + "/jwks",
			})
		case "/oauth2/jwks":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"keys": []map[string]interface{}{},
			})
		}
	}))
	defer server.Close()

	config := &Config{
		Issuer:       server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURL:  "http://localhost:8080/callback",
	}

	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	token, err := provider.CreateClientCredentialsToken(ctx, []string{"openid", "profile"})
	require.NoError(t, err)
	assert.NotNil(t, token)
	assert.Equal(t, "test-access-token", token.AccessToken)
	assert.Equal(t, "Bearer", token.TokenType)
	assert.True(t, token.Expiry.After(time.Now()))
}

func TestRevokeToken(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fmt.Sprintf("http://%s", r.Host)
		if r.URL.Path == "/oauth2/revoke" && r.Method == "POST" {
			assert.Contains(t, r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
			w.WriteHeader(http.StatusOK)
			return
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 issuer,
				"authorization_endpoint": issuer + "/oauth2/authorize",
				"token_endpoint":         issuer + "/oauth2/token",
				"jwks_uri":               issuer + "/jwks",
			})
		case "/oauth2/jwks":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"keys": []map[string]interface{}{},
			})
		}
	}))
	defer server.Close()

	config := &Config{
		Issuer:       server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURL:  "http://localhost:8080/callback",
	}

	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	err = provider.RevokeToken(ctx, "test-token-to-revoke")
	assert.NoError(t, err)
}
