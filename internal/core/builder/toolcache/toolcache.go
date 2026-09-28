package toolcache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/IniZio/nexus/internal/core/hostbin/pin"
)

// GHVersion is the pinned version of the GitHub CLI baked into every sandbox.
const GHVersion = "2.101.0"

// SkipDirective placed anywhere in a Containerfile opts the image out of
// host-provided sandbox tools.
const SkipDirective = "nexus:sandbox-tools-skip"

// BuilderTreeDir is the directory inside the builder VM rootfs where the
// staged tool tree is placed during image construction.
const BuilderTreeDir = "/opt/nexus-sandbox-tools/root"

// ErrChecksumMismatch is returned when a downloaded tarball's sha256 does not
// match the pinned value.
var ErrChecksumMismatch = errors.New("toolcache: sha256 mismatch")

// ErrUnsupportedArch is returned when no pin exists for the requested GOARCH.
var ErrUnsupportedArch = errors.New("toolcache: no pinned sha256 for arch")

// Tool describes a host-downloaded, guest-injected binary tool.
type Tool struct {
	pin.Pin           // Name, Version, URLTemplate, SHA256ByGoArch, ArchiveMember, etc.
	InstallDir string // guest dir, placeholder {VERSION} allowed
	LinkPath   string // guest symlink path, e.g. "/usr/local/bin/gh"
}

// GH returns a fresh Tool describing the pinned gh CLI release.
func GH() Tool {
	return Tool{
		Pin: pin.Pin{
			Name:        "gh",
			Version:     GHVersion,
			URLTemplate: "https://github.com/cli/cli/releases/download/v{VERSION}/gh_{VERSION}_linux_{GOARCH}.tar.gz",
			SHA256ByGoArch: map[string]string{
				"amd64": "9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8",
				"arm64": "b57e8063f18862647c9d22727c32e9da1b963f8bf9db648fe123a6975695640f",
			},
			ArchiveMember: "gh_{VERSION}_linux_{GOARCH}/bin/gh",
		},
		InstallDir: "/usr/local/share/nexus-tools/gh/{VERSION}",
		LinkPath:   "/usr/local/bin/gh",
	}
}

// Defaults returns the default set of tools injected into every sandbox.
func Defaults() []Tool {
	return []Tool{GH()}
}

// Fetched describes a tool binary that has been downloaded and verified on the host.
type Fetched struct {
	Name         string
	Version      string
	GoArch       string
	SHA256       string // verified tarball sha256 (lowercase hex)
	BinPath      string // host path of the verified, extracted, 0755 binary
	GuestBinPath string // InstallDir (expanded) + "/bin/" + Name
	LinkPath     string // e.g. /usr/local/bin/gh
}

// Fetcher downloads and caches tool binaries under Root.
type Fetcher struct {
	Root   string       // cache root, callers pass <storeRoot>/tools
	Client *http.Client // nil => &http.Client{Timeout: 2*time.Minute}
}

func (f Fetcher) httpClient() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

