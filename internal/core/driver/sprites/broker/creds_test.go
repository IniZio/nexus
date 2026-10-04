package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

const (
	realGH     = "ghp_REALSECRET_0123456789"
	realClaude = "sk-ant-oat-REALSECRET_abc"
)

type seen struct {
	mu   sync.Mutex
	auth []string
}

func (s *seen) add(a string) { s.mu.Lock(); s.auth = append(s.auth, a); s.mu.Unlock() }
func (s *seen) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.auth) == 0 {
		return "<none>"
	}
	return s.auth[len(s.auth)-1]
}

func fixture(t *testing.T, mod func(*CredsConfig)) (*Creds, *seen, *http.Client) {
	t.Helper()
	up := &seen{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.add(r.Header.Get("Authorization"))
		w.WriteHeader(200)
	}))
	t.Cleanup(upstream.Close)

	cfg := CredsConfig{
		SandboxID: domain.NewSandboxID(),
		Secrets: []Secret{
			{Name: "GH_TOKEN", Value: realGH, Hosts: []string{"github.com", "api.github.com"}, GitHubRepo: "acme/widgets"},
			{Name: "CLAUDE_CODE_OAUTH_TOKEN", Value: realClaude, Hosts: []string{"api.anthropic.com"}},
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, n, upstream.Listener.Addr().String())
			},
		},
	}
	if mod != nil {
		mod(&cfg)
	}
	c, err := NewCreds(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.CACertPEM) {
		t.Fatal("bad CA pem")
	}
	pu, _ := url.Parse(srv.URL)
	return c, up, &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
}

func do(t *testing.T, cl *http.Client, url, auth string) (int, error) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

func TestPlaceholdersNeverRealAndNotLeaked(t *testing.T) {
	c, _, _ := fixture(t, nil)
	for name, real := range map[string]string{"GH_TOKEN": realGH, "CLAUDE_CODE_OAUTH_TOKEN": realClaude} {
		ph := c.Env[name]
		if ph == "" || ph == real || strings.Contains(ph, real) {
			t.Fatalf("%s placeholder %q invalid", name, ph)
		}
	}
	for k, v := range c.Env {
		if strings.Contains(v, realGH) || strings.Contains(v, realClaude) {
			t.Fatalf("env %s leaks real token", k)
		}
	}
	for _, r := range []string{realGH, realClaude} {
		if bytes.Contains(c.CACertPEM, []byte(r)) {
			t.Fatal("CA bundle leaks real token")
		}
	}
	if bytes.Contains(c.CACertPEM, []byte("PRIVATE KEY")) {
		t.Fatal("CA bundle contains private key")
	}
}

func TestClaudeBearerSwapped(t *testing.T) {
	c, up, cl := fixture(t, nil)
	code, err := do(t, cl, "https://api.anthropic.com/v1/messages", "Bearer "+c.Env["CLAUDE_CODE_OAUTH_TOKEN"])
	if err != nil || code != 200 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if got := up.last(); got != "Bearer "+realClaude {
		t.Fatalf("upstream auth = %q", got)
	}
}

func TestGitHubSwapTokenAndBasic(t *testing.T) {
	c, up, cl := fixture(t, nil)
	ph := c.Env["GH_TOKEN"]
	code, err := do(t, cl, "https://api.github.com/repos/acme/widgets/pulls", "token "+ph)
	if err != nil || code != 200 {
		t.Fatalf("token: code=%d err=%v", code, err)
	}
	if got := up.last(); got != "token "+realGH {
		t.Fatalf("upstream auth = %q", got)
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+ph))
	code, err = do(t, cl, "https://github.com/acme/widgets.git/info/refs?service=git-upload-pack", basic)
	if err != nil || code != 200 {
		t.Fatalf("basic: code=%d err=%v", code, err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+realGH))
	if got := up.last(); got != want {
		t.Fatalf("upstream auth = %q", got)
	}
}

func TestOtherRepoAndOtherHostGetNoToken(t *testing.T) {
	c, up, cl := fixture(t, nil)
	ph := c.Env["GH_TOKEN"]
	if code, err := do(t, cl, "https://api.github.com/repos/evil/other/pulls", "token "+ph); err == nil && code == 200 {
		t.Fatalf("other repo allowed (code %d)", code)
	}
	if code, err := do(t, cl, "https://example.com/x", "Bearer "+ph); err == nil && code == 200 {
		t.Fatalf("non-secret host allowed (code %d)", code)
	}
	// placeholder presented to the wrong secret host must not resolve
	do(t, cl, "https://api.anthropic.com/x", "Bearer "+ph)
	for _, a := range up.auth {
		if strings.Contains(a, realGH) || strings.Contains(a, realClaude) {
			t.Fatalf("real token reached upstream via wrong scope: %q", a)
		}
	}
}

func TestGitHubWithoutRepoFailsClosed(t *testing.T) {
	_, err := NewCreds(CredsConfig{
		SandboxID: domain.NewSandboxID(),
		Secrets:   []Secret{{Name: "GH_TOKEN", Value: realGH, Hosts: []string{"github.com"}}},
	})
	if err == nil {
		t.Fatal("want error for unbound GitHub secret")
	}
}

func TestRefresherPushesRotationIntoBroker(t *testing.T) {
	store := filepath.Join(t.TempDir(), "claude.json")
	fresh := "sk-ant-oat-STORE_TOKEN"
	if err := cred.SaveStore(store, &cred.DedicatedCredStore{
		AccessToken: fresh, RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour),
		TokenType: "Bearer", ClientID: "c", TokenEndpoint: "https://127.0.0.1:1/token",
	}); err != nil {
		t.Fatal(err)
	}
	c, up, cl := fixture(t, func(cfg *CredsConfig) {
		cfg.RefreshStore, cfg.RefreshSecret = store, "CLAUDE_CODE_OAUTH_TOKEN"
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := do(t, cl, "https://api.anthropic.com/v1/messages", "Bearer "+c.Env["CLAUDE_CODE_OAUTH_TOKEN"]); err != nil {
		t.Fatal(err)
	}
	if got := up.last(); got != "Bearer "+fresh {
		t.Fatalf("upstream auth = %q, want rotated token", got)
	}
}
