package mitm_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
)

type corpusEntry struct {
	Command       string `json:"command"`
	OperationName string `json:"operationName"`
	Body          string `json:"body"`
}

func loadGQLCorpus(t *testing.T) []corpusEntry {
	t.Helper()
	data, err := os.ReadFile("testdata/gh_graphql_corpus.json")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	var entries []corpusEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	return entries
}

const repoInfoJSON = `{"data":{"repository":{"id":"R_kgDOAAAAAQ","name":"nexus","owner":{"login":"IniZio"}}}}`

func newGQLUpstream(t *testing.T, respBody string) (*httptest.Server, <-chan string) {
	t.Helper()
	ch := make(chan string, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case ch <- r.Header.Get("Authorization"):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, respBody) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func newNexusProxy(t *testing.T, upstreamAddr, allowedRepo string, sandboxByte byte) (*httptest.Server, cred.PlaceholderRecord) {
	t.Helper()
	const realToken = "ghp_real_r30_test"
	broker := cred.NewBroker()
	sid := newSandboxID(sandboxByte)
	if _, err := broker.RegisterPlaceholder(sid, "github.com", realToken); err != nil {
		t.Fatalf("RegisterPlaceholder github.com: %v", err)
	}
	recAPI, err := broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder api.github.com: %v", err)
	}
	cfg := mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"github.com", "api.github.com"},
		Broker:       broker,
		AllowedRepo:  allowedRepo,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstreamAddr)
			},
		},
	}
	p, err := mitm.New(cfg)
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv, recAPI
}

func gqlPOST(t *testing.T, client *http.Client, proxyURL, placeholder, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://api.github.com/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+placeholder)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	return resp.StatusCode
}

func TestR30_GQL_CorpusAllowed(t *testing.T) {
	t.Parallel()
	corpus := loadGQLCorpus(t)
	upstream, authCh := newGQLUpstream(t, repoInfoJSON)
	proxy, recAPI := newNexusProxy(t, upstream.Listener.Addr().String(), "IniZio/nexus", 80)
	client := proxyClient(proxy.URL)

	for _, entry := range corpus {
		status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, entry.Body)
		if status != http.StatusOK {
			t.Errorf("[%s] want 200, got %d", entry.OperationName, status)
		}
		got, ok := receiveOrTimeout(authCh)
		if !ok {
			t.Errorf("[%s] upstream never received request (denied?)", entry.OperationName)
		} else if want := "Bearer ghp_real_r30_test"; got != want {
			t.Errorf("[%s] upstream Authorization=%q, want %q (token swap must fire)", entry.OperationName, got, want)
		}
	}
}

func TestR30_GQL_MutationWithoutRepositoryInfo(t *testing.T) {
	t.Parallel()
	corpus := loadGQLCorpus(t)
	var mutationBody string
	for _, e := range corpus {
		if e.OperationName == "PullRequestCreate" {
			mutationBody = e.Body
			break
		}
	}
	if mutationBody == "" {
		t.Fatal("PullRequestCreate not in corpus")
	}

	upstream, authCh := newGQLUpstream(t, repoInfoJSON)
	proxy, recAPI := newNexusProxy(t, upstream.Listener.Addr().String(), "IniZio/nexus", 81)
	client := proxyClient(proxy.URL)

	if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, mutationBody); status != http.StatusForbidden {
		t.Errorf("mutation without RepositoryInfo: want 403, got %d", status)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("upstream received denied mutation (Authorization=%q)", got)
	}
}

func TestR30_GQL_WrongRepo(t *testing.T) {
	t.Parallel()
	corpus := loadGQLCorpus(t)
	upstream, authCh := newGQLUpstream(t, repoInfoJSON)
	proxy, recAPI := newNexusProxy(t, upstream.Listener.Addr().String(), "acme/other", 82)
	client := proxyClient(proxy.URL)

	for _, entry := range corpus {
		switch entry.OperationName {
		case "RepositoryInfo", "PullRequestCreate", "PullRequestSearch", "PullRequestStatus":
		default:
			continue
		}
		if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, entry.Body); status != http.StatusForbidden {
			t.Errorf("[%s] acme/other proxy: want 403, got %d", entry.OperationName, status)
		}
		if got, ok := receiveOrTimeout(authCh); ok {
			t.Errorf("[%s] upstream received denied request (Authorization=%q)", entry.OperationName, got)
		}
	}
}

