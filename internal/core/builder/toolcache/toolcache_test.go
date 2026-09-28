package toolcache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func buildTarball(memberPath string, content []byte) (data []byte, sum string) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{
		Name:     memberPath,
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0755,
	})
	_, _ = tw.Write(content)
	_ = tw.Close()
	_ = gw.Close()
	data = buf.Bytes()
	h := sha256.New()
	h.Write(data)
	return data, hex.EncodeToString(h.Sum(nil))
}

func makeTestTool(serverURL, version, goarch, pinSHA string) Tool {
	return Tool{
		Name:           "gh",
		Version:        version,
		URLTemplate:    serverURL + "/gh_{VERSION}_linux_{GOARCH}.tar.gz",
		SHA256ByGoArch: map[string]string{goarch: pinSHA},
		ArchiveMember:  "gh_{VERSION}_linux_{GOARCH}/bin/gh",
		InstallDir:     "/usr/local/share/nexus-tools/gh/{VERSION}",
		LinkPath:       "/usr/local/bin/gh",
	}
}

func TestFetchHappyPath(t *testing.T) {
	const version = "2.101.0"
	const goarch = "amd64"
	binContent := []byte("fake-gh-binary-content")
	member := "gh_" + version + "_linux_" + goarch + "/bin/gh"
	tarData, sha := buildTarball(member, binContent)

	var reqCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&reqCount, 1)
		_, _ = w.Write(tarData)
	}))
	defer srv.Close()

	root := t.TempDir()
	f := Fetcher{Root: root}
	tool := makeTestTool(srv.URL, version, goarch, sha)

	fetched, err := f.Fetch(context.Background(), tool, goarch)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fetched.Name != "gh" {
		t.Errorf("Name=%q", fetched.Name)
	}
	if fetched.Version != version {
		t.Errorf("Version=%q", fetched.Version)
	}
	if fetched.GoArch != goarch {
		t.Errorf("GoArch=%q", fetched.GoArch)
	}
	if fetched.SHA256 != sha {
		t.Errorf("SHA256=%q want %q", fetched.SHA256, sha)
	}

	wantGuest := "/usr/local/share/nexus-tools/gh/" + version + "/bin/gh"
	if fetched.GuestBinPath != wantGuest {
		t.Errorf("GuestBinPath=%q want %q", fetched.GuestBinPath, wantGuest)
	}
	if fetched.LinkPath != "/usr/local/bin/gh" {
		t.Errorf("LinkPath=%q", fetched.LinkPath)
	}

	data, err := os.ReadFile(fetched.BinPath)
	if err != nil {
		t.Fatalf("ReadFile BinPath: %v", err)
	}
	if !bytes.Equal(data, binContent) {
		t.Errorf("binary content mismatch")
	}
	info, err := os.Stat(fetched.BinPath)
	if err != nil {
		t.Fatalf("Stat BinPath: %v", err)
	}
	if info.Mode().Perm()&0755 != 0755 {
		t.Errorf("mode %o want 0755", info.Mode().Perm())
	}

	atomic.StoreInt64(&reqCount, 0)
	fetched2, err := f.Fetch(context.Background(), tool, goarch)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if atomic.LoadInt64(&reqCount) != 0 {
		t.Errorf("second Fetch made %d HTTP requests, want 0", reqCount)
	}
	if fetched2.BinPath != fetched.BinPath {
		t.Errorf("BinPath changed on cache hit: %q vs %q", fetched2.BinPath, fetched.BinPath)
	}
}

func TestFetchChecksumMismatch(t *testing.T) {
	const version = "2.101.0"
	const goarch = "amd64"
	member := "gh_" + version + "_linux_" + goarch + "/bin/gh"
	tarData, _ := buildTarball(member, []byte("real bytes"))
	_, wrongPin := buildTarball(member, []byte("other bytes"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarData)
	}))
	defer srv.Close()

	root := t.TempDir()
	f := Fetcher{Root: root}
	tool := makeTestTool(srv.URL, version, goarch, wrongPin)

	_, err := f.Fetch(context.Background(), tool, goarch)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got: %v", err)
	}

	cacheDir := filepath.Join(root, "gh", wrongPin)
	if _, statErr := os.Stat(cacheDir); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("cache dir %s should not exist after mismatch", cacheDir)
	}

	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "dl-") || strings.HasPrefix(e.Name(), "extract-") {
			t.Errorf("stray temp in root after mismatch: %s", e.Name())
		}
	}
}

