package mitm_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/config"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
)

// configPolicyYAML is the egress block a real project ships in
// .nexus/config.yaml (shape taken from hanlun-lms). It yields GENERIC pattern
// policies (HostPolicy.Patterns, GitHub == nil) — the product path that PX-3
// and PX-5 proved 403'd public archive GETs and `gh auth status`.
const configPolicyYAML = `
version: 1
egress:
  policy:
    - host: api.github.com
      paths:
        - "/repos/acme/myrepo"
        - "/repos/acme/myrepo/**"
        - "/user"
    - host: github.com
      paths:
        - "/acme/myrepo/**"
  secrets:
    - env: GH_TOKEN
      hosts: [github.com, api.github.com]
`

// configToDomainPathPolicies mirrors cli.buildWorktreeEgressArgs step 1
// (egressAddHostPolicy): every egress.policy entry lands under the wildcard
// placeholder key "" as an EgressHostPolicy{Paths}. Both functions are
// unexported in internal/cli, which this package cannot import from a test.
func configToDomainPathPolicies(cfg config.Config) domain.EgressPathPolicies {
	pp := domain.EgressPathPolicies{"": map[string]domain.EgressHostPolicy{}}
	for _, p := range cfg.Egress.Policy {
		if len(p.Paths) > 0 {
			pp[""][strings.ToLower(p.Host)] = domain.EgressHostPolicy{Paths: p.Paths}
		}
	}
	return pp
}

// domainToMITMPathPolicies mirrors service.buildMITMPathPolicies (unexported in
// internal/core/service, which imports this package): raw globs compiled via
// mitm.CompileGlobPattern, GitHub pointer carried over when set.
func domainToMITMPathPolicies(t *testing.T, pp domain.EgressPathPolicies) mitm.PathPolicies {
	t.Helper()
	out := make(mitm.PathPolicies, len(pp))
	for placeholder, hostMap := range pp {
		hm := make(map[string]mitm.HostPolicy, len(hostMap))
		for host, pol := range hostMap {
			mp := mitm.HostPolicy{}
			if pol.GitHub != nil {
				mp.GitHub = &mitm.GitHubPolicy{Owner: pol.GitHub.Owner, Name: pol.GitHub.Name}
			}
			for _, pat := range pol.Paths {
				gp, err := mitm.CompileGlobPattern(pat)
				if err != nil {
					t.Fatalf("CompileGlobPattern(%q): %v", pat, err)
				}
				mp.Patterns = append(mp.Patterns, gp)
			}
			hm[host] = mp
		}
		out[placeholder] = hm
	}
	return out
}

// newConfigPatternProxy builds a proxy whose GitHub policies come from
// configPolicyYAML via config.Parse and the create-time conversions above —
// no AllowedRepo shim, so pol.GitHub is nil for both hosts.
func newConfigPatternProxy(t *testing.T, upstreamAddr string) (proxyURL string, recGH, recAPI cred.PlaceholderRecord) {
	t.Helper()
	cfg, err := config.Parse([]byte(configPolicyYAML))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	if len(cfg.Egress.Policy) != 2 {
		t.Fatalf("config.Parse: want 2 egress.policy entries, got %d", len(cfg.Egress.Policy))
	}
	policies := domainToMITMPathPolicies(t, configToDomainPathPolicies(cfg))
	for _, h := range []string{"github.com", "api.github.com"} {
		if pol := policies[""][h]; pol.GitHub != nil || len(pol.Patterns) == 0 {
			t.Fatalf("policy for %s: want pattern policy (GitHub == nil, Patterns > 0), got %+v", h, pol)
		}
	}

	const realToken = "ghp_real_secret_token"
	broker := cred.NewBroker()
	sid := newSandboxID(37)
	recGH, err = broker.RegisterPlaceholder(sid, "github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder github.com: %v", err)
	}
	recAPI, err = broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder api.github.com: %v", err)
	}
	srv := newTestProxy(t, mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"github.com", "api.github.com"},
		Broker:       broker,
		PathPolicies: policies,
	}, upstreamAddr)
	return srv.URL, recGH, recAPI
}

