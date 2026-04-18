package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReleaseBaseURLPrefersExplicitOverride(t *testing.T) {
	t.Setenv("NEXUS_RELEASE_BASE_URL", "https://example.test/download")
	t.Setenv("NEXUS_RELEASE_CHANNEL", "prerelease")
	got := releaseBaseURL()
	if got != "https://example.test/download" {
		t.Fatalf("expected explicit base URL, got %s", got)
	}
}

func TestReleaseBaseURLUsesPrereleaseChannel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/inizio/nexus/releases" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[
			{"tag_name":"v1.4.0-rc.1","prerelease":true,"draft":false},
			{"tag_name":"v1.3.0","prerelease":false,"draft":false}
		]`))
	}))
	defer server.Close()

	origAPIBase := githubReleaseAPIBaseURL
	t.Cleanup(func() {
		githubReleaseAPIBaseURL = origAPIBase
	})
	githubReleaseAPIBaseURL = server.URL
	t.Setenv("NEXUS_RELEASE_CHANNEL", "prerelease")
	t.Setenv("NEXUS_RELEASE_REPO", "inizio/nexus")
	t.Setenv("NEXUS_RELEASE_BASE_URL", "")

	got := releaseBaseURL()
	want := "https://github.com/inizio/nexus/releases/download/v1.4.0-rc.1"
	if got != want {
		t.Fatalf("expected prerelease URL %s, got %s", want, got)
	}
}

func TestChannelDefaults(t *testing.T) {
	t.Setenv("NEXUS_RELEASE_CHANNEL", "")
	t.Setenv("NEXUS_RELEASE_REPO", "")
	if got := channelName(); got != "stable" {
		t.Fatalf("expected default stable channel, got %s", got)
	}
	if got := channelRepo(); got != "inizio/nexus" {
		t.Fatalf("expected default repo, got %s", got)
	}
}