func TestFetchUnsupportedArch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected HTTP request for unsupported arch")
	}))
	defer srv.Close()

	root := t.TempDir()
	f := Fetcher{Root: root}
	tool := makeTestTool(srv.URL, "2.101.0", "amd64", "deadbeef")

	_, err := f.Fetch(context.Background(), tool, "riscv64")
	if !errors.Is(err, ErrUnsupportedArch) {
		t.Fatalf("want ErrUnsupportedArch, got: %v", err)
	}
}

func TestFetch404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	root := t.TempDir()
	f := Fetcher{Root: root}
	const goarch = "amd64"
	tool := makeTestTool(srv.URL, "2.101.0", goarch, "cafebabe")

	_, err := f.Fetch(context.Background(), tool, goarch)
	if err == nil {
		t.Fatal("want error on 404, got nil")
	}

	cacheDir := filepath.Join(root, "gh", "cafebabe")
	if _, statErr := os.Stat(cacheDir); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("cache dir should not exist after 404")
	}
}

func TestFetchMemberMissing(t *testing.T) {
	const version = "2.101.0"
	const goarch = "amd64"
	tarData, sha := buildTarball("completely/wrong/path", []byte("content"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarData)
	}))
	defer srv.Close()

	root := t.TempDir()
	f := Fetcher{Root: root}
	tool := makeTestTool(srv.URL, version, goarch, sha)

	_, err := f.Fetch(context.Background(), tool, goarch)
	if err == nil {
		t.Fatal("want error for missing member, got nil")
	}

	cacheDir := filepath.Join(root, "gh", sha)
	if _, statErr := os.Stat(cacheDir); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("cache dir should not exist after missing member")
	}

	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "dl-") || strings.HasPrefix(e.Name(), "extract-") {
			t.Errorf("stray temp in root after missing member: %s", e.Name())
		}
	}
}

func TestGHPins(t *testing.T) {
	gh := GH()

	for _, arch := range []string{"amd64", "arm64"} {
		pin, ok := gh.SHA256ByGoArch[arch]
		if !ok {
			t.Errorf("GH missing pin for %s", arch)
			continue
		}
		if len(pin) != 64 {
			t.Errorf("GH pin %s len=%d want 64", arch, len(pin))
		}
		if pin != strings.ToLower(pin) {
			t.Errorf("GH pin %s not lowercase", arch)
		}
		for _, c := range pin {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("GH pin %s has non-hex char %c", arch, c)
			}
		}
	}

	if !strings.Contains(gh.URLTemplate, "{GOARCH}") {
		t.Errorf("GH URLTemplate missing {GOARCH}: %s", gh.URLTemplate)
	}
	if !strings.Contains(gh.URLTemplate, "{VERSION}") {
		t.Errorf("GH URLTemplate missing {VERSION}: %s", gh.URLTemplate)
	}
}

func TestGHIndependentMaps(t *testing.T) {
	g1 := GH()
	g2 := GH()
	g1.SHA256ByGoArch["amd64"] = "modified"
	if g2.SHA256ByGoArch["amd64"] == "modified" {
		t.Error("GH() maps share storage; must return independent maps")
	}
}

func TestDigestEmpty(t *testing.T) {
	if d := Digest(nil); d != "" {
		t.Errorf("Digest(nil)=%q want empty string", d)
	}
	if d := Digest([]Fetched{}); d != "" {
		t.Errorf("Digest([])=%q want empty string", d)
	}
}

func TestDigestStableOrdering(t *testing.T) {
	a := Fetched{Name: "gh", Version: "2.101.0", GoArch: "amd64", SHA256: "abc"}
	b := Fetched{Name: "zz", Version: "1.0.0", GoArch: "amd64", SHA256: "def"}
	d1 := Digest([]Fetched{a, b})
	d2 := Digest([]Fetched{b, a})
	if d1 != d2 {
		t.Errorf("Digest not stable: %q vs %q", d1, d2)
	}
	if len(d1) != 16 {
		t.Errorf("Digest len=%d want 16", len(d1))
	}
}

