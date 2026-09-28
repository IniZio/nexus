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
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/IniZio/nexus/internal/core/hostbin/pin"
	"github.com/klauspost/compress/zstd"
)

// helpers

func mustHexSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func mustZstd(data []byte) []byte {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		panic(err)
	}
	return enc.EncodeAll(data, nil)
}

func makeTarGz(member string, content []byte) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     member,
		Size:     int64(len(content)),
		Mode:     0o755,
	})
	_, _ = tw.Write(content)
	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

func makeZip(member string, content []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(member)
	_, _ = w.Write(content)
	_ = zw.Close()
	return buf.Bytes()
}

// bufHandler implements slog.Handler writing to a bytes.Buffer.
type bufHandler struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

func (h *bufHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *bufHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(h.buf, "%s %s", r.Level, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(h.buf, " %s=%v", a.Key, a.Value)
		return true
	})
	h.buf.WriteByte('\n')
	return nil
}
func (h *bufHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *bufHandler) WithGroup(_ string) slog.Handler      { return h }

func newBufLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(&bufHandler{buf: buf}), buf
}

// --- Tests ---

func TestEnvVar(t *testing.T) {
	cases := []struct{ name, want string }{
		{"cloud-hypervisor", "NEXUS_CLOUD_HYPERVISOR_PATH"},
		{"virtiofsd", "NEXUS_VIRTIOFSD_PATH"},
		{"mke2fs", "NEXUS_MKE2FS_PATH"},
	}
	for _, c := range cases {
		if got := EnvVar(c.name); got != c.want {
			t.Errorf("EnvVar(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDefaultDataDir_XDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	got := DefaultDataDir()
	if got != "/xdg/data/nexus" {
		t.Errorf("DefaultDataDir() = %q, want /xdg/data/nexus", got)
	}
}

func TestResolve_UnknownName(t *testing.T) {
	r := &Resolver{
		DataDir: t.TempDir(),
		GoArch:  "amd64",
		Pins:    map[string]pin.Pin{},
		Getenv:  func(string) string { return "" },
	}
	_, err := r.Resolve(context.Background(), "nosuchbin")
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("want ErrUnknown, got %v", err)
	}
}

func TestResolve_EnvBeatsEmbedded(t *testing.T) {
	// Write a real file to point the env at.
	dir := t.TempDir()
	binPath := filepath.Join(dir, "mybin")
	if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	data := []byte("fake binary content")
	sha := mustHexSHA256(data)
	embFS := fstest.MapFS{
		"mybin.zst": {Data: mustZstd(data)},
	}
	r := &Resolver{
		DataDir:  t.TempDir(),
		GoArch:   "amd64",
		Embedded: embFS,
		Pins: map[string]pin.Pin{
			"mybin": {
				Name: "mybin", Version: "1",
				SHA256ByGoArch: map[string]string{"amd64": sha},
			},
		},
		Getenv: func(key string) string {
			if key == EnvVar("mybin") {
				return binPath
			}
			return ""
		},
	}
	res, err := r.Resolve(context.Background(), "mybin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceEnv {
		t.Errorf("source = %s, want env", res.Source)
	}
	if res.Path != binPath {
		t.Errorf("path = %q, want %q", res.Path, binPath)
	}
}

func TestResolve_EnvMissingFile(t *testing.T) {
	r := &Resolver{
		DataDir: t.TempDir(),
		GoArch:  "amd64",
		Pins:    map[string]pin.Pin{"mybin": {Name: "mybin"}},
		Getenv: func(key string) string {
			if key == EnvVar("mybin") {
				return "/does/not/exist/mybin"
			}
			return ""
		},
	}
	_, err := r.Resolve(context.Background(), "mybin")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestResolve_EmbeddedBeatsDownload(t *testing.T) {
	data := []byte("embedded binary")
	sha := mustHexSHA256(data)

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	embFS := fstest.MapFS{
		"mybin.zst": {Data: mustZstd(data)},
	}
	r := &Resolver{
		DataDir:  t.TempDir(),
		GoArch:   "amd64",
		Embedded: embFS,
		Pins: map[string]pin.Pin{
			"mybin": {
				Name: "mybin", Version: "1",
				URLTemplate:    srv.URL + "/mybin",
				SHA256ByGoArch: map[string]string{"amd64": sha},
			},
		},
		Client: srv.Client(),
		Getenv: func(string) string { return "" },
	}
	res, err := r.Resolve(context.Background(), "mybin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceEmbedded {
		t.Errorf("source = %s, want embedded", res.Source)
	}
	if n := atomic.LoadInt64(&hits); n != 0 {
		t.Errorf("server hit %d times, want 0", n)
	}
}

func TestResolve_DownloadWhenEmbeddedIsPlaceholder(t *testing.T) {
	data := []byte("downloaded binary content")
	sha := mustHexSHA256(data)

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	// Only PLACEHOLDER in embedded FS — no .zst file.
	embFS := fstest.MapFS{
		"PLACEHOLDER": {Data: []byte("no artifacts embedded")},
	}
	dataDir := t.TempDir()
	r := &Resolver{
		DataDir:  dataDir,
		GoArch:   "amd64",
		Embedded: embFS,
		Pins: map[string]pin.Pin{
			"mybin": {
				Name: "mybin", Version: "1",
				URLTemplate:    srv.URL + "/mybin",
				SHA256ByGoArch: map[string]string{"amd64": sha},
			},
		},
		Client: srv.Client(),
		Getenv: func(string) string { return "" },
		LookPath: func(string) (string, error) {
			return "", fmt.Errorf("not in PATH")
		},
	}
	res, err := r.Resolve(context.Background(), "mybin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceDownload {
		t.Errorf("source = %s, want download", res.Source)
	}
	if atomic.LoadInt64(&hits) != 1 {
		t.Errorf("server hit %d times, want 1", atomic.LoadInt64(&hits))
	}
	// File exists at expected path with correct content and mode.
	finalPath := filepath.Join(dataDir, "artifacts", sha, "mybin")
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("final path missing: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("cached content mismatch")
	}
	fi, _ := os.Stat(finalPath)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0755", fi.Mode().Perm())
	}
}

func TestResolve_Idempotent(t *testing.T) {
	data := []byte("stable binary")
	sha := mustHexSHA256(data)
	embFS := fstest.MapFS{
		"mybin.zst": {Data: mustZstd(data)},
	}
	dataDir := t.TempDir()
	r := &Resolver{
		DataDir:  dataDir,
		GoArch:   "amd64",
		Embedded: embFS,
		Pins: map[string]pin.Pin{
			"mybin": {
				Name: "mybin", Version: "1",
				SHA256ByGoArch: map[string]string{"amd64": sha},
			},
		},
		Getenv: func(string) string { return "" },
	}

	res1, err := r.Resolve(context.Background(), "mybin")
	if err != nil {
		t.Fatal(err)
	}
	fi1, _ := os.Stat(res1.Path)

	res2, err := r.Resolve(context.Background(), "mybin")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Source != SourceCache {
		t.Errorf("second resolve source = %s, want cache", res2.Source)
	}
	fi2, _ := os.Stat(res2.Path)
	if !fi1.ModTime().Equal(fi2.ModTime()) || !os.SameFile(fi1, fi2) {
		t.Error("second resolve rewrote the file")
	}

	// No temp files left.
	entries, _ := os.ReadDir(filepath.Dir(res1.Path))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestResolve_EmbeddedSHAMismatch(t *testing.T) {
	data := []byte("real binary")
	wrongSHA := mustHexSHA256([]byte("other content"))
	embFS := fstest.MapFS{
		"mybin.zst": {Data: mustZstd(data)},
	}
	dataDir := t.TempDir()
	r := &Resolver{
		DataDir:  dataDir,
		GoArch:   "amd64",
		Embedded: embFS,
		Pins: map[string]pin.Pin{
			"mybin": {
				Name: "mybin", Version: "1",
				SHA256ByGoArch: map[string]string{"amd64": wrongSHA},
			},
		},
		Getenv: func(string) string { return "" },
	}
	_, err := r.Resolve(context.Background(), "mybin")
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
	// Final path must not exist.
	finalPath := filepath.Join(dataDir, "artifacts", wrongSHA, "mybin")
	if _, statErr := os.Stat(finalPath); statErr == nil {
		t.Error("final path must not exist after mismatch")
	}
	// No temp files.
	artifactsDir := filepath.Join(dataDir, "artifacts", wrongSHA)
	if entries, err := os.ReadDir(artifactsDir); err == nil {
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp-") {
				t.Errorf("temp file left: %s", e.Name())
			}
		}
	}
}

func TestResolve_DownloadSHAMismatch(t *testing.T) {
	data := []byte("real binary")
	wrongSHA := mustHexSHA256([]byte("different"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	r := &Resolver{
		DataDir:  dataDir,
		GoArch:   "amd64",
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
		Pins: map[string]pin.Pin{
			"mybin": {
				Name: "mybin", Version: "1",
				URLTemplate:    srv.URL + "/mybin",
				SHA256ByGoArch: map[string]string{"amd64": wrongSHA},
			},
		},
		Client: srv.Client(),
		Getenv: func(string) string { return "" },
	}
	_, err := r.Resolve(context.Background(), "mybin")
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
	finalPath := filepath.Join(dataDir, "artifacts", wrongSHA, "mybin")
	if _, statErr := os.Stat(finalPath); statErr == nil {
		t.Error("final path must not exist after download mismatch")
	}
}

func TestResolve_TarGzPin(t *testing.T) {
	member := "bin/mytool"
	binary := []byte("targz binary content")
	binarySHA := mustHexSHA256(binary)
	archive := makeTarGz(member, binary)
	archiveSHA := mustHexSHA256(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	r := &Resolver{
		DataDir:  dataDir,
		GoArch:   "amd64",
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
		Pins: map[string]pin.Pin{
			"mytool": {
				Name:          "mytool",
				Version:       "1",
				URLTemplate:   srv.URL + "/mytool.tar.gz",
				ArchiveMember: member,
				SHA256ByGoArch: map[string]string{
					"amd64": archiveSHA,
				},
				BinarySHA256ByGoArch: map[string]string{
					"amd64": binarySHA,
				},
			},
		},
		Client: srv.Client(),
		Getenv: func(string) string { return "" },
		LookPath: func(string) (string, error) {
			return "", fmt.Errorf("not in PATH")
		},
	}
	res, err := r.Resolve(context.Background(), "mytool")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceDownload {
		t.Errorf("source = %s, want download", res.Source)
	}
	got, _ := os.ReadFile(res.Path)
	if !bytes.Equal(got, binary) {
		t.Error("extracted binary content mismatch")
	}
}

func TestResolve_ZipPin(t *testing.T) {
	member := "dist/zipbin"
	binary := []byte("zip binary content")
	binarySHA := mustHexSHA256(binary)
	archive := makeZip(member, binary)
	archiveSHA := mustHexSHA256(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	r := &Resolver{
		DataDir:  dataDir,
		GoArch:   "amd64",
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
		Pins: map[string]pin.Pin{
			"zipbin": {
				Name:          "zipbin",
				Version:       "1",
				URLTemplate:   srv.URL + "/zipbin.zip",
				ArchiveMember: member,
				SHA256ByGoArch: map[string]string{
					"amd64": archiveSHA,
				},
				BinarySHA256ByGoArch: map[string]string{
					"amd64": binarySHA,
				},
			},
		},
		Client: srv.Client(),
		Getenv: func(string) string { return "" },
		LookPath: func(string) (string, error) {
			return "", fmt.Errorf("not in PATH")
		},
	}
	res, err := r.Resolve(context.Background(), "zipbin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceDownload {
		t.Errorf("source = %s, want download", res.Source)
	}
	got, _ := os.ReadFile(res.Path)
	if !bytes.Equal(got, binary) {
		t.Error("extracted binary content mismatch")
	}
}

func TestResolve_UnavailablePin_FallsBackToPath(t *testing.T) {
	log, buf := newBufLogger()
	pathBin := filepath.Join(t.TempDir(), "noarchbin")
	_ = os.WriteFile(pathBin, []byte("path bin"), 0o755)

	r := &Resolver{
		DataDir:  t.TempDir(),
		GoArch:   "amd64",
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
		Pins: map[string]pin.Pin{
			// No amd64 sha → BinarySHA256 returns ErrUnsupportedArch.
			"noarchbin": {Name: "noarchbin", Version: "1"},
		},
		Logger: log,
		Getenv: func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			if name == "noarchbin" {
				return pathBin, nil
			}
			return "", fmt.Errorf("not found")
		},
	}
	res, err := r.Resolve(context.Background(), "noarchbin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourcePath {
		t.Errorf("source = %s, want path", res.Source)
	}
	if !strings.Contains(buf.String(), "falling back to PATH") {
		t.Errorf("log missing 'falling back to PATH'; got: %s", buf.String())
	}
}

func TestResolve_DownloadFailure_FallsBackToPath(t *testing.T) {
	data := []byte("binary")
	sha := mustHexSHA256(data)
	log, buf := newBufLogger()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	pathBin := filepath.Join(t.TempDir(), "failbin")
	_ = os.WriteFile(pathBin, []byte("path binary"), 0o755)

	r := &Resolver{
		DataDir:  t.TempDir(),
		GoArch:   "amd64",
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
		Pins: map[string]pin.Pin{
			"failbin": {
				Name: "failbin", Version: "1",
				URLTemplate:    srv.URL + "/failbin",
				SHA256ByGoArch: map[string]string{"amd64": sha},
			},
		},
		Client: srv.Client(),
		Logger: log,
		Getenv: func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			if name == "failbin" {
				return pathBin, nil
			}
			return "", fmt.Errorf("not found")
		},
	}
	res, err := r.Resolve(context.Background(), "failbin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourcePath {
		t.Errorf("source = %s, want path", res.Source)
	}
	if !strings.Contains(buf.String(), "falling back to PATH") {
		t.Errorf("log missing 'falling back to PATH'; got: %s", buf.String())
	}
}

// Ensure fs.FS is satisfied by fstest.MapFS (compile-time check).
var _ fs.FS = fstest.MapFS{}
