package auth

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// Authgear OIDC configuration constants
const (
	AuthgearIssuer   = "https://certain-thing-311.authgear.cloud"
	AuthgearClientID = "9a0da7557c863ff9"
	AuthgearScopes   = "openid profile email"
)

// Config holds OIDC authentication configuration
type Config struct {
	Issuer           string   `yaml:"issuer"`
	ClientID         string   `yaml:"client_id"`
	ClientSecret     string   `yaml:"client_secret,omitempty"`
	RedirectURL      string   `yaml:"redirect_url"`
	Scopes           string   `yaml:"scopes"`
	TokenExpiry      string   `yaml:"token_expiry"`
	SkipTLSVerify    bool     `yaml:"skip_tls_verify,omitempty"`
	SkipIssuerCheck  bool     `yaml:"skip_issuer_check,omitempty"`
	AllowedAudiences []string `yaml:"allowed_audiences,omitempty"`
}

// OIDCProvider manages OIDC authentication with Authgear
type OIDCProvider struct {
	config       *Config
	verifier     *oidc.IDTokenVerifier
	oauth2Config *oauth2.Config
	provider     *oidc.Provider
	httpClient   *http.Client
	jwksCache    *JWKSCache
	mu           sync.RWMutex
}

// JWKSCache caches the JWKS for token validation
type JWKSCache struct {
	keys       *jose.JSONWebKeySet
	expiration time.Time
	mu         sync.RWMutex
}

