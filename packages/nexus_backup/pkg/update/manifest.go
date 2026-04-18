package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
)

func fetchManifest(ctx context.Context, opts Options) (ReleaseManifest, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.ReleaseBaseURL), "/")
	if base == "" {
		return ReleaseManifest{}, fmt.Errorf("missing release base URL")
	}
	manifestURL := base + "/release-manifest.json"
	manifestBytes, err := fetchBytes(ctx, manifestURL)
	if err != nil {
		return ReleaseManifest{}, err
	}
	sigURL := base + "/release-manifest.sig"
	if strings.TrimSpace(opts.PublicKeyBase64) != "" {
		signatureText, sigErr := fetchBytes(ctx, sigURL)
		if sigErr != nil {
			return ReleaseManifest{}, fmt.Errorf("fetch manifest signature: %w", sigErr)
		}
		if err := verifyManifestSignature(opts.PublicKeyBase64, manifestBytes, strings.TrimSpace(string(signatureText))); err != nil {
			return ReleaseManifest{}, err
		}
	} else {
		return ReleaseManifest{}, fmt.Errorf("manifest public key is required")
	}

	var manifest ReleaseManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return ReleaseManifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return ReleaseManifest{}, fmt.Errorf("unsupported manifest schema version %d", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return ReleaseManifest{}, fmt.Errorf("manifest version is empty")
	}
	return manifest, nil
}

func verifyManifestSignature(publicKeyB64 string, manifestBytes []byte, signatureB64 string) error {
	pubKeyRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyB64))
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	signatureRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signatureB64))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if len(pubKeyRaw) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid manifest public key size")
	}
	if len(signatureRaw) != ed25519.SignatureSize {
		return fmt.Errorf("invalid manifest signature size")
	}
	if !ed25519.Verify(ed25519.PublicKey(pubKeyRaw), manifestBytes, signatureRaw) {
		return fmt.Errorf("manifest signature verification failed")
	}
	return nil
}

func fetchBytes(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, rawURL)
	}
	return io.ReadAll(resp.Body)
}

func currentTargetKey() string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	if goarch == "x86_64" {
		goarch = "amd64"
	}
	return goos + "-" + goarch
}

func buildArtifactURL(base, name string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + name
	return u.String(), nil
}

func sha256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func compareVersion(a, b string) int {
	normalize := func(s string) []int {
		s = strings.TrimSpace(strings.TrimPrefix(s, "v"))
		parts := strings.SplitN(s, "-", 2)
		core := parts[0]
		chunks := strings.Split(core, ".")
		out := make([]int, 0, len(chunks))
		for _, c := range chunks {
			n, err := strconv.Atoi(c)
			if err != nil {
				out = append(out, 0)
				continue
			}
			out = append(out, n)
		}
		for len(out) < 3 {
			out = append(out, 0)
		}
		return out[:3]
	}
	av := normalize(a)
	bv := normalize(b)
	for i := 0; i < 3; i += 1 {
		if av[i] < bv[i] {
			return -1
		}
		if av[i] > bv[i] {
			return 1
		}
	}
	return 0
}