func TestR30_GQL_MethodPath(t *testing.T) {
	t.Parallel()
	upstream, authCh := newGQLUpstream(t, repoInfoJSON)
	proxy, recAPI := newNexusProxy(t, upstream.Listener.Addr().String(), "IniZio/nexus", 83)
	client := proxyClient(proxy.URL)

	cases := []struct {
		method string
		url    string
		body   string
	}{
		{http.MethodGet, "http://api.github.com/graphql?query=%7Bviewer%7Blogin%7D%7D", ""},
		{http.MethodPost, "http://api.github.com/graphql/foo", `{"query":"{viewer{login}}"}`},
	}
	for _, tc := range cases {
		var bodyReader io.Reader
		if tc.body != "" {
			bodyReader = strings.NewReader(tc.body)
		}
		req, _ := http.NewRequest(tc.method, tc.url, bodyReader)
		req.Header.Set("Authorization", "Bearer "+recAPI.Placeholder)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.url, err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s: want 403, got %d", tc.method, tc.url, resp.StatusCode)
		}
		if got, ok := receiveOrTimeout(authCh); ok {
			t.Errorf("%s %s: upstream received denied request (Authorization=%q)", tc.method, tc.url, got)
		}
	}
}

func TestR30_GQL_BatchDenied(t *testing.T) {
	t.Parallel()
	upstream, authCh := newGQLUpstream(t, repoInfoJSON)
	proxy, recAPI := newNexusProxy(t, upstream.Listener.Addr().String(), "IniZio/nexus", 84)
	client := proxyClient(proxy.URL)

	body := `[{"query":"{viewer{login}}"},{"query":"query RepositoryInfo($owner:String!,$name:String!){repository(owner:$owner,name:$name){id}}","variables":{"owner":"IniZio","name":"nexus"}}]`
	if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, body); status != http.StatusForbidden {
		t.Errorf("batch body: want 403, got %d", status)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("batch body: upstream received request (Authorization=%q)", got)
	}
}

func TestR30_GQL_SmuggledRoot(t *testing.T) {
	t.Parallel()
	upstream, authCh := newGQLUpstream(t, repoInfoJSON)
	proxy, recAPI := newNexusProxy(t, upstream.Listener.Addr().String(), "IniZio/nexus", 85)
	client := proxyClient(proxy.URL)

	body := `{"query":"query{repository(owner:\"IniZio\",name:\"nexus\"){id} evil:repository(owner:\"evil\",name:\"x\"){id}}"}`
	if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, body); status != http.StatusForbidden {
		t.Errorf("smuggled root: want 403, got %d", status)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("smuggled root: upstream received request (Authorization=%q)", got)
	}
}

func newPatternProxy(t *testing.T, upstreamAddr string, patterns []string, sandboxByte byte) (*httptest.Server, cred.PlaceholderRecord) {
	t.Helper()
	const realToken = "ghp_real_r30_pat"
	broker := cred.NewBroker()
	sid := newSandboxID(sandboxByte)
	if _, err := broker.RegisterPlaceholder(sid, "github.com", realToken); err != nil {
		t.Fatalf("RegisterPlaceholder github.com: %v", err)
	}
	recAPI, err := broker.RegisterPlaceholder(sid, "api.github.com", realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder api.github.com: %v", err)
	}
	var pats []mitm.GlobPattern
	for _, p := range patterns {
		gp, compErr := mitm.CompileGlobPattern(p)
		if compErr != nil {
			t.Fatalf("CompileGlobPattern(%q): %v", p, compErr)
		}
		pats = append(pats, gp)
	}
	pp := mitm.PathPolicies{
		"": {
			"api.github.com": mitm.HostPolicy{Patterns: pats},
			"github.com":     mitm.HostPolicy{Patterns: pats},
		},
	}
	cfg := mitm.Config{
		SandboxID:    sid,
		AllowedHosts: []string{"github.com", "api.github.com"},
		Broker:       broker,
		PathPolicies: pp,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstreamAddr)
			},
		},
	}
	p, err := mitm.New(cfg)
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv, recAPI
}

