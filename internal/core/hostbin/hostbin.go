// Package hostbin resolves pinned host executables from env override,
// embedded zstd artifact, pinned download, or PATH — in that order.
// Artifacts are linked at build time by blank-importing internal/core/hostbin/embedded.
package hostbin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/hostbin/pin"
	"github.com/klauspost/compress/zstd"
)

var (
	ErrChecksumMismatch = errors.New("hostbin: sha256 mismatch")
	ErrUnknown          = errors.New("hostbin: unknown host binary")
	ErrNotFound         = errors.New("hostbin: host binary not found")
)

// embeddedFS holds artifacts registered by package hostbin/embedded's init.
var embeddedFS fs.FS = emptyFS{}

// emptyFS is an fs.FS with no files.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// RegisterEmbedded sets the package-level embedded FS.
// Called by package hostbin/embedded's init; host binaries blank-import that package.
func RegisterEmbedded(f fs.FS) { embeddedFS = f }

// Source identifies how a binary was located.
type Source string

const (
	SourceEnv      Source = "env"
	SourceCache    Source = "cache"
	SourceEmbedded Source = "embedded"
	SourceDownload Source = "download"
	SourcePath     Source = "path"
)

// Resolved is the result of a successful Resolve call.
type Resolved struct {
	Name   string
	Path   string
	Source Source
}

// Resolver locates pinned host executables via a configurable resolution chain.
type Resolver struct {
	DataDir  string                       // "" => DefaultDataDir(); artifacts at <DataDir>/artifacts/<binsha256>/<name>
	GoArch   string                       // "" => runtime.GOARCH
	Pins     map[string]pin.Pin           // nil => Pins()
	Embedded fs.FS                        // nil => embeddedFS
	Client   *http.Client                 // nil => &http.Client{Timeout: 5*time.Minute}
	Getenv   func(string) string          // nil => os.Getenv
	LookPath func(string) (string, error) // nil => exec.LookPath
	Logger   *slog.Logger                 // nil => slog.Default()
}

const maxArtifactBytes = 512 << 20 // 512 MiB safety cap

// DefaultDataDir returns $XDG_DATA_HOME/nexus or $HOME/.local/share/nexus.
func DefaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "nexus")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".local", "share", "nexus")
}

// EnvVar returns the override env var name for a binary.
// e.g. "cloud-hypervisor" → "NEXUS_CLOUD_HYPERVISOR_PATH".
func EnvVar(name string) string {
	return "NEXUS_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_PATH"
}

func (r *Resolver) goarch() string {
	if r.GoArch != "" {
		return r.GoArch
	}
	return runtime.GOARCH
}

func (r *Resolver) dataDir() string {
	if r.DataDir != "" {
		return r.DataDir
	}
	return DefaultDataDir()
}

func (r *Resolver) pins() map[string]pin.Pin {
	if r.Pins != nil {
		return r.Pins
	}
	return Pins()
}

func (r *Resolver) embedded() fs.FS {
	if r.Embedded != nil {
		return r.Embedded
	}
	return embeddedFS
}

func (r *Resolver) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (r *Resolver) getenv(key string) string {
	if r.Getenv != nil {
		return r.Getenv(key)
	}
	return os.Getenv(key)
}

func (r *Resolver) lookPath(name string) (string, error) {
	if r.LookPath != nil {
		return r.LookPath(name)
	}
	return exec.LookPath(name)
}

func (r *Resolver) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Resolve locates the named binary using the resolution chain described in the
// package doc: env → cache → embedded → download → PATH.
func (r *Resolver) Resolve(ctx context.Context, name string) (Resolved, error) {
	goarch := r.goarch()
	log := r.logger()

	// 1. Env override — must point to a regular file; no fallthrough on bad path.
	if v := r.getenv(EnvVar(name)); v != "" {
		fi, err := os.Stat(v)
		if err != nil || !fi.Mode().IsRegular() {
			return Resolved{}, fmt.Errorf("%w: %s=%q (stat: %v)", ErrNotFound, EnvVar(name), v, err)
		}
		return Resolved{Name: name, Path: v, Source: SourceEnv}, nil
	}

	// 2. Pin lookup.
	p, ok := r.pins()[name]
	if !ok {
		return Resolved{}, fmt.Errorf("%w: %s", ErrUnknown, name)
	}

	// 3. BinarySHA — if arch has no pin, skip directly to PATH.
	binSHA, err := p.BinarySHA256(goarch)
	if err != nil {
		return r.fallbackToPath(name, log, "no pin for arch")
	}

	// 4. Cache hit — never rewrite, just return.
	finalPath := filepath.Join(r.dataDir(), "artifacts", binSHA, name)
	if fi, err := os.Stat(finalPath); err == nil && fi.Mode().IsRegular() {
		return Resolved{Name: name, Path: finalPath, Source: SourceCache}, nil
	}

	// 5. Embedded zstd artifact.
	emb := r.embedded()
	if ef, err := emb.Open(name + ".zst"); err == nil {
		ef.Close()
		data, err := decompressEmbedded(emb, name, binSHA)
		if err != nil {
			return Resolved{}, err // includes ErrChecksumMismatch — never fall through
		}
		if err := atomicWrite(finalPath, data); err != nil {
			return Resolved{}, fmt.Errorf("hostbin: write %s: %w", name, err)
		}
		return Resolved{Name: name, Path: finalPath, Source: SourceEmbedded}, nil
	}

	// 6. Download — checksum mismatch is fatal; network errors fall through to PATH.
	if p.Downloadable(goarch) {
		data, err := FetchVerified(ctx, r.client(), p, goarch)
		if err != nil {
			if errors.Is(err, ErrChecksumMismatch) {
				return Resolved{}, err
			}
			log.Warn("hostbin: download failed, falling back to PATH", "name", name, "err", err)
		} else {
			if err := atomicWrite(finalPath, data); err != nil {
				return Resolved{}, fmt.Errorf("hostbin: write %s: %w", name, err)
			}
			return Resolved{Name: name, Path: finalPath, Source: SourceDownload}, nil
		}
	}

	// 7. PATH last resort.
	return r.fallbackToPath(name, log, "tried env, embedded, download")
}

