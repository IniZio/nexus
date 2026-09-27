package connectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"

	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func TestGitHubConnectorContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "test-device-code",
				"user_code":        "TEST-CODE",
				"verification_uri": "https://github.com/login/device",
				"expires_in":       900,
				"interval":         0,
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "ghs_test_token",
				"refresh_token": "ghr_test_refresh",
				"expires_in":    28800,
				"scope":         "repo,read:user",
			})
		}
	}))
	defer srv.Close()

	ep := oauth2.Endpoint{
		DeviceAuthURL: srv.URL + "/login/device/code",
		TokenURL:      srv.URL + "/login/oauth/access_token",
	}
	c := newGitHubWithEndpoint("test-client-id", ep, srv.Client())
	vaulttest.RunConnectorContract(t, c)
}

func TestGitHubDevicePollSlowDown(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := calls.Add(1)
		if n == 1 {
			json.NewEncoder(w).Encode(map[string]any{"error": "slow_down"})
			return
		}
		if n == 2 {
			json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "ghs_slow_token",
			"refresh_token": "ghr_slow_refresh",
			"expires_in":    28800,
			"scope":         "repo",
		})
	}))
	defer srv.Close()

	ep := oauth2.Endpoint{
		DeviceAuthURL: srv.URL + "/device/code",
		TokenURL:      srv.URL + "/token",
	}
	c := newGitHubWithEndpoint("test-client-id", ep, srv.Client())
	ctx := t.Context()

	rec, err := c.PollDevice(ctx, "dev-code")
	if err != ErrSlowDown {
		t.Fatalf("first poll: want ErrSlowDown, got err=%v rec=%+v", err, rec)
	}

	_, err = c.PollDevice(ctx, "dev-code")
	if err != ErrAuthorizationPending {
		t.Fatalf("second poll: want ErrAuthorizationPending, got %v", err)
	}

	rec, err = c.PollDevice(ctx, "dev-code")
	if err != nil {
		t.Fatalf("third poll: unexpected error: %v", err)
	}
	if rec.AccessToken != "ghs_slow_token" {
		t.Errorf("access token mismatch: got %q", rec.AccessToken)
	}
}

func TestGitHubPollDeviceNilAllowedProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "ghs_token",
			"refresh_token": "ghr_refresh",
			"expires_in":    28800,
			"scope":         "repo",
		})
	}))
	defer srv.Close()

	ep := oauth2.Endpoint{
		DeviceAuthURL: srv.URL + "/device",
		TokenURL:      srv.URL + "/token",
	}
	c := newGitHubWithEndpoint("cid", ep, srv.Client())
	rec, err := c.PollDevice(t.Context(), "dev-code")
	if err != nil {
		t.Fatalf("PollDevice: %v", err)
	}
	if rec.AllowedProjects != nil {
		t.Errorf("connector must not set AllowedProjects; got %v", rec.AllowedProjects)
	}
}

func TestGitHubDefaultClientIDOverridable(t *testing.T) {
	defaultConn := NewGitHub("")
	if defaultConn.cfg.ClientID != DefaultGitHubClientID {
		t.Errorf("expected default client ID %q, got %q", DefaultGitHubClientID, defaultConn.cfg.ClientID)
	}

	custom := "my-custom-app-id"
	overrideConn := NewGitHub(custom)
	if overrideConn.cfg.ClientID != custom {
		t.Errorf("expected overridden client ID %q, got %q", custom, overrideConn.cfg.ClientID)
	}
}
