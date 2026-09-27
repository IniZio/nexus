package connectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func TestLinearConnectorContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "lin_test_access",
			"refresh_token": "lin_test_refresh",
			"token_type":    "Bearer",
			"expires_in":    86400,
		})
	}))
	defer srv.Close()

	ep := oauth2.Endpoint{
		AuthURL:  srv.URL + "/oauth/authorize",
		TokenURL: srv.URL + "/oauth/token",
	}
	c := newLinearWithEndpoint("test-client-id", "test-secret", srv.URL+"/callback", ep, srv.Client())
	vaulttest.RunConnectorContract(t, c)
}

func TestLinearExchangeNilAllowedProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "lin_token",
			"expires_in":   86400,
		})
	}))
	defer srv.Close()

	ep := oauth2.Endpoint{
		AuthURL:  srv.URL + "/auth",
		TokenURL: srv.URL + "/token",
	}
	c := newLinearWithEndpoint("cid", "csec", srv.URL+"/cb", ep, srv.Client())
	rec, err := c.Exchange(t.Context(), "code", "verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if rec.AllowedProjects != nil {
		t.Errorf("connector must not set AllowedProjects; got %v", rec.AllowedProjects)
	}
}

func TestLinearRefreshRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "lin_refreshed",
			"refresh_token": "lin_new_refresh",
			"expires_in":    86400,
		})
	}))
	defer srv.Close()

	ep := oauth2.Endpoint{
		AuthURL:  srv.URL + "/oauth/authorize",
		TokenURL: srv.URL + "/oauth/token",
	}
	c := newLinearWithEndpoint("cid", "csec", srv.URL+"/cb", ep, srv.Client())

	old := vault.Record{
		AccessToken:     "old_token",
		RefreshToken:    "old_refresh",
		Expiry:          time.Now().Add(-time.Minute),
		AllowedProjects: []string{"proj1"},
	}
	rec, err := c.Refresh(t.Context(), old)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if rec.AccessToken != "lin_refreshed" {
		t.Errorf("refreshed token mismatch: got %q", rec.AccessToken)
	}
}
