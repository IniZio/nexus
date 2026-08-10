package builder_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// fakeBuildkitClient is the test double for builder.BuildkitClient.
//
// It records the last SolveRequest so tests can assert the correct solve
// specification was assembled, and populates outDir with a minimal POSIX
// filesystem tree so the ext4 export step has something to process.
//
// No buildkitd process, OCI registry, or network access is required.
type fakeBuildkitClient struct {
	// LastReq is the SolveRequest passed to the most recent Solve call.
	LastReq builder.SolveRequest
	// err, if non-nil, causes Solve to return this error immediately.
	err error
}

func (f *fakeBuildkitClient) Solve(_ context.Context, req builder.SolveRequest, outDir string) error {
	f.LastReq = req
	if f.err != nil {
		return f.err
	}
	// Populate a minimal filesystem that satisfies mke2fs -d requirements.
	dirs := []string{
		filepath.Join(outDir, "sbin"),
		filepath.Join(outDir, "etc"),
		filepath.Join(outDir, "usr", "lib"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	// Write a placeholder agent so the tree is not empty.
	if err := os.WriteFile(
		filepath.Join(outDir, "sbin", "nexus3-agent"),
		[]byte("fake-agent-binary"),
		0755,
	); err != nil {
		return err
	}
	return nil
}

// setupWorkspace creates a temp workspace directory with a .nexus/Containerfile
// containing cfContent.
func setupWorkspace(t *testing.T, cfContent string) string {
	t.Helper()
	dir := t.TempDir()
	nexusDir := filepath.Join(dir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("setupWorkspace: mkdir .nexus: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nexusDir, "Containerfile"), []byte(cfContent), 0644); err != nil {
		t.Fatalf("setupWorkspace: write Containerfile: %v", err)
	}
	return dir
}

// setupAgentBinary writes a fake agent binary to a temp file and returns its path.
func setupAgentBinary(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "nexus3-agent-*")
	if err != nil {
		t.Fatalf("setupAgentBinary: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString("fake-agent-binary-content"); err != nil {
		t.Fatalf("setupAgentBinary: write: %v", err)
	}
	return f.Name()
}

// newTestBuilder constructs a Builder backed by the given fake for unit tests.
func newTestBuilder(t *testing.T, fake *fakeBuildkitClient, cacheDir string) *builder.Builder {
	t.Helper()
	agentPath := setupAgentBinary(t)
	cache, err := image.NewCache(cacheDir)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	cfg := builder.Config{
		BuildkitdAddr:   "unix:///run/buildkit/buildkitd.sock",
		AgentBinaryPath: agentPath,
		ImageKind:       domain.KindBase,
	}
	return builder.NewWithClient(cfg, fake, cache)
}

// ─── happy-path test ──────────────────────────────────────────────────────────

// TestBuild_HappyPath verifies the full Build pipeline with the fake seam:
//   - correct SolveRequest assembled (base ref, Containerfile bytes, agent path,
//     agent install path matching the boot contract)
//   - ext4 image produced (requires mke2fs; skipped if unavailable)
//   - cache.Put called with a valid digest-keyed artifact
func TestBuild_HappyPath(t *testing.T) {
	if !builder.Mke2fsAvailable() {
		t.Skip("mke2fs not available; install e2fsprogs to run this test")
	}

	cacheDir := t.TempDir()
	fake := &fakeBuildkitClient{}
	b := newTestBuilder(t, fake, cacheDir)
	workspace := setupWorkspace(t, "FROM debian:bookworm-slim\nRUN echo hello\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	img, err := b.Build(ctx, builder.BuildRequest{
		BaseRef:      "debian:bookworm-slim",
		WorkspaceDir: workspace,
		Ref:          "test:latest",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// ── assert SolveRequest correctness ───────────────────────────────────────

	if fake.LastReq.BaseRef != "debian:bookworm-slim" {
		t.Errorf("SolveRequest.BaseRef = %q, want %q",
			fake.LastReq.BaseRef, "debian:bookworm-slim")
	}
	if len(fake.LastReq.ContainerfileBytes) == 0 {
		t.Error("SolveRequest.ContainerfileBytes is empty; expected Containerfile content")
	}
	if fake.LastReq.AgentPath == "" {
		t.Error("SolveRequest.AgentPath is empty")
	}
	// Boot contract: agent must be installed at /sbin/nexus3-agent.
	const wantInstall = "/sbin/nexus3-agent"
	if fake.LastReq.AgentInstallPath != wantInstall {
		t.Errorf("SolveRequest.AgentInstallPath = %q, want %q (boot contract: init=%s)",
			fake.LastReq.AgentInstallPath, wantInstall, wantInstall)
	}

	// ── assert cache artifact ─────────────────────────────────────────────────

	if !img.Digest.Valid() {
		t.Errorf("Build returned invalid digest %q", img.Digest)
	}
	if img.Kind != domain.KindBase {
		t.Errorf("img.Kind = %v, want KindBase", img.Kind)
	}
	if img.Ref != "test:latest" {
		t.Errorf("img.Ref = %q, want %q", img.Ref, "test:latest")
	}
	if img.Size <= 0 {
		t.Errorf("img.Size = %d, expected > 0", img.Size)
	}

	// Verify the artifact is retrievable from the cache.
	cache, err := image.NewCache(cacheDir)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	cached, err := cache.Get(context.Background(), img.Digest)
	if err != nil {
		t.Fatalf("cache.Get(%s): %v", img.Digest, err)
	}
	if cached.Digest != img.Digest {
		t.Errorf("cached.Digest = %v, want %v", cached.Digest, img.Digest)
	}
}

// ─── error path tests (no mke2fs required) ────────────────────────────────────

// TestBuild_MissingContainerfile verifies Build returns an error when the
// workspace does not contain .nexus/Containerfile.
func TestBuild_MissingContainerfile(t *testing.T) {
	cacheDir := t.TempDir()
	fake := &fakeBuildkitClient{}
	b := newTestBuilder(t, fake, cacheDir)

	// workspace intentionally lacks .nexus/Containerfile
	workspace := t.TempDir()

	_, err := b.Build(context.Background(), builder.BuildRequest{
		BaseRef:      "debian:bookworm-slim",
		WorkspaceDir: workspace,
	})
	if err == nil {
		t.Fatal("Build: expected error for missing .nexus/Containerfile, got nil")
	}
}

// TestBuild_EmptyContainerfile verifies Build rejects an empty Containerfile.
func TestBuild_EmptyContainerfile(t *testing.T) {
	cacheDir := t.TempDir()
	fake := &fakeBuildkitClient{}
	b := newTestBuilder(t, fake, cacheDir)

	workspace := setupWorkspace(t, "") // empty

	_, err := b.Build(context.Background(), builder.BuildRequest{
		BaseRef:      "debian:bookworm-slim",
		WorkspaceDir: workspace,
	})
	if err == nil {
		t.Fatal("Build: expected error for empty Containerfile, got nil")
	}
}

// TestBuild_MissingAgentBinary verifies Build returns an error when
// Config.AgentBinaryPath does not exist.
func TestBuild_MissingAgentBinary(t *testing.T) {
	cacheDir := t.TempDir()
	fake := &fakeBuildkitClient{}
	cache, err := image.NewCache(cacheDir)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	cfg := builder.Config{
		BuildkitdAddr:   "unix:///run/buildkit/buildkitd.sock",
		AgentBinaryPath: "/nonexistent/path/nexus3-agent",
		ImageKind:       domain.KindBase,
	}
	b := builder.NewWithClient(cfg, fake, cache)
	workspace := setupWorkspace(t, "FROM debian:bookworm-slim\n")

	_, err = b.Build(context.Background(), builder.BuildRequest{
		BaseRef:      "debian:bookworm-slim",
		WorkspaceDir: workspace,
	})
	if err == nil {
		t.Fatal("Build: expected error for missing agent binary, got nil")
	}
}

// TestBuild_SolveError verifies Build propagates errors from the BuildkitClient
// seam (e.g. buildkitd unreachable, out of disk space).
func TestBuild_SolveError(t *testing.T) {
	cacheDir := t.TempDir()
	fake := &fakeBuildkitClient{err: errors.New("buildkitd: no space left on device")}
	b := newTestBuilder(t, fake, cacheDir)
	workspace := setupWorkspace(t, "FROM debian:bookworm-slim\n")

	_, err := b.Build(context.Background(), builder.BuildRequest{
		BaseRef:      "debian:bookworm-slim",
		WorkspaceDir: workspace,
	})
	if err == nil {
		t.Fatal("Build: expected error from BuildkitClient.Solve, got nil")
	}
}

// TestBuild_EmptyBaseRef verifies Build returns an error immediately if
// BuildRequest.BaseRef is empty.
func TestBuild_EmptyBaseRef(t *testing.T) {
	cacheDir := t.TempDir()
	fake := &fakeBuildkitClient{}
	b := newTestBuilder(t, fake, cacheDir)
	workspace := setupWorkspace(t, "FROM debian:bookworm-slim\n")

	_, err := b.Build(context.Background(), builder.BuildRequest{
		// BaseRef intentionally empty
		WorkspaceDir: workspace,
	})
	if err == nil {
		t.Fatal("Build: expected error for empty BaseRef, got nil")
	}
}

// TestBuild_EmptyWorkspaceDir verifies Build returns an error immediately if
// BuildRequest.WorkspaceDir is empty.
func TestBuild_EmptyWorkspaceDir(t *testing.T) {
	cacheDir := t.TempDir()
	fake := &fakeBuildkitClient{}
	b := newTestBuilder(t, fake, cacheDir)

	_, err := b.Build(context.Background(), builder.BuildRequest{
		BaseRef: "debian:bookworm-slim",
		// WorkspaceDir intentionally empty
	})
	if err == nil {
		t.Fatal("Build: expected error for empty WorkspaceDir, got nil")
	}
}

// ─── build-context tests ──────────────────────────────────────────────────────

// TestBuild_WorkspaceDirThreaded verifies that Build passes WorkspaceDir
// through to the SolveRequest, so that the BuildkitClient (and therefore
// buildkitd) receives the correct workspace root as the build context.
// This is the precondition for user COPY instructions to resolve against the
// repo root.
func TestBuild_WorkspaceDirThreaded(t *testing.T) {
	if !builder.Mke2fsAvailable() {
		t.Skip("mke2fs not available; install e2fsprogs to run this test")
	}

	cacheDir := t.TempDir()
	fake := &fakeBuildkitClient{}
	b := newTestBuilder(t, fake, cacheDir)

	// Workspace contains a file that a real Containerfile COPY would reference.
	workspace := setupWorkspace(t, "FROM debian:bookworm-slim\n# COPY assets/hello.txt /hello.txt\n")
	if err := os.MkdirAll(filepath.Join(workspace, "assets"), 0755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "assets", "hello.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write hello.txt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := b.Build(ctx, builder.BuildRequest{
		BaseRef:      "debian:bookworm-slim",
		WorkspaceDir: workspace,
		Ref:          "test:ctx",
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// SolveRequest.WorkspaceDir must equal the workspace root.
	if fake.LastReq.WorkspaceDir != workspace {
		t.Errorf("SolveRequest.WorkspaceDir = %q, want %q",
			fake.LastReq.WorkspaceDir, workspace)
	}
}

// TestBuild_AgentLayerPreserved verifies that after the WorkspaceDir context
// extension the appended agent layer still targets /sbin/nexus3-agent.
// This is the boot contract: the kernel command line passes init=/sbin/nexus3-agent.
func TestBuild_AgentLayerPreserved(t *testing.T) {
	if !builder.Mke2fsAvailable() {
		t.Skip("mke2fs not available; install e2fsprogs to run this test")
	}

	cacheDir := t.TempDir()
	fake := &fakeBuildkitClient{}
	b := newTestBuilder(t, fake, cacheDir)
	workspace := setupWorkspace(t, "FROM debian:bookworm-slim\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := b.Build(ctx, builder.BuildRequest{
		BaseRef:      "debian:bookworm-slim",
		WorkspaceDir: workspace,
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	const wantInstall = "/sbin/nexus3-agent"
	if fake.LastReq.AgentInstallPath != wantInstall {
		t.Errorf("SolveRequest.AgentInstallPath = %q, want %q (boot contract: init=%s)",
			fake.LastReq.AgentInstallPath, wantInstall, wantInstall)
	}
	if fake.LastReq.AgentPath == "" {
		t.Error("SolveRequest.AgentPath is empty; agent binary was not threaded through")
	}
}

// TestCopyDirIntoContext_EscapeSymlink verifies that copyDirIntoContext skips
// symlinks whose resolved targets lie outside the source directory, preventing
// a malicious workspace from reading host files outside the repo root.
func TestCopyDirIntoContext_EscapeSymlink(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create a legitimate file inside the workspace.
	if err := os.WriteFile(filepath.Join(src, "legit.txt"), []byte("ok"), 0644); err != nil {
		t.Fatalf("write legit.txt: %v", err)
	}

	// Create an escape symlink pointing to /etc/passwd (outside src).
	escapePath := filepath.Join(src, "escape.txt")
	if err := os.Symlink("/etc/passwd", escapePath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := builder.CopyDirIntoContext(src, dst); err != nil {
		t.Fatalf("CopyDirIntoContext: %v", err)
	}

	// The legitimate file must be copied.
	if _, err := os.Stat(filepath.Join(dst, "legit.txt")); err != nil {
		t.Errorf("legit.txt not found in dst: %v", err)
	}

	// The escape symlink must NOT be present in the destination.
	if _, err := os.Stat(filepath.Join(dst, "escape.txt")); err == nil {
		t.Error("escape.txt should NOT be copied into dst (escape symlink was allowed)")
	}
}
