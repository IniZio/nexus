package cli

import (
	"context"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/service"
)

// fakeFetched returns a minimal toolcache.Fetched for test assertions.
func fakeFetched(name string) toolcache.Fetched {
	return toolcache.Fetched{Name: name, SHA256: "abc123"}
}

// fetchAlwaysGH is a toolFetchFn that always returns a gh tool entry.
func fetchAlwaysGH(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
	return fakeFetched("gh"), nil
}

// TestBuildRunCreateOpts_SandboxToolsSet verifies that an image ref with optOut=false
// results in SandboxTools being populated (at least one tool).
// If the assignment in buildRunCreateOpts is removed, this test fails.
func TestBuildRunCreateOpts_SandboxToolsSet(t *testing.T) {
	opts := buildRunCreateOpts(
		context.Background(),
		"ghcr.io/example/myimage:latest",
		nil,
		t.TempDir(),
		0, 0,
		false,
		false, // optOut=false → tools should be injected
		fetchAlwaysGH,
	)
	if len(opts.SandboxTools) == 0 {
		t.Fatal("expected SandboxTools to be populated for OCI image ref with optOut=false; got none — likely the imageOptsSandboxTools assignment was removed")
	}
	found := false
	for _, tool := range opts.SandboxTools {
		if tool.Name == "gh" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected gh in SandboxTools, got %v", opts.SandboxTools)
	}
}

// TestBuildRunCreateOpts_OptOut verifies that optOut=true results in nil SandboxTools.
func TestBuildRunCreateOpts_OptOut(t *testing.T) {
	called := 0
	fetch := func(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
		called++
		return fakeFetched("gh"), nil
	}
	opts := buildRunCreateOpts(
		context.Background(),
		"ghcr.io/example/myimage:latest",
		nil,
		t.TempDir(),
		0, 0,
		false,
		true, // optOut=true → tools must NOT be injected
		fetch,
	)
	if len(opts.SandboxTools) != 0 {
		t.Fatalf("expected nil SandboxTools when optOut=true, got %v", opts.SandboxTools)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called when optOut=true, got %d calls", called)
	}
}

// TestParseRunArgs_NoSandboxToolsFlag verifies that --no-sandbox-tools before the image ref
// sets noSandboxTools=true even when the user-global opt-out is false.
// The mutation proof relies on this test: removing *noSandboxToolsFlag from the
// optOut expression in parseRunArgs causes noSandboxTools to be false here, failing the assertion.
func TestParseRunArgs_NoSandboxToolsFlag(t *testing.T) {
	orig := userGlobalSandboxToolsOptOut
	userGlobalSandboxToolsOptOut = func() bool { return false }
	t.Cleanup(func() { userGlobalSandboxToolsOptOut = orig })

	parsed, err := parseRunArgs([]string{"--no-sandbox-tools", "img:tag", "--", "echo", "hi"})
	if err != nil {
		t.Fatalf("parseRunArgs: %v", err)
	}
	if !parsed.noSandboxTools {
		t.Fatal("expected noSandboxTools=true after --no-sandbox-tools flag with global opt-out=false")
	}
	if parsed.imageRef != "img:tag" {
		t.Errorf("imageRef: got %q want %q", parsed.imageRef, "img:tag")
	}
	if len(parsed.argv) != 2 || parsed.argv[0] != "echo" || parsed.argv[1] != "hi" {
		t.Errorf("argv: got %v want [echo hi]", parsed.argv)
	}
}

// TestParseRunArgs_WithoutFlag is the control: no flag → noSandboxTools=false with global false.
func TestParseRunArgs_WithoutFlag(t *testing.T) {
	orig := userGlobalSandboxToolsOptOut
	userGlobalSandboxToolsOptOut = func() bool { return false }
	t.Cleanup(func() { userGlobalSandboxToolsOptOut = orig })

	parsed, err := parseRunArgs([]string{"img:tag", "--", "echo", "hi"})
	if err != nil {
		t.Fatalf("parseRunArgs: %v", err)
	}
	if parsed.noSandboxTools {
		t.Fatal("expected noSandboxTools=false without --no-sandbox-tools flag")
	}
}

