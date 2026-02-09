package github

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AppConfig holds GitHub App configuration
type AppConfig struct {
	AppID        int64
	ClientID     string
	ClientSecret string
	PrivateKey   *rsa.PrivateKey
	RedirectURL  string
}

// NewAppConfig loads GitHub App configuration from environment variables
// Environment variables:
// - GITHUB_APP_ID: GitHub App ID (e.g., 2680779)
// - GITHUB_APP_CLIENT_ID: OAuth Client ID
// - GITHUB_APP_CLIENT_SECRET: OAuth Client Secret
// - GITHUB_APP_PRIVATE_KEY: Base64-encoded PEM-format RSA private key
// - GITHUB_APP_REDIRECT_URL: OAuth redirect URL (e.g., https://linuxbox.tail31e11.ts.net/auth/github/callback)
func NewAppConfig() (*AppConfig, error) {
	appIDStr := os.Getenv("GITHUB_APP_ID")
	if appIDStr == "" {
		return nil, fmt.Errorf("GITHUB_APP_ID environment variable not set")
	}

	clientID := os.Getenv("GITHUB_APP_CLIENT_ID")
	if clientID == "" {
		return nil, fmt.Errorf("GITHUB_APP_CLIENT_ID environment variable not set")
	}

	clientSecret := os.Getenv("GITHUB_APP_CLIENT_SECRET")
	if clientSecret == "" {
		return nil, fmt.Errorf("GITHUB_APP_CLIENT_SECRET environment variable not set")
	}

	privateKeyB64 := os.Getenv("GITHUB_APP_PRIVATE_KEY")
	if privateKeyB64 == "" {
		return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY environment variable not set")
	}

	redirectURL := os.Getenv("GITHUB_APP_REDIRECT_URL")
	if redirectURL == "" {
		redirectURL = "https://linuxbox.tail31e11.ts.net/auth/github/callback"
	}

	// Parse app ID
	var appID int64
	_, err := fmt.Sscanf(appIDStr, "%d", &appID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse GITHUB_APP_ID: %w", err)
	}

	// Decode base64-encoded private key
	privateKeyPEM, err := base64.StdEncoding.DecodeString(privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode GITHUB_APP_PRIVATE_KEY from base64: %w", err)
	}

	// Parse PEM private key
	privateKey, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse GITHUB_APP_PRIVATE_KEY PEM: %w", err)
	}

	return &AppConfig{
		AppID:        appID,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		PrivateKey:   privateKey,
		RedirectURL:  redirectURL,
	}, nil
}

// parsePrivateKey parses a PEM-encoded RSA private key
func parsePrivateKey(pemData []byte) (*rsa.PrivateKey, error) {
	// Remove PEM headers if present
	pemStr := string(pemData)
	pemStr = strings.TrimSpace(pemStr)

	// Extract the key content
	lines := strings.Split(pemStr, "\n")
	var keyContent strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-----") {
			keyContent.WriteString(line)
		}
	}

	// Decode from base64
	keyBytes, err := base64.StdEncoding.DecodeString(keyContent.String())
	if err != nil {
		return nil, fmt.Errorf("failed to decode PEM key content: %w", err)
	}

	// Parse PKCS#1 private key
	pk, err := x509.ParsePKCS1PrivateKey(keyBytes)
	if err != nil {
		// Try PKCS#8 format
		pk8, err2 := x509.ParsePKCS8PrivateKey(keyBytes)
		if err2 != nil {
			return nil, fmt.Errorf("failed to parse private key (tried PKCS#1 and PKCS#8): %w", err)
		}

		// Convert to RSA private key
		rsaKey, ok := pk8.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("parsed key is not RSA private key")
		}
		pk = rsaKey
	}

	return pk, nil
}

// GenerateJWT generates a JWT token for GitHub App authentication
// The token is signed with the app's private key and expires in 10 minutes
func GenerateJWT(appID int64, privateKey *rsa.PrivateKey) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": appID,
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	return tokenString, nil
}

// OAuthCodeExchangeResponse represents the response from GitHub's OAuth token endpoint
type OAuthCodeExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
}

// GitHubUserResponse represents GitHub's /user API response
type GitHubUserResponse struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// Installation represents a GitHub App installation for a user
type Installation struct {
	InstallationID int64
	GitHubUserID   int64
	GitHubUsername string
	AccessToken    string
	ExpiresAt      time.Time
}

// ExchangeCodeForToken exchanges an OAuth authorization code for an installation ID and user info
// Returns the GitHub user ID, username, and a fresh access token
func ExchangeCodeForToken(ctx context.Context, clientID, clientSecret, code string) (*Installation, error) {
	// Exchange code for access token
	tokenURL := "https://github.com/login/oauth/access_token"
	data := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp OAuthCodeExchangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}

	// Get user info with the access token
	userReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create user request: %w", err)
	}

	userReq.Header.Set("Authorization", fmt.Sprintf("token %s", tokenResp.AccessToken))
	userReq.Header.Set("Accept", "application/vnd.github.v3+json")

	userResp, err := client.Do(userReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	defer userResp.Body.Close()

	if userResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(userResp.Body)
		return nil, fmt.Errorf("GitHub user endpoint returned status %d: %s", userResp.StatusCode, string(body))
	}

	var user GitHubUserResponse
	if err := json.NewDecoder(userResp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode user response: %w", err)
	}

	return &Installation{
		InstallationID: 0,
		GitHubUserID:   user.ID,
		GitHubUsername: user.Login,
		AccessToken:    tokenResp.AccessToken,
		ExpiresAt:      time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
	}, nil
}

// GitHubInstallationsResponse represents the list of GitHub App installations for a user
type GitHubInstallationsResponse struct {
	Installations []struct {
		ID int64 `json:"id"`
	} `json:"installations"`
}

// getInstallationIDForUser fetches the GitHub App installation ID for the authenticated user
func getInstallationIDForUser(ctx context.Context, userAccessToken string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/installations", nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("token %s", userAccessToken))
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch installations: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("GitHub returned status %d: %s", resp.StatusCode, string(body))
	}

	var installResp GitHubInstallationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&installResp); err != nil {
		return 0, fmt.Errorf("failed to decode installations response: %w", err)
	}

	if len(installResp.Installations) == 0 {
		return 0, fmt.Errorf("no GitHub App installations found for user")
	}

	return installResp.Installations[0].ID, nil
}

// InstallationTokenResponse represents the response from GitHub's installation access token endpoint
type InstallationTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GenerateInstallationAccessToken generates a short-lived access token for a GitHub App installation
// The token expires in 1 hour and is used for authenticated git operations
func GenerateInstallationAccessToken(ctx context.Context, appID int64, privateKey *rsa.PrivateKey, installationID int64) (string, time.Time, error) {
	// Generate JWT for app authentication
	jwtToken, err := GenerateJWT(appID, privateKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to generate JWT: %w", err)
	}

	// Request installation access token from GitHub API
	apiURL := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", jwtToken))
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("GitHub returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp InstallationTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to decode token response: %w", err)
	}

	return tokenResp.Token, tokenResp.ExpiresAt, nil
}
