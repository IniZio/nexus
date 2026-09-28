package cli

import (
	"context"
	"testing"

	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

// TestBuildOrcaCreateOpts_SandboxTools_HasGH verifies that buildOrcaCreateOpts
// assigns SandboxTools. Deleting the opts.SandboxTools assignment in
// buildOrcaCreateOpts causes this test to fail.
func TestBuildOrcaCreateOpts_SandboxTools_HasGH(t *testing.T) {
	gh := toolcache.Fetched{Name: "gh", Version: "2.0.0", SHA256: "abc123"}
	fetch := func(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
		return gh, nil
	}

	orig := userGlobalSandboxToolsOptOut
	userGlobalSandboxToolsOptOut = func() bool { return false }
	t.Cleanup(func() { userGlobalSandboxToolsOptOut = orig })

	opts := buildOrcaCreateOpts(
		context.Background(),
		orcaEnv{InstanceID: "test-instance"},
		"ghcr.io/example/image:latest",
		"",
		"",
		nil,
		cred.AgentProfile{},
		fetch,
	)
	var found bool
	for _, f := range opts.SandboxTools {
		if f.Name == "gh" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected gh in opts.SandboxTools, got %v", opts.SandboxTools)
	}
}

// TestOrcaToolsForCreate_ImageRef_HasGH verifies that an OCI image ref causes
// orcaToolsForCreate to return the injected tool (gh). Deleting the
// imageOptsSandboxTools call inside orcaToolsForCreate makes this fail.
func TestOrcaToolsForCreate_ImageRef_HasGH(t *testing.T) {
	gh := toolcache.Fetched{Name: "gh", Version: "2.0.0", SHA256: "abc123"}
	fetch := func(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
		return gh, nil
	}

	got := orcaToolsForCreate(context.Background(), "ghcr.io/example/image:latest", false, fetch)
	if len(got) == 0 {
		t.Fatal("expected SandboxTools to be non-empty for OCI image ref")
	}
	var found bool
	for _, f := range got {
		if f.Name == "gh" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected gh in SandboxTools, got %v", got)
	}
}

// TestOrcaToolsForCreate_OptOut_Nil verifies that opt-out suppresses injection.
func TestOrcaToolsForCreate_OptOut_Nil(t *testing.T) {
	called := 0
	fetch := func(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
		called++
		return toolcache.Fetched{}, nil
	}

	got := orcaToolsForCreate(context.Background(), "ghcr.io/example/image:latest", true, fetch)
	if got != nil {
		t.Fatalf("expected nil when opted out, got %v", got)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called when opted out, got %d calls", called)
	}
}

// TestOrcaToolsForCreate_EmptyRef_Nil verifies that an empty image ref returns nil.
func TestOrcaToolsForCreate_EmptyRef_Nil(t *testing.T) {
	called := 0
	fetch := func(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
		called++
		return toolcache.Fetched{}, nil
	}

	got := orcaToolsForCreate(context.Background(), "", false, fetch)
	if got != nil {
		t.Fatalf("expected nil for empty image ref, got %v", got)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called for empty ref, got %d calls", called)
	}
}

// TestOrcaToolsForCreate_DigestRef_Nil verifies that a bare sha256 digest returns nil.
func TestOrcaToolsForCreate_DigestRef_Nil(t *testing.T) {
	called := 0
	fetch := func(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
		called++
		return toolcache.Fetched{}, nil
	}

	got := orcaToolsForCreate(context.Background(), "sha256:deadbeef", false, fetch)
	if got != nil {
		t.Fatalf("expected nil for digest-only ref, got %v", got)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called for digest ref, got %d calls", called)
	}
}