func TestR30_GQL_PathPolicies(t *testing.T) {
	t.Parallel()
	corpus := loadGQLCorpus(t)
	var repoInfoBody, mutationBody string
	for _, e := range corpus {
		if e.OperationName == "RepositoryInfo" && repoInfoBody == "" {
			repoInfoBody = e.Body
		}
		if e.OperationName == "PullRequestCreate" && mutationBody == "" {
			mutationBody = e.Body
		}
	}
	if repoInfoBody == "" || mutationBody == "" {
		t.Fatal("corpus missing RepositoryInfo or PullRequestCreate")
	}

	t.Run("writable pattern admits reads and mutation after warm", func(t *testing.T) {
		t.Parallel()
		upstream, authCh := newGQLUpstream(t, repoInfoJSON)
		proxy, recAPI := newPatternProxy(t, upstream.Listener.Addr().String(),
			[]string{"/repos/IniZio/nexus/**", "GET /user"}, 86)
		client := proxyClient(proxy.URL)

		if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, repoInfoBody); status != http.StatusOK {
			t.Fatalf("RepositoryInfo: want 200, got %d", status)
		}
		receiveOrTimeout(authCh)

		if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, mutationBody); status != http.StatusOK {
			t.Errorf("mutation with writable pattern: want 200, got %d", status)
		}
	})

	t.Run("read-only pattern denies mutation after warm", func(t *testing.T) {
		t.Parallel()
		upstream, authCh := newGQLUpstream(t, repoInfoJSON)
		proxy, recAPI := newPatternProxy(t, upstream.Listener.Addr().String(),
			[]string{"GET /repos/IniZio/nexus/**"}, 87)
		client := proxyClient(proxy.URL)

		if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, repoInfoBody); status != http.StatusOK {
			t.Fatalf("RepositoryInfo with read-only pattern: want 200, got %d", status)
		}
		receiveOrTimeout(authCh)

		if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, mutationBody); status != http.StatusForbidden {
			t.Errorf("mutation with read-only pattern: want 403, got %d", status)
		}
		if got, ok := receiveOrTimeout(authCh); ok {
			t.Errorf("read-only pattern: upstream received mutation (Authorization=%q)", got)
		}
	})
}

func TestR30_GQL_WrongCachedID(t *testing.T) {
	t.Parallel()
	corpus := loadGQLCorpus(t)
	var repoInfoBody, mutationBody string
	for _, e := range corpus {
		if e.OperationName == "RepositoryInfo" && repoInfoBody == "" {
			repoInfoBody = e.Body
		}
		if e.OperationName == "PullRequestCreate" && mutationBody == "" {
			mutationBody = e.Body
		}
	}
	if repoInfoBody == "" || mutationBody == "" {
		t.Fatal("corpus missing required entries")
	}

	wrongIDJSON := `{"data":{"repository":{"id":"R_other","name":"nexus","owner":{"login":"IniZio"}}}}`
	upstream, authCh := newGQLUpstream(t, wrongIDJSON)
	proxy, recAPI := newNexusProxy(t, upstream.Listener.Addr().String(), "IniZio/nexus", 88)
	client := proxyClient(proxy.URL)

	if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, repoInfoBody); status != http.StatusOK {
		t.Fatalf("RepositoryInfo: want 200, got %d", status)
	}
	receiveOrTimeout(authCh)

	if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, mutationBody); status != http.StatusForbidden {
		t.Errorf("wrong cached ID: want 403 (R_kgDOAAAAAQ not in cache), got %d", status)
	}
	if got, ok := receiveOrTimeout(authCh); ok {
		t.Errorf("wrong cached ID: upstream received mutation (Authorization=%q)", got)
	}
}

func TestR30_GQL_ViewerLoginAllowed(t *testing.T) {
	t.Parallel()
	upstream, authCh := newGQLUpstream(t, `{"data":{"viewer":{"login":"IniZio"}}}`)
	proxy, recAPI := newNexusProxy(t, upstream.Listener.Addr().String(), "IniZio/nexus", 89)
	client := proxyClient(proxy.URL)

	body := `{"query":"query UserCurrent{viewer{login}}"}`
	if status := gqlPOST(t, client, proxy.URL, recAPI.Placeholder, body); status != http.StatusOK {
		t.Errorf("viewer{login}: want 200, got %d", status)
	}
	got, ok := receiveOrTimeout(authCh)
	if !ok {
		t.Errorf("viewer{login}: upstream never received request")
	} else if want := "Bearer ghp_real_r30_test"; got != want {
		t.Errorf("viewer{login}: upstream Authorization=%q, want %q", got, want)
	}
}