func (r *Resolver) fallbackToPath(name string, log *slog.Logger, tried string) (Resolved, error) {
	p, err := r.lookPath(name)
	if err == nil {
		log.Warn("hostbin: falling back to PATH", "name", name, "path", p)
		return Resolved{Name: name, Path: p, Source: SourcePath}, nil
	}
	return Resolved{}, fmt.Errorf("%w: %s (%s, PATH)", ErrNotFound, name, tried)
}

// Resolve is a convenience wrapper using a zero-value Resolver.
func Resolve(ctx context.Context, name string) (string, error) {
	res, err := (&Resolver{}).Resolve(ctx, name)
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// FetchVerified downloads p's artifact for goarch, verifies the source
// checksum, extracts the binary if needed, and verifies the binary checksum.
// Also used by the `make artifacts` generator.
func FetchVerified(ctx context.Context, client *http.Client, p pin.Pin, goarch string) ([]byte, error) {
	url := p.URL(goarch)
	if url == "" {
		return nil, fmt.Errorf("hostbin: no URL for %s/%s", p.Name, goarch)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("hostbin: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hostbin: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hostbin: GET %s: status %d", url, resp.StatusCode)
	}

	h := sha256.New()
	raw, err := io.ReadAll(io.TeeReader(io.LimitReader(resp.Body, maxArtifactBytes), h))
	if err != nil {
		return nil, fmt.Errorf("hostbin: read %s: %w", url, err)
	}

	srcSHA, err := p.SourceSHA256(goarch)
	if err != nil {
		return nil, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != srcSHA {
		return nil, fmt.Errorf("%w: %s source: got %s want %s", ErrChecksumMismatch, p.Name, got, srcSHA)
	}

	var binary []byte
	switch p.Format(goarch) {
	case pin.FormatRaw:
		binary = raw
	case pin.FormatTarGz:
		binary, err = extractTarGz(raw, p.Member(goarch))
		if err != nil {
			return nil, err
		}
	case pin.FormatZip:
		binary, err = extractZip(raw, p.Member(goarch))
		if err != nil {
			return nil, err
		}
	}

	// Verify binary sha for archive formats (raw sha == source sha, already checked).
	if p.ArchiveMember != "" {
		binSHA, err := p.BinarySHA256(goarch)
		if err != nil {
			return nil, err
		}
		if got := hexSHA256(binary); got != binSHA {
			return nil, fmt.Errorf("%w: %s binary: got %s want %s", ErrChecksumMismatch, p.Name, got, binSHA)
		}
	}

	return binary, nil
}

func decompressEmbedded(emb fs.FS, name, wantSHA string) ([]byte, error) {
	f, err := emb.Open(name + ".zst")
	if err != nil {
		return nil, fmt.Errorf("hostbin: open embedded %s.zst: %w", name, err)
	}
	defer f.Close()

	dec, err := zstd.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("hostbin: zstd reader: %w", err)
	}
	defer dec.Close()

	data, err := io.ReadAll(io.LimitReader(dec, maxArtifactBytes))
	if err != nil {
		return nil, fmt.Errorf("hostbin: decompress %s: %w", name, err)
	}

	if got := hexSHA256(data); got != wantSHA {
		return nil, fmt.Errorf("%w: %s embedded: got %s want %s", ErrChecksumMismatch, name, got, wantSHA)
	}
	return data, nil
}

func extractTarGz(data []byte, member string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("hostbin: gzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("hostbin: tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Name == member {
			return io.ReadAll(io.LimitReader(tr, maxArtifactBytes))
		}
	}
	return nil, fmt.Errorf("hostbin: member %q not found in tar.gz", member)
}

func extractZip(data []byte, member string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("hostbin: zip: %w", err)
	}
	for _, f := range zr.File {
		if f.Name == member {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("hostbin: zip open %q: %w", member, err)
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxArtifactBytes))
		}
	}
	return nil, fmt.Errorf("hostbin: member %q not found in zip", member)
}

func hexSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// atomicWrite writes data to path via a temp file in the same directory.
// The temp file is removed on any error; concurrent identical writes are safe.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}