// TokenClaims represents the claims in an OIDC token
type TokenClaims struct {
	jwt.RegisteredClaims
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Phone         string `json:"phone_number"`
	PhoneVerified bool   `json:"phone_number_verified"`
	Username      string `json:"preferred_username"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	Profile       string `json:"profile"`
}

// NewOIDCProvider creates a new OIDC provider with Authgear
func NewOIDCProvider(ctx context.Context, config *Config) (*OIDCProvider, error) {
	if config.Issuer == "" {
		config.Issuer = AuthgearIssuer
	}
	if config.ClientID == "" {
		config.ClientID = AuthgearClientID
	}
	if config.Scopes == "" {
		config.Scopes = AuthgearScopes
	}
	if config.TokenExpiry == "" {
		config.TokenExpiry = "24h"
	}

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}
	if config.SkipTLSVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	provider, err := oidc.NewProvider(ctx, config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC provider: %w", err)
	}

	jwksCache := &JWKSCache{}

	verifierConfig := &oidc.Config{
		ClientID:             config.ClientID,
		SupportedSigningAlgs: []string{"RS256"},
		SkipIssuerCheck:      config.SkipIssuerCheck,
	}
	// Audience is validated via ClientID field - Authgear uses client_id as audience
	if len(config.AllowedAudiences) > 0 {
		verifierConfig.SkipClientIDCheck = false // Use ClientID for audience check
	}

	oauth2Config := &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  config.RedirectURL,
		Scopes:       strings.Split(config.Scopes, " "),
	}

	return &OIDCProvider{
		config:       config,
		provider:     provider,
		verifier:     provider.Verifier(verifierConfig),
		oauth2Config: oauth2Config,
		httpClient:   httpClient,
		jwksCache:    jwksCache,
	}, nil
}

// NewAuthgearProvider is a convenience function to create Authgear provider
func NewAuthgearProvider(ctx context.Context, redirectURL string) (*OIDCProvider, error) {
	config := &Config{
		Issuer:      AuthgearIssuer,
		ClientID:    AuthgearClientID,
		RedirectURL: redirectURL,
		Scopes:      AuthgearScopes,
		TokenExpiry: "24h",
	}
	return NewOIDCProvider(ctx, config)
}

// AuthCodeURL generates the OAuth2 authorization URL
func (p *OIDCProvider) AuthCodeURL(state string, opts ...oauth2.AuthCodeOption) string {
	return p.oauth2Config.AuthCodeURL(state, opts...)
}

// Exchange exchanges an authorization code for tokens
func (p *OIDCProvider) Exchange(ctx context.Context, code string, opts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	return p.oauth2Config.Exchange(ctx, code, opts...)
}

// VerifyIDToken validates an ID token and returns the claims
func (p *OIDCProvider) VerifyIDToken(ctx context.Context, rawToken string) (*TokenClaims, error) {
	idToken, err := p.verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("failed to verify ID token: %w", err)
	}

	var claims TokenClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("failed to parse claims: %w", err)
	}

	return &claims, nil
}

// RefreshToken refreshes an access token using a refresh token
func (p *OIDCProvider) RefreshToken(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	token := &oauth2.Token{
		RefreshToken: refreshToken,
	}
	return p.oauth2Config.TokenSource(ctx, token).Token()
}

// UserInfo retrieves user information from the UserInfo endpoint
func (p *OIDCProvider) UserInfo(ctx context.Context, accessToken string) (*TokenClaims, error) {
	userInfo, err := p.provider.UserInfo(ctx, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: accessToken,
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	claims := &TokenClaims{}
	if err := userInfo.Claims(claims); err != nil {
		return nil, fmt.Errorf("failed to parse user info: %w", err)
	}

	return claims, nil
}

// RevokeToken revokes an access or refresh token
func (p *OIDCProvider) RevokeToken(ctx context.Context, token string) error {
	revocationURL := strings.TrimSuffix(p.config.Issuer, "/") + "/oauth2/revoke"

	req, err := http.NewRequestWithContext(ctx, "POST", revocationURL, strings.NewReader(
		fmt.Sprintf("token=%s", token),
	))
	if err != nil {
		return fmt.Errorf("failed to create revocation request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if p.config.ClientSecret != "" {
		req.SetBasicAuth(p.config.ClientID, p.config.ClientSecret)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to revoke token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token revocation failed with status: %d", resp.StatusCode)
	}

	return nil
}

// EndSession ends a user session
func (p *OIDCProvider) EndSession(ctx context.Context, idTokenHint, postLogoutRedirectURI string) string {
	endSessionURL := strings.TrimSuffix(p.config.Issuer, "/") + "/oauth2/end_session"

	params := fmt.Sprintf("id_token_hint=%s", idTokenHint)
	if postLogoutRedirectURI != "" {
		params += fmt.Sprintf("&post_logout_redirect_uri=%s", postLogoutRedirectURI)
	}

	return fmt.Sprintf("%s?%s", endSessionURL, params)
}

// GetConfig returns the OIDC configuration
func (p *OIDCProvider) GetConfig() *Config {
	return p.config
}

// GetProvider returns the OIDC provider
func (p *OIDCProvider) GetProvider() *oidc.Provider {
	return p.provider
}

// IntrospectToken introspects an access token
func (p *OIDCProvider) IntrospectToken(ctx context.Context, token string) (*jwt.RegisteredClaims, error) {
	introspectionURL := strings.TrimSuffix(p.config.Issuer, "/") + "/oauth2/introspect"

	req, err := http.NewRequestWithContext(ctx, "POST", introspectionURL, strings.NewReader(
		fmt.Sprintf("token=%s", token),
	))
	if err != nil {
		return nil, fmt.Errorf("failed to create introspect request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(p.config.ClientID, p.config.ClientSecret)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to introspect token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token introspection failed with status: %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse introspection response: %w", err)
	}

	active, ok := result["active"].(bool)
	if !ok || !active {
		return nil, fmt.Errorf("token is not active")
	}

	if tokenStr, ok := result["token"].(string); ok {
		parts := strings.Split(tokenStr, ".")
		if len(parts) == 3 {
			payload, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err == nil {
				var claims jwt.RegisteredClaims
				if json.Unmarshal(payload, &claims) == nil {
					return &claims, nil
				}
			}
		}
	}

	return nil, nil
}

// IsTokenValid checks if a token is valid without full verification
func (p *OIDCProvider) IsTokenValid(ctx context.Context, rawToken string) (bool, error) {
	claims, err := p.VerifyIDToken(ctx, rawToken)
	if err != nil {
		return false, err
	}

	if claims.ExpiresAt != nil && claims.ExpiresAt.Before(time.Now()) {
		return false, fmt.Errorf("token has expired")
	}

	if claims.NotBefore != nil && claims.NotBefore.After(time.Now()) {
		return false, fmt.Errorf("token is not yet valid")
	}

	if claims.IssuedAt != nil && claims.IssuedAt.After(time.Now().Add(60*time.Second)) {
		return false, fmt.Errorf("token is issued in the future")
	}

	return true, nil
}

// GetJWKS returns the JSON Web Key Set for signature verification
func (p *OIDCProvider) GetJWKS() (*jose.JSONWebKeySet, error) {
	p.jwksCache.mu.RLock()
	if p.jwksCache.keys != nil && time.Now().Before(p.jwksCache.expiration) {
		defer p.jwksCache.mu.RUnlock()
		return p.jwksCache.keys, nil
	}
	p.jwksCache.mu.RUnlock()

	p.jwksCache.mu.Lock()
	defer p.jwksCache.mu.Unlock()

	if p.jwksCache.keys != nil && time.Now().Before(p.jwksCache.expiration) {
		return p.jwksCache.keys, nil
	}

	jwksURL := strings.TrimSuffix(p.config.Issuer, "/") + "/oauth2/jwks"
	resp, err := p.httpClient.Get(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS request failed with status: %d", resp.StatusCode)
	}

	var jwks jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("failed to parse JWKS: %w", err)
	}

	p.jwksCache.keys = &jwks
	p.jwksCache.expiration = time.Now().Add(1 * time.Hour)

	return &jwks, nil
}

// CreateClientCredentialsToken creates a token using client credentials grant
func (p *OIDCProvider) CreateClientCredentialsToken(ctx context.Context, scopes []string) (*oauth2.Token, error) {
	tokenURL := strings.TrimSuffix(p.config.Issuer, "/") + "/oauth2/token"

	values := map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     p.config.ClientID,
		"client_secret": p.config.ClientSecret,
	}
	if len(scopes) > 0 {
		values["scope"] = strings.Join(scopes, " ")
	}

	body := ""
	for k, v := range values {
		if body != "" {
			body += "&"
		}
		body += fmt.Sprintf("%s=%s", k, v)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request failed with status: %d", resp.StatusCode)
	}

	var tokenRes struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenRes); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &oauth2.Token{
		AccessToken:  tokenRes.AccessToken,
		TokenType:    tokenRes.TokenType,
		RefreshToken: tokenRes.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(tokenRes.ExpiresIn) * time.Second),
	}, nil
}

// Discovery represents the OIDC discovery document
type Discovery struct {
	Issuer                           string   `json:"issuer"`
	AuthorizationEndpoint            string   `json:"authorization_endpoint"`
	TokenEndpoint                    string   `json:"token_endpoint"`
	UserInfoEndpoint                 string   `json:"userinfo_endpoint"`
	JWKSURI                          string   `json:"jwks_uri"`
	ScopesSupported                  []string `json:"scopes_supported"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	SubjectTypesSupported            []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`
	ClaimsSupported                  []string `json:"claims_supported"`
}

// Discovery returns the OIDC discovery document
func (p *OIDCProvider) Discovery(ctx context.Context) (*Discovery, error) {
	discoveryURL := strings.TrimSuffix(p.config.Issuer, "/") + "/.well-known/openid-configuration"
	resp, err := p.httpClient.Get(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch discovery document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery request failed with status: %d", resp.StatusCode)
	}

	var discovery Discovery
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return nil, fmt.Errorf("failed to parse discovery document: %w", err)
	}

	return &discovery, nil
}
