package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"

	"github.com/IniZio/nexus/internal/core/vault"
)

// DefaultGitHubClientID is the nexus GitHub App client ID (device flow enabled).
const DefaultGitHubClientID = "Iv23liNEYppyhvQpXxXy"

var (
	ErrAuthorizationPending = errors.New("connectors: authorization pending")
	ErrSlowDown             = errors.New("connectors: slow down")
	ErrAccessDenied         = errors.New("connectors: access denied")
)

var githubAllowedHosts = []string{"github.com", "api.github.com", "uploads.github.com"}

// GitHubConnector implements vault.Connector using the GitHub App device flow.
type GitHubConnector struct {
	cfg        *oauth2.Config
	httpClient *http.Client
}

// NewGitHub returns a connector. Empty clientID falls back to DefaultGitHubClientID.
func NewGitHub(clientID string) *GitHubConnector {
	if clientID == "" {
		clientID = DefaultGitHubClientID
	}
	return &GitHubConnector{
		cfg: &oauth2.Config{
			ClientID: clientID,
			Endpoint: endpoints.GitHub,
			Scopes:   []string{"repo", "read:user", "read:org"},
		},
	}
}

func newGitHubWithEndpoint(clientID string, ep oauth2.Endpoint, hc *http.Client) *GitHubConnector {
	c := NewGitHub(clientID)
	c.cfg.Endpoint = ep
	c.httpClient = hc
	return c
}

func (c *GitHubConnector) ID() string               { return "github" }
func (c *GitHubConnector) LinkFlow() vault.LinkFlow { return vault.LinkFlowDevice }
func (c *GitHubConnector) AllowedHosts() []string {
	return append([]string(nil), githubAllowedHosts...)
}

func (c *GitHubConnector) StartDevice(ctx context.Context) (vault.DeviceAuth, error) {
	resp, err := c.cfg.DeviceAuth(c.oauthCtx(ctx))
	if err != nil {
		return vault.DeviceAuth{}, fmt.Errorf("github: start device: %w", err)
	}
	return vault.DeviceAuth{
		DeviceCode:      resp.DeviceCode,
		UserCode:        resp.UserCode,
		VerificationURI: resp.VerificationURI,
		ExpiresIn:       int(time.Until(resp.Expiry).Seconds()),
		Interval:        int(resp.Interval),
	}, nil
}

// PollDevice makes a single token poll. Returns ErrAuthorizationPending or ErrSlowDown when not yet ready.
func (c *GitHubConnector) PollDevice(ctx context.Context, deviceCode string) (vault.Record, error) {
	hc := c.resolvedClient()
	v := url.Values{
		"client_id":   {c.cfg.ClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint.TokenURL,
		strings.NewReader(v.Encode()))
	if err != nil {
		return vault.Record{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return vault.Record{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return vault.Record{}, fmt.Errorf("github: poll device: malformed response: %w", err)
	}
	switch out.Error {
	case "":
	case "authorization_pending":
		return vault.Record{}, ErrAuthorizationPending
	case "slow_down":
		return vault.Record{}, ErrSlowDown
	case "access_denied":
		return vault.Record{}, ErrAccessDenied
	case "expired_token":
		return vault.Record{}, fmt.Errorf("github: device code expired")
	default:
		return vault.Record{}, fmt.Errorf("github: poll device error: %s", out.Error)
	}
	expiry := time.Now().Add(8 * time.Hour)
	if out.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	var scopes []string
	if out.Scope != "" {
		scopes = strings.Split(strings.TrimSpace(out.Scope), ",")
	}
	return vault.Record{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Expiry:       expiry,
		Scopes:       scopes,
	}, nil
}

func (c *GitHubConnector) AuthURL(_ string) (string, string, error) {
	return "", "", fmt.Errorf("github: device-flow connector does not support PKCE")
}

func (c *GitHubConnector) Exchange(_ context.Context, _, _ string) (vault.Record, error) {
	return vault.Record{}, fmt.Errorf("github: device-flow connector does not support Exchange")
}

func (c *GitHubConnector) Refresh(ctx context.Context, rec vault.Record) (vault.Record, error) {
	ts := c.cfg.TokenSource(c.oauthCtx(ctx), &oauth2.Token{
		RefreshToken: rec.RefreshToken,
		Expiry:       time.Now().Add(-time.Second),
	})
	tok, err := ts.Token()
	if err != nil {
		return vault.Record{}, fmt.Errorf("github: refresh: %w", err)
	}
	return vault.Record{
		AccessToken:     tok.AccessToken,
		RefreshToken:    tok.RefreshToken,
		Expiry:          tok.Expiry,
		Scopes:          rec.Scopes,
		AllowedProjects: rec.AllowedProjects,
	}, nil
}

func (c *GitHubConnector) oauthCtx(ctx context.Context) context.Context {
	if c.httpClient != nil {
		return context.WithValue(ctx, oauth2.HTTPClient, c.httpClient)
	}
	return ctx
}

func (c *GitHubConnector) resolvedClient() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	return http.DefaultClient
}
