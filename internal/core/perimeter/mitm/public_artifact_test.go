package mitm_test

import (
	"io"
	"net/http"
	"testing"
)

// TestF14_PublicArtifactAllowedWithoutCredential verifies that GET/HEAD on
// /<owner>/<repo>/archive/* and /<owner>/<repo>/releases/download/* pass the
// github.com path policy for a repo OTHER than AllowedRepo, and that the
// GH_TOKEN placeholder is stripped: upstream sees no Authorization header.
//
// Mutation evidence: remove the isGitHubPublicArtifactPath case from
// gitHubPathAllowed → 403 and upstream receives nothing. Remove the strip
// branch in the swap handler → upstream receives "Bearer ghp_real_secret_token".
func TestF14_PublicArtifactAllowedWithoutCredential(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, recGH, _, _ := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/other/repo/archive/refs/tags/v1.2.3.zip"},
		{http.MethodGet, "/other/repo/archive/v1.2.3.tar.gz"},
		{http.MethodHead, "/other/repo/archive/refs/heads/main.zip"},
		{http.MethodGet, "/other/repo/releases/download/v1.2.3/tool_linux_amd64.tar.gz"},
		{http.MethodGet, "/acme/myrepo/archive/refs/tags/v0.1.0.zip"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, "http://github.com"+tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+recGH.Placeholder)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("want 200 (allowed), got %d", resp.StatusCode)
			}
			got, ok := receiveOrTimeout(authCh)
			if !ok {
				t.Fatalf("upstream never received request")
			}
			if got != "" {
				t.Errorf("upstream Authorization = %q, want none (credential must not be forwarded)", got)
			}
		})
	}
}

// TestF14_PublicArtifactDeniedShapes verifies the carve-out is exactly the
// public read class: POST to an archive path, non-artifact paths on a foreign
// repo, and smart-HTTP on a foreign repo all remain 403 with no upstream hit.
//
// Mutation evidence: drop the method check in isGitHubPublicArtifactPath →
// the POST case reaches upstream. Change gitHubPathAllowed to ignore
// owner/repo for smart-HTTP → the git-receive-pack case reaches upstream.
func TestF14_PublicArtifactDeniedShapes(t *testing.T) {
	t.Parallel()

	upstream, authCh := captureAuthUpstream(t)
	proxy, recGH, _, _ := newGitHubAllowedRepoProxy(t, upstream.Listener.Addr().String())
	client := proxyClient(proxy.URL)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/other/repo/archive/refs/tags/v1.2.3.zip"},
		{http.MethodPost, "/other/repo/releases/download/v1.2.3/tool.tar.gz"},
		{http.MethodGet, "/other/repo/archive"},
		{http.MethodGet, "/other/repo/archive/"},
		{http.MethodGet, "/other/repo/releases/download"},
		{http.MethodGet, "/other/repo/releases/download/"},
		{http.MethodGet, "/other/repo/releases/tag/v1.2.3"},
		{http.MethodGet, "/other/repo/releases"},
		{http.MethodGet, "/other/repo"},
		{http.MethodGet, "/archive/x"},
		{http.MethodGet, "/other/repo.git/info/refs"},
		{http.MethodPost, "/other/repo.git/git-upload-pack"},
		{http.MethodPost, "/other/repo.git/git-receive-pack"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, "http://github.com"+tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+recGH.Placeholder)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("want 403 (denied), got %d", resp.StatusCode)
			}
			if got, ok := receiveOrTimeout(authCh); ok {
				t.Errorf("upstream received denied request (Authorization=%q)", got)
			}
		})
	}
}
