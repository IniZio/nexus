package hostbin

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestResolveAgentEnvWinsOverEmbedded(t *testing.T) {
	agentData := []byte("agent-binary")
	binPath := filepath.Join(t.TempDir(), NexusAgent)
	if err := os.WriteFile(binPath, agentData, 0o755); err != nil {
		t.Fatal(err)
	}
	embData := []byte("embedded-binary")
	embSHA := mustHexSHA256(embData)
	r := &Resolver{
		DataDir: t.TempDir(),
		Embedded: fstest.MapFS{
			NexusAgent + ".zst":    {Data: mustZstd(embData)},
			NexusAgent + ".sha256": {Data: []byte(embSHA + "\n")},
		},
		Getenv: func(key string) string {
			if key == AgentEnvVar {
				return binPath
			}
			return ""
		},
		LookPath: func(string) (string, error) { return "", errors.New("no") },
	}
	a, err := r.ResolveAgent(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Source != SourceEnv {
		t.Errorf("source = %s, want env", a.Source)
	}
	if a.Path != binPath {
		t.Errorf("path = %q, want %q", a.Path, binPath)
	}
}

func TestResolveAgentEnvMissingFileErrors(t *testing.T) {
	r := &Resolver{
		DataDir:  t.TempDir(),
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
		Getenv: func(key string) string {
			if key == AgentEnvVar {
				return "/does/not/exist/nexus-agent"
			}
			return ""
		},
		LookPath: func(string) (string, error) { return "", errors.New("no") },
	}
	_, err := r.ResolveAgent(nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestResolveAgentEmbedded(t *testing.T) {
	agentData := []byte("real-agent-binary")
	sha := mustHexSHA256(agentData)
	dataDir := t.TempDir()
	r := &Resolver{
		DataDir: dataDir,
		Embedded: fstest.MapFS{
			NexusAgent + ".zst":    {Data: mustZstd(agentData)},
			NexusAgent + ".sha256": {Data: []byte(sha)},
		},
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("no") },
	}

	a, err := r.ResolveAgent(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Source != SourceEmbedded {
		t.Errorf("source = %s, want embedded", a.Source)
	}
	wantPath := filepath.Join(dataDir, "artifacts", sha, NexusAgent)
	if a.Path != wantPath {
		t.Errorf("path = %q, want %q", a.Path, wantPath)
	}
	if !bytes.Equal(a.Bytes, agentData) {
		t.Error("bytes mismatch")
	}
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("extracted file missing: %v", err)
	}
	if !bytes.Equal(got, agentData) {
		t.Error("extracted file content mismatch")
	}

	fi1, _ := os.Stat(wantPath)
	a2, err := r.ResolveAgent(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a2.Source != SourceEmbedded {
		t.Errorf("second call source = %s, want embedded", a2.Source)
	}
	fi2, _ := os.Stat(a2.Path)
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Error("second call rewrote the file (cache miss)")
	}
}

func TestResolveAgentEmbeddedWrongSHA(t *testing.T) {
	agentData := []byte("real-agent-binary")
	wrongSHA := mustHexSHA256([]byte("other"))
	r := &Resolver{
		DataDir: t.TempDir(),
		Embedded: fstest.MapFS{
			NexusAgent + ".zst":    {Data: mustZstd(agentData)},
			NexusAgent + ".sha256": {Data: []byte(wrongSHA)},
		},
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("no") },
	}
	_, err := r.ResolveAgent(nil)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
}

func TestResolveAgentNoEmbedFallsBackToPath(t *testing.T) {
	agentData := []byte("path-agent-binary")
	binPath := filepath.Join(t.TempDir(), NexusAgent)
	if err := os.WriteFile(binPath, agentData, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{
		DataDir:  t.TempDir(),
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("no artifacts")}},
		Getenv:   func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			if name == NexusAgent {
				return binPath, nil
			}
			return "", errors.New("no")
		},
	}
	a, err := r.ResolveAgent(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Source != SourcePath {
		t.Errorf("source = %s, want path", a.Source)
	}
	if !bytes.Equal(a.Bytes, agentData) {
		t.Error("bytes mismatch")
	}
}

func TestResolveAgentKernelDir(t *testing.T) {
	agentData := []byte("kernel-agent-binary")
	kdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kdir, NexusAgent), agentData, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{
		DataDir:  t.TempDir(),
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("no") },
	}
	a, err := r.ResolveAgent(func() string { return kdir })
	if err != nil {
		t.Fatal(err)
	}
	if a.Source != SourceKernelDir {
		t.Errorf("source = %s, want kernel-dir", a.Source)
	}
	if !bytes.Equal(a.Bytes, agentData) {
		t.Error("bytes mismatch")
	}
}

func TestResolveAgentNothingFound(t *testing.T) {
	r := &Resolver{
		DataDir:  t.TempDir(),
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("no") },
	}
	_, err := r.ResolveAgent(nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	_, err = r.ResolveAgent(func() string { return "" })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound with empty kernelDir, got %v", err)
	}
}

func TestEmbeddedAgentSHA256(t *testing.T) {
	sha := "abc123def456"
	r := &Resolver{
		Embedded: fstest.MapFS{
			NexusAgent + ".sha256": {Data: []byte(sha + "\n")},
		},
	}
	if got := r.EmbeddedAgentSHA256(); got != sha {
		t.Errorf("EmbeddedAgentSHA256() = %q, want %q", got, sha)
	}

	r2 := &Resolver{
		Embedded: fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}},
	}
	if got := r2.EmbeddedAgentSHA256(); got != "" {
		t.Errorf("EmbeddedAgentSHA256() absent = %q, want empty", got)
	}
}