func doProxied(t *testing.T, client *http.Client, method, rawURL, placeholder, body string) int {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if placeholder != "" {
		req.Header.Set("Authorization", "Bearer "+placeholder)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	return resp.StatusCode
}

// TestConfigPatternPolicy_GitHubCarveOutsAllowed is the PX-3/PX-5 regression:
// under generic config-derived pattern policies (not the built-in AllowedRepo
// policy) the host-scoped carve-outs must still fire.
//
// Mutation evidence: remove the gitHubHostCarveOut case from the path-policy
// switch in New → every case here is 403 and upstream receives nothing.
func TestConfigPatternPolicy_GitHubCarveOutsAllowed(t *testing.T) {
	t.Parallel()
	upstream, authCh := captureAuthUpstream(t)
	proxyURL, recGH, recAPI := newConfigPatternProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxyURL)

	cases := []struct {
		name, method, url, placeholder, body, wantUpstreamAuth string
	}{
		{"archive GET foreign repo, credential stripped", http.MethodGet,
			"http://github.com/pgpartman/pg_partman/archive/refs/tags/v5.2.4.zip", recGH.Placeholder, "", ""},
		{"archive GET foreign repo, unauthenticated (wget)", http.MethodGet,
			"http://github.com/pgpartman/pg_partman/archive/refs/tags/v5.2.4.zip", "", "", ""},
		{"release asset HEAD foreign repo", http.MethodHead,
			"http://github.com/other/tool/releases/download/v1.0.0/tool_linux_amd64.tar.gz", recGH.Placeholder, "", ""},
		{"api root GET", http.MethodGet,
			"http://api.github.com/", recAPI.Placeholder, "", "Bearer ghp_real_secret_token"},
		{"viewer-login GraphQL (gh auth status)", http.MethodPost,
			"http://api.github.com/graphql", recAPI.Placeholder,
			`{"query":"query UserCurrent{viewer{login}}"}`, "Bearer ghp_real_secret_token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := doProxied(t, client, tc.method, tc.url, tc.placeholder, tc.body); got != http.StatusOK {
				t.Fatalf("want 200, got %d", got)
			}
			got, ok := receiveOrTimeout(authCh)
			if !ok {
				t.Fatalf("upstream never received request")
			}
			if got != tc.wantUpstreamAuth {
				t.Errorf("upstream Authorization = %q, want %q", got, tc.wantUpstreamAuth)
			}
		})
	}
}

// TestConfigPatternPolicy_GitHubCarveOutsStayNarrow pins the carve-outs to
// exactly the public-read class under config-derived pattern policies: POST
// to an archive path, smart-HTTP on a foreign repo, any non-root API path
// outside the pattern, and every other GraphQL document remain 403 with no
// upstream hit.
//
// Mutation evidence: make gitHubHostCarveOut return true for any github.com
// path → the receive-pack case reaches upstream; drop the
// isGitHubTokenValidationQuery gate in the GraphQL handler → the mutation
// case reaches upstream.
func TestConfigPatternPolicy_GitHubCarveOutsStayNarrow(t *testing.T) {
	t.Parallel()
	upstream, authCh := captureAuthUpstream(t)
	proxyURL, recGH, recAPI := newConfigPatternProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxyURL)

	cases := []struct {
		name, method, url, placeholder, body string
	}{
		{"POST archive", http.MethodPost,
			"http://github.com/pgpartman/pg_partman/archive/refs/tags/v5.2.4.zip", recGH.Placeholder, ""},
		{"foreign receive-pack", http.MethodPost,
			"http://github.com/pgpartman/pg_partman.git/git-receive-pack", recGH.Placeholder, ""},
		{"foreign info/refs", http.MethodGet,
			"http://github.com/pgpartman/pg_partman.git/info/refs", recGH.Placeholder, ""},
		{"foreign repo metadata", http.MethodGet,
			"http://api.github.com/repos/pgpartman/pg_partman", recAPI.Placeholder, ""},
		{"GraphQL mutation", http.MethodPost,
			"http://api.github.com/graphql", recAPI.Placeholder,
			`{"query":"mutation{deleteRepository(input:{repositoryId:\"x\"}){clientMutationId}}"}`},
		{"GraphQL viewer with extra fields", http.MethodPost,
			"http://api.github.com/graphql", recAPI.Placeholder,
			`{"query":"query{viewer{login repositories(first:100){nodes{name}}}}"}`},
		{"GraphQL viewer-login via GET", http.MethodGet,
			"http://api.github.com/graphql?query=%7Bviewer%7Blogin%7D%7D", recAPI.Placeholder, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := doProxied(t, client, tc.method, tc.url, tc.placeholder, tc.body); got != http.StatusForbidden {
				t.Fatalf("want 403, got %d", got)
			}
			if got, ok := receiveOrTimeout(authCh); ok {
				t.Errorf("upstream received denied request (Authorization=%q)", got)
			}
		})
	}
}

// TestConfigPatternPolicy_PatternsStillEnforced confirms the carve-outs sit
// beside the config patterns rather than replacing them: the repo's own
// pattern paths keep working and keep swapping the credential.
func TestConfigPatternPolicy_PatternsStillEnforced(t *testing.T) {
	t.Parallel()
	upstream, authCh := captureAuthUpstream(t)
	proxyURL, recGH, recAPI := newConfigPatternProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxyURL)

	for _, tc := range []struct{ method, url, placeholder string }{
		{http.MethodGet, "http://api.github.com/repos/acme/myrepo/pulls", recAPI.Placeholder},
		{http.MethodGet, "http://api.github.com/user", recAPI.Placeholder},
		{http.MethodGet, "http://github.com/acme/myrepo/pull/1", recGH.Placeholder},
	} {
		if got := doProxied(t, client, tc.method, tc.url, tc.placeholder, ""); got != http.StatusOK {
			t.Fatalf("%s %s: want 200, got %d", tc.method, tc.url, got)
		}
		if got, _ := receiveOrTimeout(authCh); got != "Bearer ghp_real_secret_token" {
			t.Errorf("%s %s: upstream Authorization = %q, want swapped real token", tc.method, tc.url, got)
		}
	}
}
