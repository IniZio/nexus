package hostbin

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func hubResolver(t *testing.T, data []byte, env map[string]string) *Resolver {
	t.Helper()
	return &Resolver{
		DataDir: t.TempDir(),
		Embedded: fstest.MapFS{
			"nexus-hub.zst":    {Data: mustZstd(data)},
			"nexus-hub.sha256": {Data: []byte(mustHexSHA256(data) + "\n")},
		},
		Getenv:   func(k string) string { return env[k] },
		LookPath: func(string) (string, error) { return "/usr/bin/nexus-hub", nil },
	}
}

func TestResolveEmbeddedToolExtracts(t *testing.T) {
	data := []byte("hub-binary")
	r := hubResolver(t, data, nil)
	p, err := r.ResolveEmbeddedTool("nexus-hub")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(r.DataDir, "artifacts", mustHexSHA256(data), "nexus-hub")
	if p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}
	got, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("content mismatch: %v", err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("not executable: %v", fi.Mode())
	}
	p2, err := r.ResolveEmbeddedTool("nexus-hub")
	if err != nil || p2 != p {
		t.Fatalf("second resolve: %q %v", p2, err)
	}
}

func TestResolveEmbeddedToolEnvOverride(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "nexus-hub")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := hubResolver(t, []byte("emb"), map[string]string{EnvVar("nexus-hub"): bin})
	p, err := r.ResolveEmbeddedTool("nexus-hub")
	if err != nil || p != bin {
		t.Fatalf("got %q %v, want %q", p, err, bin)
	}
	r.Getenv = func(k string) string {
		if k == EnvVar("nexus-hub") {
			return "/does/not/exist"
		}
		return ""
	}
	if _, err := r.ResolveEmbeddedTool("nexus-hub"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestResolveEmbeddedToolNoFallback(t *testing.T) {
	r := &Resolver{
		DataDir:  t.TempDir(),
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return "/usr/bin/nexus-hub", nil },
	}
	_, err := r.ResolveEmbeddedTool("nexus-hub")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound (no PATH fallback), got %v", err)
	}
}

func TestResolveEmbeddedToolChecksumMismatch(t *testing.T) {
	r := hubResolver(t, []byte("good"), nil)
	r.Embedded = fstest.MapFS{
		"nexus-hub.zst":    {Data: mustZstd([]byte("tampered"))},
		"nexus-hub.sha256": {Data: []byte(mustHexSHA256([]byte("good")) + "\n")},
	}
	if _, err := r.ResolveEmbeddedTool("nexus-hub"); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
}
