package connectors

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/IniZio/nexus/internal/core/vault"
)

var linearAllowedHosts = []string{"api.linear.app", "mcp.linear.app"}

var linearEndpoint = oauth2.Endpoint{
	AuthURL:  "https://linear.app/oauth/authorize",
	TokenURL: "https://api.linear.app/oauth/token",
}

// LinearConnector implements vault.Connector for Linear using PKCE.
type LinearConnector struct {
	cfg        *oauth2.Config
	httpClient *http.Client
}

// NewLinear returns a connector for Linear. redirectURL must be registered with the Linear app.
func NewLinear(clientID, clientSecret, redirectURL string) *LinearConnector {
	return &LinearConnector{
		cfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint:     linearEndpoint,
			Scopes:       []string{"read", "write", "issues:create", "comments:create"},
		},
	}
}

func newLinearWithEndpoint(clientID, clientSecret, redirectURL string, ep oauth2.Endpoint, hc *http.Client) *LinearConnector {
	c := NewLinear(clientID, clientSecret, redirectURL)
	c.cfg.Endpoint = ep
	c.httpClient = hc
	return c
}

func (c *LinearConnector) ID() string              { return "linear" }
func (c *LinearConnector) LinkFlow() vault.LinkFlow { return vault.LinkFlowPKCE }
func (c *LinearConnector) AllowedHosts() []string   { return append([]string(nil), linearAllowedHosts...) }

func (c *LinearConnector) StartDevice(_ context.Context) (vault.DeviceAuth, error) {
	return vault.DeviceAuth{}, fmt.Errorf("linear: PKCE connector does not support device flow")
}

func (c *LinearConnector) PollDevice(_ context.Context, _ string) (vault.Record, error) {
	return vault.Record{}, fmt.Errorf("linear: PKCE connector does not support device flow")
}

func (c *LinearConnector) AuthURL(state string) (authURL, codeVerifier string, err error) {
	verifier := oauth2.GenerateVerifier()
	u := c.cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	return u, verifier, nil
}

func (c *LinearConnector) Exchange(ctx context.Context, code, codeVerifier string) (vault.Record, error) {
	tok, err := c.cfg.Exchange(c.oauthCtx(ctx), code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return vault.Record{}, fmt.Errorf("linear: exchange: %w", err)
	}
	return linearTokenToRecord(tok, []string{"*"}), nil
}

func (c *LinearConnector) Refresh(ctx context.Context, rec vault.Record) (vault.Record, error) {
	ts := c.cfg.TokenSource(c.oauthCtx(ctx), &oauth2.Token{
		RefreshToken: rec.RefreshToken,
		Expiry:       time.Now().Add(-time.Second),
	})
	tok, err := ts.Token()
	if err != nil {
		return vault.Record{}, fmt.Errorf("linear: refresh: %w", err)
	}
	return vault.Record{
		AccessToken:     tok.AccessToken,
		RefreshToken:    tok.RefreshToken,
		Expiry:          tok.Expiry,
		Scopes:          rec.Scopes,
		AllowedProjects: rec.AllowedProjects,
	}, nil
}

func (c *LinearConnector) oauthCtx(ctx context.Context) context.Context {
	if c.httpClient != nil {
		return context.WithValue(ctx, oauth2.HTTPClient, c.httpClient)
	}
	return ctx
}

func linearTokenToRecord(tok *oauth2.Token, allowedProjects []string) vault.Record {
	expiry := tok.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(24 * time.Hour)
	}
	return vault.Record{
		AccessToken:     tok.AccessToken,
		RefreshToken:    tok.RefreshToken,
		Expiry:          expiry,
		AllowedProjects: allowedProjects,
	}
}
