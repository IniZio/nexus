package builder

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

// TestSynthesizeDockerfileWithTools_EmptyDir checks that passing "" for
// toolsCtxDir produces byte-identical output to synthesizeDockerfile.
func TestSynthesizeDockerfileWithTools_EmptyDir(t *testing.T) {
	containerfile := []byte("FROM debian:bookworm-slim\nRUN echo hello\n")
	recipeBytes := []byte("RUN apt-get install -y curl\n")
	agentFile := "_nexus-agent-abc123"
	installPath := "/sbin/nexus-agent"
	runcShim := runcShimContextFilename

	want := synthesizeDockerfile(containerfile, recipeBytes, agentFile, installPath, runcShim)
	got := synthesizeDockerfileWithTools(containerfile, recipeBytes, "", agentFile, installPath, runcShim)

	if !bytes.Equal(want, got) {
		t.Fatalf("synthesizeDockerfileWithTools(\"\") differs from synthesizeDockerfile:\nwant: %s\ngot:  %s", want, got)
	}
}

// TestSynthesizeDockerfileWithTools_EmptyDir_NoRecipe checks the no-recipe case.
func TestSynthesizeDockerfileWithTools_EmptyDir_NoRecipe(t *testing.T) {
	containerfile := []byte("FROM scratch\n")
	agentFile := "_nexus-agent-def456"
	installPath := "/sbin/nexus-agent"
	runcShim := runcShimContextFilename

	want := synthesizeDockerfile(containerfile, nil, agentFile, installPath, runcShim)
	got := synthesizeDockerfileWithTools(containerfile, nil, "", agentFile, installPath, runcShim)

	if !bytes.Equal(want, got) {
		t.Fatalf("synthesizeDockerfileWithTools(\"\") no-recipe differs from synthesizeDockerfile:\nwant: %s\ngot:  %s", want, got)
	}
}

// TestSynthesizeDockerfileWithTools_WithDir checks that when toolsCtxDir is
// non-empty the COPY line appears exactly once and before "# Final layer".
func TestSynthesizeDockerfileWithTools_WithDir(t *testing.T) {
	containerfile := []byte("FROM debian:bookworm-slim\n")
	agentFile := "_nexus-agent-ghi789"
	installPath := "/sbin/nexus-agent"
	runcShim := runcShimContextFilename
	toolsDir := sandboxToolsContextDir

	out := string(synthesizeDockerfileWithTools(containerfile, nil, toolsDir, agentFile, installPath, runcShim))

	wantCOPY := "COPY --from=nexusagent " + toolsDir + "/ /"
	count := strings.Count(out, wantCOPY)
	if count != 1 {
		t.Fatalf("expected COPY line exactly once, got %d occurrences in:\n%s", count, out)
	}

	idxCOPY := strings.Index(out, wantCOPY)
	idxFinal := strings.Index(out, "# Final layer")
	if idxFinal == -1 {
		t.Fatalf("'# Final layer' marker not found in output:\n%s", out)
	}
	if idxCOPY >= idxFinal {
		t.Fatalf("COPY sandbox-tools line at %d must appear before '# Final layer' at %d:\n%s", idxCOPY, idxFinal, out)
	}
}

// TestStageSandboxTools_EmptySrcDir returns "" for empty srcDir.
func TestStageSandboxTools_EmptySrcDir(t *testing.T) {
	agentDir := t.TempDir()
	result, err := stageSandboxTools(agentDir, "", []byte("FROM scratch\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty result, got %q", result)
	}
}

// TestStageSandboxTools_MissingSrcDir returns "" for a non-existent srcDir.
func TestStageSandboxTools_MissingSrcDir(t *testing.T) {
	agentDir := t.TempDir()
	result, err := stageSandboxTools(agentDir, "/does/not/exist/ever", []byte("FROM scratch\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty result, got %q", result)
	}
}

// TestStageSandboxTools_EmptyDir returns "" for an existing but empty srcDir.
func TestStageSandboxTools_EmptyDir(t *testing.T) {
	agentDir := t.TempDir()
	srcDir := t.TempDir() // empty
	result, err := stageSandboxTools(agentDir, srcDir, []byte("FROM scratch\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty result for empty srcDir, got %q", result)
	}
}