// TestBuildRunCreateOpts_RunArgs_OptOut threads parsed.noSandboxTools=true through
// to buildRunCreateOpts and asserts nil SandboxTools with no fetch call.
func TestBuildRunCreateOpts_RunArgs_OptOut(t *testing.T) {
	orig := userGlobalSandboxToolsOptOut
	userGlobalSandboxToolsOptOut = func() bool { return false }
	t.Cleanup(func() { userGlobalSandboxToolsOptOut = orig })

	parsed, err := parseRunArgs([]string{"--no-sandbox-tools", "img:tag", "--", "echo", "hi"})
	if err != nil {
		t.Fatalf("parseRunArgs: %v", err)
	}

	called := 0
	fetch := func(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
		called++
		return fakeFetched("gh"), nil
	}
	opts := buildRunCreateOpts(
		context.Background(),
		parsed.imageRef,
		nil,
		t.TempDir(),
		0, 0,
		false,
		parsed.noSandboxTools,
		fetch,
	)
	if len(opts.SandboxTools) != 0 {
		t.Fatalf("expected nil SandboxTools when --no-sandbox-tools, got %v", opts.SandboxTools)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called, got %d calls", called)
	}
}

// TestBuildRunCreateOpts_RunArgs_ToolsPresent is the control: without the flag, tools are injected.
func TestBuildRunCreateOpts_RunArgs_ToolsPresent(t *testing.T) {
	orig := userGlobalSandboxToolsOptOut
	userGlobalSandboxToolsOptOut = func() bool { return false }
	t.Cleanup(func() { userGlobalSandboxToolsOptOut = orig })

	parsed, err := parseRunArgs([]string{"img:tag", "--", "echo", "hi"})
	if err != nil {
		t.Fatalf("parseRunArgs: %v", err)
	}

	opts := buildRunCreateOpts(
		context.Background(),
		parsed.imageRef,
		nil,
		t.TempDir(),
		0, 0,
		false,
		parsed.noSandboxTools,
		fetchAlwaysGH,
	)
	if len(opts.SandboxTools) == 0 {
		t.Fatal("expected SandboxTools populated without --no-sandbox-tools; got none")
	}
}

// TestBuildRunCreateOpts_FieldsPassthrough verifies scalar fields are forwarded correctly.
func TestBuildRunCreateOpts_FieldsPassthrough(t *testing.T) {
	imageRef := "docker.io/library/ubuntu:22.04"
	agentBytes := []byte("agent-binary")
	cacheRoot := t.TempDir()

	opts := buildRunCreateOpts(
		context.Background(),
		imageRef,
		agentBytes,
		cacheRoot,
		512, 2,
		true, // forceFlag
		true, // optOut — avoid real fetch
		fetchAlwaysGH,
	)

	if opts.Image.Ref != imageRef {
		t.Errorf("Image.Ref: got %q want %q", opts.Image.Ref, imageRef)
	}
	if string(opts.AgentBytes) != "agent-binary" {
		t.Error("AgentBytes not forwarded")
	}
	if opts.CacheRoot != cacheRoot {
		t.Errorf("CacheRoot: got %q want %q", opts.CacheRoot, cacheRoot)
	}
	if opts.MemoryMiB != 512 {
		t.Errorf("MemoryMiB: got %d want 512", opts.MemoryMiB)
	}
	if opts.VCPUs != 2 {
		t.Errorf("VCPUs: got %d want 2", opts.VCPUs)
	}
	if !opts.ForceDiskSpace {
		t.Error("ForceDiskSpace should be true")
	}
	if opts.ReachabilityTimeout != 30*time.Second {
		t.Errorf("ReachabilityTimeout: got %v want 30s", opts.ReachabilityTimeout)
	}
	// Confirm type for doc purposes.
	var _ service.CreateAndBootOptions = opts
}
