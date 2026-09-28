package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/IniZio/nexus/internal/core/hostbin"
	"github.com/IniZio/nexus/internal/core/hostbin/pin"
)

const kernelPinName = "vmlinux"

// kernelPins returns the kernel pin. Tests may replace this var to inject a fake pin.
var kernelPins func() pin.Pin = hostbin.KernelPin

func activeKernelPin() (pin.Pin, bool) {
	p := kernelPins()
	return p, p.Name != ""
}

// kernelURL constructs the download URL for the given version and goarch.
// When NEXUS_RELEASE_BASE_URL is set it overrides the GitHub URL from the pin.
func kernelURL(version, goarch string) string {
	if base := os.Getenv("NEXUS_RELEASE_BASE_URL"); base != "" {
		suffix := "vmlinux-x86_64"
		if goarch == "arm64" {
			suffix = "vmlinux-aarch64"
		}
		return fmt.Sprintf("%s/v%s/%s", base, version, suffix)
	}
	p, ok := activeKernelPin()
	if !ok {
		return ""
	}
	return p.URL(goarch)
}

// fetchKernelTo downloads from url to dest atomically, verifying against expectedSHA.
// client nil → 10-minute default. Cleans up the temp file on any error.
func fetchKernelTo(ctx context.Context, client *http.Client, url, dest, expectedSHA string) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("kernel fetch: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "vmlinux-*.tmp")
	if err != nil {
		return fmt.Errorf("kernel fetch: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("kernel fetch: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("kernel fetch: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		tmp.Close()
		return &UsageError{Msg: fmt.Sprintf(
			"kernel asset not found (HTTP 404): %s\n  run: nexus kernel install", url,
		)}
	}
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return fmt.Errorf("kernel fetch: GET %s: HTTP %d", url, resp.StatusCode)
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("kernel fetch: download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("kernel fetch: close: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != expectedSHA {
		return fmt.Errorf("kernel fetch: sha256 mismatch (want %s, got %s)", expectedSHA, got)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("kernel fetch: install %s: %w", dest, err)
	}
	success = true
	return nil
}

// autoFetchKernelXDG downloads the pinned kernel to xdgKernelPath using client.
// client nil → default. Returns the installed path on success.
func autoFetchKernelXDG(ctx context.Context, client *http.Client) (string, error) {
	goarch := runtime.GOARCH
	p, ok := activeKernelPin()
	if !ok {
		return "", fmt.Errorf("kernel: no pin for %q; set NEXUS_KERNEL_PATH or run `nexus kernel install`", kernelPinName)
	}
	wantSHA, err := p.SourceSHA256(goarch)
	if err != nil {
		return "", fmt.Errorf("kernel: no pinned asset for arch %s; set NEXUS_KERNEL_PATH or run `nexus kernel install`", goarch)
	}
	url := kernelURL(p.Version, goarch)
	if url == "" {
		return "", fmt.Errorf("kernel: no download URL for arch %s; set NEXUS_KERNEL_PATH or run `nexus kernel install`", goarch)
	}
	dest := xdgKernelPath()
	if dest == "" {
		return "", fmt.Errorf("kernel: cannot determine home directory")
	}
	slog.Default().Info("kernel: not found locally; downloading pinned release",
		"version", p.Version, "arch", goarch, "dest", dest)
	if err := fetchKernelTo(ctx, client, url, dest, wantSHA); err != nil {
		return "", err
	}
	return dest, nil
}