// TestStageSandboxTools_SkipDirective returns "" when containerfile contains
// the skip directive and does not copy anything.
func TestStageSandboxTools_SkipDirective(t *testing.T) {
	agentDir := t.TempDir()
	srcDir := t.TempDir()
	// Populate srcDir so it is non-empty.
	if err := os.WriteFile(filepath.Join(srcDir, "gh"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	cf := []byte("FROM scratch\n# " + toolcache.SkipDirective + "\n")
	result, err := stageSandboxTools(agentDir, srcDir, cf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty result when skip directive present, got %q", result)
	}
	// Verify nothing was copied into agentDir.
	dstDir := filepath.Join(agentDir, sandboxToolsContextDir)
	if _, err := os.Stat(dstDir); err == nil {
		t.Fatalf("expected dst dir to not exist when skip directive present")
	}
}

// makeAgentReq builds a minimal SolveRequest for prepareSolveDockerfile tests.
// It writes a fake agent binary to t.TempDir and sets AgentPath to it.
func makeAgentReq(t *testing.T, containerfile []byte, sandboxToolsDir string) SolveRequest {
	t.Helper()
	agentBin := filepath.Join(t.TempDir(), "nexus-agent")
	if err := os.WriteFile(agentBin, []byte("#!/bin/sh\nexec fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	return SolveRequest{
		ContainerfileBytes: containerfile,
		AgentPath:          agentBin,
		AgentInstallPath:   "/sbin/nexus-agent",
		SandboxToolsDir:    sandboxToolsDir,
		ToolRecipe:         cred.ToolRecipe{}, // no recipe packages
	}
}

// TestPrepareSolveDockerfile_WithSandboxTools checks that a populated
// SandboxToolsDir causes the returned Dockerfile to contain the
// COPY --from=nexusagent nexus-sandbox-tools/ / line and that the source
// tree is staged under agentDir/nexus-sandbox-tools.
func TestPrepareSolveDockerfile_WithSandboxTools(t *testing.T) {
	// Build a populated SandboxToolsDir.
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "gh"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	agentDir := t.TempDir()
	req := makeAgentReq(t, []byte("FROM debian:bookworm-slim\n"), srcDir)
	df, err := prepareSolveDockerfile(req, agentDir)
	if err != nil {
		t.Fatalf("prepareSolveDockerfile: %v", err)
	}

	wantCOPY := "COPY --from=nexusagent " + sandboxToolsContextDir + "/ /"
	if !strings.Contains(string(df), wantCOPY) {
		t.Fatalf("Dockerfile missing COPY sandbox-tools line.\nwant: %q\ngot:\n%s", wantCOPY, df)
	}

	stagedFile := filepath.Join(agentDir, sandboxToolsContextDir, "gh")
	if _, err := os.Stat(stagedFile); err != nil {
		t.Fatalf("staged file not found at %s: %v", stagedFile, err)
	}
}

// TestPrepareSolveDockerfile_EmptySandboxToolsDir checks that SandboxToolsDir=""
// produces no sandbox-tools COPY line in the returned Dockerfile.
func TestPrepareSolveDockerfile_EmptySandboxToolsDir(t *testing.T) {
	agentDir := t.TempDir()
	req := makeAgentReq(t, []byte("FROM debian:bookworm-slim\n"), "")
	df, err := prepareSolveDockerfile(req, agentDir)
	if err != nil {
		t.Fatalf("prepareSolveDockerfile: %v", err)
	}

	wantCOPY := "COPY --from=nexusagent " + sandboxToolsContextDir + "/ /"
	if strings.Contains(string(df), wantCOPY) {
		t.Fatalf("Dockerfile must not contain COPY sandbox-tools line when SandboxToolsDir is empty:\n%s", df)
	}
}

// TestPrepareSolveDockerfile_SkipDirective checks that when the Containerfile
// contains the sandbox-tools skip directive, the returned Dockerfile has no
// COPY sandbox-tools line even if SandboxToolsDir is populated.
func TestPrepareSolveDockerfile_SkipDirective(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "gh"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	cf := []byte("FROM scratch\n# " + toolcache.SkipDirective + "\n")
	agentDir := t.TempDir()
	req := makeAgentReq(t, cf, srcDir)
	df, err := prepareSolveDockerfile(req, agentDir)
	if err != nil {
		t.Fatalf("prepareSolveDockerfile: %v", err)
	}

	wantCOPY := "COPY --from=nexusagent " + sandboxToolsContextDir + "/ /"
	if strings.Contains(string(df), wantCOPY) {
		t.Fatalf("Dockerfile must not contain COPY sandbox-tools line when skip directive present:\n%s", df)
	}
}

// TestStageSandboxTools_PopulatedSrc checks that a populated srcDir is copied
// correctly: the function returns sandboxToolsContextDir, a regular file's mode
// is preserved (0755), and a relative symlink is copied as a symlink (Readlink
// returns the same target).
func TestStageSandboxTools_PopulatedSrc(t *testing.T) {
	agentDir := t.TempDir()
	srcDir := t.TempDir()

	// Create usr/local/share/nexus-tools/gh/2.101.0/bin/gh (mode 0755).
	binDir := filepath.Join(srcDir, "usr", "local", "share", "nexus-tools", "gh", "2.101.0", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghBin := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghBin, []byte("#!/bin/sh\necho gh"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create usr/local/bin/ directory and a relative symlink pointing to the gh binary.
	linkDir := filepath.Join(srcDir, "usr", "local", "bin")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Relative symlink: ../share/nexus-tools/gh/2.101.0/bin/gh
	relTarget := "../share/nexus-tools/gh/2.101.0/bin/gh"
	if err := os.Symlink(relTarget, filepath.Join(linkDir, "gh")); err != nil {
		t.Fatal(err)
	}

	result, err := stageSandboxTools(agentDir, srcDir, []byte("FROM scratch\n"))
	if err != nil {
		t.Fatalf("stageSandboxTools error: %v", err)
	}
	if result != sandboxToolsContextDir {
		t.Fatalf("expected %q, got %q", sandboxToolsContextDir, result)
	}

	// Verify the binary was copied with mode 0755.
	dstBin := filepath.Join(agentDir, sandboxToolsContextDir, "usr", "local", "share", "nexus-tools", "gh", "2.101.0", "bin", "gh")
	info, err := os.Lstat(dstBin)
	if err != nil {
		t.Fatalf("gh binary not found at dst: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("gh binary lost execute permission: mode=%v", info.Mode())
	}

	// Verify the symlink is a symlink with the same relative target.
	dstLink := filepath.Join(agentDir, sandboxToolsContextDir, "usr", "local", "bin", "gh")
	linkInfo, err := os.Lstat(dstLink)
	if err != nil {
		t.Fatalf("symlink not found at dst: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink, got mode=%v", linkInfo.Mode())
	}
	gotTarget, err := os.Readlink(dstLink)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if gotTarget != relTarget {
		t.Fatalf("symlink target: want %q, got %q", relTarget, gotTarget)
	}
}
