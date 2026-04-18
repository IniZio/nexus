package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestForceUpdateSuccessFlow(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tempRoot)

	cliCurrent := filepath.Join(tempRoot, "bin", "nexus")
	daemonCurrent := filepath.Join(tempRoot, "bin", "nexus-daemon")
	if err := os.MkdirAll(filepath.Dir(cliCurrent), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cliCurrent, []byte("old-cli"), 0o755); err != nil {
		t.Fatalf("write cli: %v", err)
	}
	if err := os.WriteFile(daemonCurrent, []byte("old-daemon"), 0o755); err != nil {
		t.Fatalf("write daemon: %v", err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keys: %v", err)
	}
	var manifestJSON []byte
	var signature string
	server := testReleaseServer(&manifestJSON, &signature, map[string][]byte{
		"/nexus-" + currentTargetKey():        []byte("#!/usr/bin/env bash\nexit 0\n"),
		"/nexus-daemon-" + currentTargetKey(): []byte("#!/usr/bin/env bash\nexit 0\n"),
	})
	defer server.Close()
	manifestJSON, signature = signedManifestFixture(t, priv, "1.1.0", server.URL)

	origResolve := resolveBinaryPathsFn
	origRestart := restartAndProbeDaemonFn
	origStop := stopRunningDaemonFn
	t.Cleanup(func() {
		resolveBinaryPathsFn = origResolve
		restartAndProbeDaemonFn = origRestart
		stopRunningDaemonFn = origStop
	})
	resolveBinaryPathsFn = func() (string, string, error) {
		return cliCurrent, daemonCurrent, nil
	}
	restartAndProbeDaemonFn = func(ctx context.Context, expectedVersion string) (bool, error) {
		return true, nil
	}
	stopRunningDaemonFn = func(port int) error {
		return nil
	}

	result, err := ForceUpdate(context.Background(), Options{
		ReleaseBaseURL:     server.URL,
		PublicKeyBase64:    base64.StdEncoding.EncodeToString(pub),
		CheckInterval:      0,
		BadVersionCooldown: 24 * time.Hour,
		CurrentVersion:     "1.0.0",
		CurrentUpdater:     "1.0.0",
		AutoApply:          true,
		Force:              true,
	})
	if err != nil {
		t.Fatalf("force update: %v", err)
	}
	if !result.Updated {
		t.Fatalf("expected update to be applied")
	}
	cliData, _ := os.ReadFile(cliCurrent)
	daemonData, _ := os.ReadFile(daemonCurrent)
	if string(cliData) == "old-cli" || string(daemonData) == "old-daemon" {
		t.Fatalf("expected binaries to be replaced")
	}
}

func TestForceUpdateRollbackOnHealthFailure(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tempRoot)

	cliCurrent := filepath.Join(tempRoot, "bin", "nexus")
	daemonCurrent := filepath.Join(tempRoot, "bin", "nexus-daemon")
	_ = os.MkdirAll(filepath.Dir(cliCurrent), 0o755)
	_ = os.WriteFile(cliCurrent, []byte("old-cli"), 0o755)
	_ = os.WriteFile(daemonCurrent, []byte("old-daemon"), 0o755)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var manifestJSON []byte
	var signature string
	server := testReleaseServer(&manifestJSON, &signature, map[string][]byte{
		"/nexus-" + currentTargetKey():        []byte("#!/usr/bin/env bash\nexit 0\n"),
		"/nexus-daemon-" + currentTargetKey(): []byte("#!/usr/bin/env bash\nexit 0\n"),
	})
	defer server.Close()
	manifestJSON, signature = signedManifestFixture(t, priv, "1.2.0", server.URL)

	origResolve := resolveBinaryPathsFn
	origRestart := restartAndProbeDaemonFn
	origStop := stopRunningDaemonFn
	t.Cleanup(func() {
		resolveBinaryPathsFn = origResolve
		restartAndProbeDaemonFn = origRestart
		stopRunningDaemonFn = origStop
	})
	resolveBinaryPathsFn = func() (string, string, error) {
		return cliCurrent, daemonCurrent, nil
	}
	restartAndProbeDaemonFn = func(ctx context.Context, expectedVersion string) (bool, error) {
		if expectedVersion == "1.2.0" {
			return false, nil
		}
		return true, nil
	}
	stopRunningDaemonFn = func(port int) error {
		return nil
	}

	_, err := ForceUpdate(context.Background(), Options{
		ReleaseBaseURL:     server.URL,
		PublicKeyBase64:    base64.StdEncoding.EncodeToString(pub),
		CheckInterval:      0,
		BadVersionCooldown: 24 * time.Hour,
		CurrentVersion:     "1.0.0",
		CurrentUpdater:     "1.0.0",
		AutoApply:          true,
		Force:              true,
	})
	if err == nil {
		t.Fatalf("expected update failure due to daemon health")
	}
	cliData, _ := os.ReadFile(cliCurrent)
	daemonData, _ := os.ReadFile(daemonCurrent)
	if string(cliData) != "old-cli" || string(daemonData) != "old-daemon" {
		t.Fatalf("expected rollback to restore previous binaries")
	}
}

func signedManifestFixture(t *testing.T, priv ed25519.PrivateKey, version, baseURL string) ([]byte, string) {
	target := currentTargetKey()
	manifest := ReleaseManifest{
		SchemaVersion:         1,
		Version:               version,
		PublishedAt:           "2026-04-01T00:00:00Z",
		MinimumUpdaterVersion: "1.0.0",
		Artifacts: map[string]TargetBuild{
			target: {
				URLBase: baseURL,
				CLI: BinaryArtifact{
					Name:   "nexus-" + target,
					SHA256: sha256Bytes([]byte("#!/usr/bin/env bash\nexit 0\n")),
				},
				Daemon: BinaryArtifact{
					Name:   "nexus-daemon-" + target,
					SHA256: sha256Bytes([]byte("#!/usr/bin/env bash\nexit 0\n")),
				},
			},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	data = append(data, '\n')
	signature := ed25519.Sign(priv, data)
	return data, base64.StdEncoding.EncodeToString(signature)
}

func testReleaseServer(manifest *[]byte, signature *string, artifacts map[string][]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release-manifest.json":
			_, _ = w.Write(*manifest)
		case "/release-manifest.sig":
			_, _ = w.Write([]byte(*signature))
		default:
			body, ok := artifacts[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(body)
		}
	}))
}