// Fetch returns the verified binary for tool t on the given GOARCH.
// Cache layout: <Root>/<name>/<sha256>/<name>.
func (f Fetcher) Fetch(ctx context.Context, t Tool, goarch string) (Fetched, error) {
	sha, ok := t.SHA256ByGoArch[goarch]
	if !ok || sha == "" {
		return Fetched{}, fmt.Errorf("%w: arch=%s tool=%s", ErrUnsupportedArch, goarch, t.Name)
	}

	// Cache hit: <Root>/<name>/<sha256>/<name>
	cachePath := filepath.Join(f.Root, t.Name, sha, t.Name)
	if info, err := os.Stat(cachePath); err == nil && info.Mode().IsRegular() {
		return makeFetched(t, goarch, sha, cachePath), nil
	}

	if err := os.MkdirAll(f.Root, 0755); err != nil {
		return Fetched{}, fmt.Errorf("toolcache: mkdir root: %w", err)
	}

	url := t.URL(goarch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Fetched{}, fmt.Errorf("toolcache: build request: %w", err)
	}

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return Fetched{}, fmt.Errorf("toolcache: GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Fetched{}, fmt.Errorf("toolcache: GET %s: status %s", url, resp.Status)
	}

	tmp, err := os.CreateTemp(f.Root, "dl-*")
	if err != nil {
		return Fetched{}, fmt.Errorf("toolcache: create temp: %w", err)
	}
	tmpName := tmp.Name()

	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	tmp.Close()

	if copyErr != nil {
		os.Remove(tmpName)
		return Fetched{}, fmt.Errorf("toolcache: download: %w", copyErr)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != sha {
		os.Remove(tmpName)
		return Fetched{}, fmt.Errorf("%w: want=%s got=%s", ErrChecksumMismatch, sha, got)
	}

	binPath, err := extractMember(f.Root, t, goarch, sha, tmpName)
	os.Remove(tmpName)
	if err != nil {
		return Fetched{}, err
	}

	return makeFetched(t, goarch, sha, binPath), nil
}

var maxMemberBytes int64 = 512 << 20

// extractMember opens tarPath, locates the expected member, writes it to a
// temp dir, then atomically renames the temp dir to <Root>/<name>/<sha256>.
func extractMember(root string, t Tool, goarch, sha, tarPath string) (string, error) {
	fh, err := os.Open(tarPath)
	if err != nil {
		return "", fmt.Errorf("toolcache: open tarball: %w", err)
	}
	defer fh.Close()

	gr, err := gzip.NewReader(fh)
	if err != nil {
		return "", fmt.Errorf("toolcache: gzip reader: %w", err)
	}
	defer gr.Close()

	target := t.Member(goarch)
	tr := tar.NewReader(gr)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("toolcache: tar read: %w", err)
		}
		if hdr.Name != target {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return "", fmt.Errorf("toolcache: member %s is not a regular file", target)
		}
		if hdr.Size > maxMemberBytes {
			return "", fmt.Errorf("toolcache: member %s too large (%d bytes)", target, hdr.Size)
		}

		tmpDir, err := os.MkdirTemp(root, "extract-*")
		if err != nil {
			return "", fmt.Errorf("toolcache: mkdirtemp: %w", err)
		}

		tmpBin := filepath.Join(tmpDir, t.Name)
		bf, err := os.OpenFile(tmpBin, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			os.RemoveAll(tmpDir)
			return "", fmt.Errorf("toolcache: create binary: %w", err)
		}
		_, writeErr := io.Copy(bf, tr)
		bf.Close()
		if writeErr != nil {
			os.RemoveAll(tmpDir)
			return "", fmt.Errorf("toolcache: write binary: %w", writeErr)
		}

		destDir := filepath.Join(root, t.Name, sha)
		parentDir := filepath.Dir(destDir)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			os.RemoveAll(tmpDir)
			return "", fmt.Errorf("toolcache: mkdir parent: %w", err)
		}

		if err := os.Rename(tmpDir, destDir); err != nil {
			if _, statErr := os.Stat(destDir); statErr == nil {
				os.RemoveAll(tmpDir)
				return filepath.Join(destDir, t.Name), nil
			}
			os.RemoveAll(tmpDir)
			return "", fmt.Errorf("toolcache: rename cache dir: %w", err)
		}

		return filepath.Join(destDir, t.Name), nil
	}

	return "", fmt.Errorf("toolcache: member %s not found in archive", target)
}

func makeFetched(t Tool, goarch, sha256sum, binPath string) Fetched {
	installDir := pin.Expand(t.InstallDir, t.Version, goarch)
	return Fetched{
		Name:         t.Name,
		Version:      t.Version,
		GoArch:       goarch,
		SHA256:       sha256sum,
		BinPath:      binPath,
		GuestBinPath: installDir + "/bin/" + t.Name,
		LinkPath:     t.LinkPath,
	}
}

// Digest returns the first 16 hex characters of the sha256 over all tools,
// sorted by name, formatted as "name|version|goarch|sha256\n" per line.
// Returns "" when tools is empty.
func Digest(tools []Fetched) string {
	if len(tools) == 0 {
		return ""
	}
	lines := make([]string, len(tools))
	for i, t := range tools {
		lines[i] = t.Name + "|" + t.Version + "|" + t.GoArch + "|" + t.SHA256
	}
	sort.Strings(lines)

	h := sha256.New()
	for _, l := range lines {
		fmt.Fprintf(h, "%s\n", l)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// SkippedBy reports whether containerfile contains SkipDirective.
func SkippedBy(containerfile []byte) bool {
	return bytes.Contains(containerfile, []byte(SkipDirective))
}
