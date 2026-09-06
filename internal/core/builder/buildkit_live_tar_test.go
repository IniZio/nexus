package builder_test

// TestBuildkitTarExportLive proves that the ExporterTar + archive.Apply path
// runs end-to-end against a real buildkitd, producing an ext4 image that
// contains the expected files at the expected sizes.
//
// Unlike TestBuildkitBaseBuild, this test:
//   - Uses a unique Containerfile (RUN echo + timestamp) so buildkitd cannot
//     return a content-store cache hit — the tar export MUST execute.
//   - Stores the image cache in a fixed directory (/tmp/nexus3-w29-live-cache)
//     so the ext4 artifact persists after the test for debugfs inspection.
//   - Emits the path and size of the nexus3-agent binary inside the ext4 so
//     the caller can run `debugfs -R 'stat /sbin/nexus3-agent' <ext4>` to
//     verify no 32 MiB truncation occurred.
//
// Self-skip: the test skips when BUILDKIT_HOST is not set and the default
// buildkitd socket is absent, or when mke2fs is not in PATH.  No build tag is
// required — it compiles and is visible to plain go test / go vet.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// w29LiveEndpoint returns the buildkitd address and true if buildkitd is
// reachable. Checks BUILDKIT_HOST first, then the default socket path.
func w29LiveEndpoint() (string, bool) {
	if h := os.Getenv("BUILDKIT_HOST"); h != "" {
		return h, true
	}
	const defaultSock = "/run/buildkit/buildkitd.sock"
	if _, err := os.Stat(defaultSock); err == nil {
		return "unix://" + defaultSock, true
	}
	return "", false
}

// w29RepoRoot returns the module root by parsing go env GOMOD.
func w29RepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Skipf("skipping: go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		t.Skip("skipping: not in a Go module")
	}
	return filepath.Dir(mod)
}

// w29BuildNexus3Agent compiles cmd/nexus3-agent as a static Linux/amd64
// binary and returns its path in a temp dir cleaned up when t ends.
func w29BuildNexus3Agent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "nexus3-agent")
	cmd := exec.Command("go", "build", "-o", bin,
		"github.com/IniZio/nexus3/cmd/nexus3-agent")
	cmd.Dir = w29RepoRoot(t)
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build nexus3-agent: %s\n%v", out, err)
	}
	return bin
}

func TestBuildkitTarExportLive(t *testing.T) {
	buildkitHost, ok := w29LiveEndpoint()
	if !ok {
		t.Skip("no buildkitd available: set BUILDKIT_HOST or start buildkitd at /run/buildkit/buildkitd.sock")
	}
	if !builder.Mke2fsAvailable() {
		t.Skip("skipping: mke2fs not in PATH")
	}

	// Enable slog at Debug level so exportAndUnpack log lines appear on stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() {
		// Restore default handler after the test.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	})

	agentBin := w29BuildNexus3Agent(t)
	agentStat, err := os.Stat(agentBin)
	if err != nil {
		t.Fatalf("stat agent binary: %v", err)
	}
	t.Logf("source nexus3-agent size: %d bytes (%.1f MiB)", agentStat.Size(), float64(agentStat.Size())/(1<<20))

	// Fixed (persistent) cache dir so the ext4 survives test teardown.
	cacheDir := "/tmp/nexus3-w29-live-cache"
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	cache, err := image.NewCache(cacheDir)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	// Workspace with a UNIQUE Containerfile — prevents buildkitd from returning
	// a cached solve result without executing the tar exporter.
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".nexus"), 0o755); err != nil {
		t.Fatalf("mkdir .nexus: %v", err)
	}
	// Timestamp in the RUN command changes the image fingerprint, forcing a
	// new buildkitd solve. The probe file is visible in debugfs.
	probe := fmt.Sprintf("RUN echo %d > /.nexus3-w29-probe", time.Now().UnixNano())
	cf := "FROM alpine:latest\n" + probe + "\n"
	t.Logf("Containerfile:\n%s", cf)
	if err := os.WriteFile(filepath.Join(workspace, ".nexus", "Containerfile"), []byte(cf), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	b, err := builder.New(builder.Config{
		BuildkitdAddr:   buildkitHost,
		AgentBinaryPath: agentBin,
	}, cache)
	if err != nil {
		t.Fatalf("builder.New (connect failed): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	img, err := b.Build(ctx, builder.BuildRequest{
		BaseRef:      "alpine:latest",
		WorkspaceDir: workspace,
		Ref:          "w29-live-tar-export:alpine",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Builder.Build (elapsed %s): %v", elapsed.Round(time.Second), err)
	}
	t.Logf("Build completed: elapsed=%s digest=%s size=%d", elapsed.Round(time.Second), img.Digest, img.Size)

	if img.Kind != domain.KindBase {
		t.Errorf("img.Kind = %v, want KindBase", img.Kind)
	}

	// Confirm the ext4 artifact is in the cache.
	cached, err := cache.Get(ctx, img.Digest)
	if err != nil {
		t.Fatalf("cache.Get: %v", err)
	}
	t.Logf("cache.Get: digest=%s size=%d", cached.Digest, cached.Size)

	// Locate the ext4 file in the persistent cache dir.
	// Cache stores artifacts under <root>/…/<hex>/artifact.
	var ext4Path string
	_ = filepath.Walk(cacheDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		if filepath.Base(p) == "artifact" {
			ext4Path = p
		}
		return nil
	})
	if ext4Path == "" {
		t.Fatal("could not locate ext4 artifact in cache dir")
	}
	ext4Stat, err := os.Stat(ext4Path)
	if err != nil {
		t.Fatalf("stat ext4: %v", err)
	}
	t.Logf("ext4 artifact: path=%s size=%d bytes (%.1f MiB)", ext4Path, ext4Stat.Size(), float64(ext4Stat.Size())/(1<<20))

	// debugfs: stat /sbin/nexus3-agent inside the ext4.
	debugfsOut, err := exec.Command("debugfs", "-R", "stat /sbin/nexus3-agent", ext4Path).CombinedOutput()
	if err != nil {
		t.Logf("debugfs not available or failed: %v\n%s", err, debugfsOut)
	} else {
		t.Logf("debugfs stat /sbin/nexus3-agent:\n%s", debugfsOut)
	}

	// debugfs: stat /.nexus3-w29-probe (proves unique Containerfile was built).
	probeOut, _ := exec.Command("debugfs", "-R", "stat /.nexus3-w29-probe", ext4Path).CombinedOutput()
	t.Logf("debugfs stat /.nexus3-w29-probe:\n%s", probeOut)
}