func TestDigestChangesWithSHA(t *testing.T) {
	t1 := []Fetched{{Name: "gh", Version: "2.101.0", GoArch: "amd64", SHA256: "abc"}}
	t2 := []Fetched{{Name: "gh", Version: "2.101.0", GoArch: "amd64", SHA256: "xyz"}}
	if Digest(t1) == Digest(t2) {
		t.Error("Digest did not change when SHA changed")
	}
}

func buildTarballWithTypeflag(memberPath string, typeflag byte, linkname string) (data []byte, sum string) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{
		Name:     memberPath,
		Typeflag: typeflag,
		Linkname: linkname,
		Mode:     0755,
	})
	_ = tw.Close()
	_ = gw.Close()
	data = buf.Bytes()
	h := sha256.New()
	h.Write(data)
	return data, hex.EncodeToString(h.Sum(nil))
}

func TestFetchSymlinkMemberRejected(t *testing.T) {
	const version = "2.101.0"
	const goarch = "amd64"
	member := "gh_" + version + "_linux_" + goarch + "/bin/gh"
	tarData, sha := buildTarballWithTypeflag(member, tar.TypeSymlink, "/etc/passwd")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarData)
	}))
	defer srv.Close()

	root := t.TempDir()
	f := Fetcher{Root: root}
	tool := makeTestTool(srv.URL, version, goarch, sha)

	_, err := f.Fetch(context.Background(), tool, goarch)
	if err == nil {
		t.Fatal("want error for symlink member, got nil")
	}

	cacheDir := filepath.Join(root, "gh", sha)
	if _, statErr := os.Stat(cacheDir); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("cache dir %s should not exist after symlink rejection", cacheDir)
	}
}

func TestFetchHardlinkMemberRejected(t *testing.T) {
	const version = "2.101.0"
	const goarch = "amd64"
	member := "gh_" + version + "_linux_" + goarch + "/bin/gh"
	tarData, sha := buildTarballWithTypeflag(member, tar.TypeLink, "/etc/passwd")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarData)
	}))
	defer srv.Close()

	root := t.TempDir()
	f := Fetcher{Root: root}
	tool := makeTestTool(srv.URL, version, goarch, sha)

	_, err := f.Fetch(context.Background(), tool, goarch)
	if err == nil {
		t.Fatal("want error for hardlink member, got nil")
	}

	cacheDir := filepath.Join(root, "gh", sha)
	if _, statErr := os.Stat(cacheDir); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("cache dir %s should not exist after hardlink rejection", cacheDir)
	}
}

func TestFetchMemberTooLarge(t *testing.T) {
	const version = "2.101.0"
	const goarch = "amd64"
	member := "gh_" + version + "_linux_" + goarch + "/bin/gh"

	// Lower the cap so we don't allocate 512 MiB.
	orig := maxMemberBytes
	maxMemberBytes = 5
	t.Cleanup(func() { maxMemberBytes = orig })

	content := []byte("toolarge") // 8 bytes > cap of 5
	tarData, sha := buildTarball(member, content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarData)
	}))
	defer srv.Close()

	root := t.TempDir()
	f := Fetcher{Root: root}
	tool := makeTestTool(srv.URL, version, goarch, sha)

	_, err := f.Fetch(context.Background(), tool, goarch)
	if err == nil {
		t.Fatal("want error for oversized member, got nil")
	}

	cacheDir := filepath.Join(root, "gh", sha)
	if _, statErr := os.Stat(cacheDir); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("cache dir %s should not exist after size rejection", cacheDir)
	}
}

func TestSkippedBy(t *testing.T) {
	cf := []byte("FROM ubuntu:22.04\n# " + SkipDirective + "\nRUN echo ok\n")
	if !SkippedBy(cf) {
		t.Error("SkippedBy returned false when directive present")
	}
	if SkippedBy([]byte("FROM ubuntu:22.04\n")) {
		t.Error("SkippedBy returned true when directive absent")
	}
}
