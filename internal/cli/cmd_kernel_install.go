package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	Register(Command{
		Name:    "kernel install",
		Summary: "Download and install the guest kernel image from a release",
		Run:     runKernelInstall,
	})
}

func runKernelInstall(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("kernel install", flag.ContinueOnError)
	flagVersion := fs.String("version", version, "release version to download (e.g. 1.2.3)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: err.Error()}
	}

	baseURL := os.Getenv("NEXUS_RELEASE_BASE_URL")
	if baseURL == "" {
		baseURL = "https://github.com/IniZio/nexus/releases/download"
	}

	ver := *flagVersion
	if strings.HasSuffix(ver, "-dev") && os.Getenv("NEXUS_RELEASE_BASE_URL") == "" && ver == version {
		return &UsageError{Msg: "kernel install: --version is required for dev builds (or set NEXUS_RELEASE_BASE_URL)"}
	}

	tag := ver
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}

	kernelURL := fmt.Sprintf("%s/%s/vmlinux-x86_64", baseURL, tag)
	sha256URL := fmt.Sprintf("%s/%s/vmlinux-x86_64.sha256", baseURL, tag)

	expectedHash, err := downloadSHA256(sha256URL)
	if err != nil {
		return err
	}

	dest := xdgKernelPath()
	if dest == "" {
		return fmt.Errorf("kernel install: cannot determine home directory")
	}

	if existingHash, err := fileSHA256(dest); err == nil && existingHash == expectedHash {
		fmt.Fprintln(out.Stdout(), "kernel already installed and up to date")
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("kernel install: cannot create directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), "vmlinux-*.tmp")
	if err != nil {
		return fmt.Errorf("kernel install: cannot create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	downloadErr := func() error {
		resp, err := http.Get(kernelURL) //nolint:noctx
		if err != nil {
			return fmt.Errorf("kernel install: download failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return &UsageError{Msg: fmt.Sprintf(
				"kernel install: release asset not found (HTTP 404): %s\n  run: nexus kernel install --version <tag>",
				kernelURL,
			)}
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("kernel install: unexpected HTTP status %d for %s", resp.StatusCode, kernelURL)
		}
		_, err = io.Copy(tmp, resp.Body)
		return err
	}()

	if closeErr := tmp.Close(); closeErr != nil && downloadErr == nil {
		downloadErr = fmt.Errorf("kernel install: close temp file: %w", closeErr)
	}
	if downloadErr != nil {
		os.Remove(tmpPath)
		return downloadErr
	}

	gotHash, err := fileSHA256(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("kernel install: cannot hash downloaded file: %w", err)
	}
	if gotHash != expectedHash {
		os.Remove(tmpPath)
		return fmt.Errorf("kernel install: sha256 mismatch (want %s, got %s)", expectedHash, gotHash)
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("kernel install: cannot install kernel: %w", err)
	}

	fmt.Fprintf(out.Stdout(), "kernel installed: %s\n", dest)
	return nil
}

func downloadSHA256(url string) (string, error) {
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return "", fmt.Errorf("kernel install: cannot fetch sha256: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", &UsageError{Msg: fmt.Sprintf(
			"kernel install: release asset not found (HTTP 404): %s\n  run: nexus kernel install --version <tag>",
			url,
		)}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("kernel install: unexpected HTTP status %d for %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("kernel install: cannot read sha256: %w", err)
	}
	line := strings.TrimSpace(string(body))
	return strings.Fields(line)[0], nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
