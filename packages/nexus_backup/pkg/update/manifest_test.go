package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchManifestWithSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	manifest := ReleaseManifest{
		SchemaVersion:         1,
		Version:               "1.2.3",
		PublishedAt:           "2026-01-01T00:00:00Z",
		MinimumUpdaterVersion: "1.0.0",
		Artifacts: map[string]TargetBuild{
			"darwin-arm64": {
				URLBase: "https://example.com",
				CLI:     BinaryArtifact{Name: "nexus-darwin-arm64", SHA256: "abc"},
				Daemon:  BinaryArtifact{Name: "nexus-daemon-darwin-arm64", SHA256: "def"},
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	signature := ed25519.Sign(priv, manifestBytes)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release-manifest.json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(manifestBytes)
		case "/release-manifest.sig":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(signature)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	got, err := fetchManifest(context.Background(), Options{
		ReleaseBaseURL:  server.URL,
		PublicKeyBase64: base64.StdEncoding.EncodeToString(pub),
	})
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	if got.Version != "1.2.3" {
		t.Fatalf("expected version 1.2.3, got %s", got.Version)
	}
}

func TestCompareVersion(t *testing.T) {
	if compareVersion("1.2.3", "1.2.4") >= 0 {
		t.Fatalf("expected 1.2.3 < 1.2.4")
	}
	if compareVersion("v2.0.0", "1.9.9") <= 0 {
		t.Fatalf("expected v2.0.0 > 1.9.9")
	}
	if compareVersion("1.0.0", "1.0.0") != 0 {
		t.Fatalf("expected equality")
	}
}
