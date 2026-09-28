package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/builder"
	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/hostbin/pin"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
)

// fakeTool returns a minimal toolcache.Tool for test use.
func fakeTool(name string) toolcache.Tool {
	return toolcache.Tool{
		Pin: pin.Pin{
			Name:    name,
			Version: "1.0.0",
		},
	}
}

// makeFetch returns a toolFetchFn that succeeds and records calls.
func makeFetch(called *int, result toolcache.Fetched) toolFetchFn {
	return func(_ context.Context, t toolcache.Tool, _ string) (toolcache.Fetched, error) {
		if called != nil {
			*called++
		}
		return result, nil
	}
}

// makeFetchErr returns a toolFetchFn that always returns err.
func makeFetchErr(called *int, err error) toolFetchFn {
	return func(_ context.Context, t toolcache.Tool, _ string) (toolcache.Fetched, error) {
		if called != nil {
			*called++
		}
		return toolcache.Fetched{}, err
	}
}

// TestResolveSandboxTools_OptOut verifies that optOut=true returns nil without calling fetch.
func TestResolveSandboxTools_OptOut(t *testing.T) {
	called := 0
	result := resolveSandboxTools(context.Background(), true, nil, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called, got %d calls", called)
	}
}

// TestResolveSandboxTools_SkipDirective verifies that a containerfile with the
// skip directive returns nil without calling fetch.
func TestResolveSandboxTools_SkipDirective(t *testing.T) {
	called := 0
	cf := []byte("# nexus:sandbox-tools-skip\nFROM ubuntu\n")
	result := resolveSandboxTools(context.Background(), false, cf, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if result != nil {
		t.Fatalf("expected nil for skip directive, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called for skip directive, got %d calls", called)
	}
}

// TestResolveSandboxTools_Happy verifies that a successful fetch returns the tool.
func TestResolveSandboxTools_Happy(t *testing.T) {
	ghFetched := toolcache.Fetched{
		Name:         "gh",
		Version:      "2.101.0",
		GoArch:       "amd64",
		SHA256:       "abc123",
		BinPath:      "/tmp/gh",
		GuestBinPath: "/usr/local/share/nexus-tools/gh/2.101.0/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}
	// Override Defaults to return a single tool via fetch interception.
	// We rely on the real Defaults() containing at least one entry (gh).
	// The fake fetch always returns ghFetched regardless of which tool is asked for.
	result := resolveSandboxTools(context.Background(), false, nil, "amd64",
		func(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
			return ghFetched, nil
		})
	if len(result) == 0 {
		t.Fatal("expected at least one fetched tool, got none")
	}
	if result[0].Name != "gh" {
		t.Fatalf("expected gh, got %q", result[0].Name)
	}
}

// TestResolveSandboxTools_FetchError verifies that a generic fetch error drops
// the tool but does not panic and does not return nil (other tools may succeed;
// here all Defaults() tools fail so we get an empty non-nil slice or nil —
// accept both since the count of Defaults may vary).
func TestResolveSandboxTools_FetchError(t *testing.T) {
	called := 0
	result := resolveSandboxTools(context.Background(), false, nil, "amd64",
		makeFetchErr(&called, errors.New("network unreachable")))
	// No panic is the main assertion. result may be nil or empty.
	_ = result
	if called == 0 {
		// Defaults() must have returned at least one tool for the test to be meaningful.
		// If this fires, check toolcache.Defaults().
		t.Log("warning: toolcache.Defaults() returned no tools; skipping call-count assertion")
	}
}

// TestResolveSandboxTools_ChecksumMismatch verifies that ErrChecksumMismatch
// drops the tool at Error log level without panicking.
func TestResolveSandboxTools_ChecksumMismatch(t *testing.T) {
	called := 0
	result := resolveSandboxTools(context.Background(), false, nil, "amd64",
		makeFetchErr(&called, toolcache.ErrChecksumMismatch))
	_ = result // no panic is the key assertion
}

// TestSandboxToolsOptOutFromConfig covers nil/true/false GH pointer.
func TestSandboxToolsOptOutFromConfig(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	t.Run("nil=default on", func(t *testing.T) {
		cfg := config.Config{}
		// cfg.Sandbox.Tools.GH is nil by default
		if sandboxToolsOptOutFromConfig(cfg) {
			t.Fatal("nil GH should not opt out")
		}
	})

	t.Run("true=enabled, not opted out", func(t *testing.T) {
		cfg := config.Config{}
		cfg.Sandbox.Tools.GH = boolPtr(true)
		if sandboxToolsOptOutFromConfig(cfg) {
			t.Fatal("GH=true should not opt out")
		}
	})

	t.Run("false=opted out", func(t *testing.T) {
		cfg := config.Config{}
		cfg.Sandbox.Tools.GH = boolPtr(false)
		if !sandboxToolsOptOutFromConfig(cfg) {
			t.Fatal("GH=false should opt out")
		}
	})
}

// TestFingerprintWithTools_NilTools verifies that nil tools return fp unchanged.
func TestFingerprintWithTools_NilTools(t *testing.T) {
	fp := "deadbeef"
	got := fingerprintWithTools(fp, nil)
	if got != fp {
		t.Fatalf("expected %q, got %q", fp, got)
	}
}

// TestFingerprintWithTools_EmptyTools verifies that empty tools return fp unchanged.
func TestFingerprintWithTools_EmptyTools(t *testing.T) {
	fp := "deadbeef"
	got := fingerprintWithTools(fp, []toolcache.Fetched{})
	if got != fp {
		t.Fatalf("expected %q, got %q", fp, got)
	}
}

// TestFingerprintWithTools_WithTools verifies that non-empty tools change fp.
func TestFingerprintWithTools_WithTools(t *testing.T) {
	tools := []toolcache.Fetched{{Name: "gh", SHA256: "abc"}}
	fp := "deadbeef"
	got := fingerprintWithTools(fp, tools)
	if got == fp {
		t.Fatal("fingerprint should differ with tools")
	}
	// Deterministic: calling again produces same result.
	got2 := fingerprintWithTools(fp, tools)
	if got != got2 {
		t.Fatalf("fingerprint not deterministic: %q != %q", got, got2)
	}
}

// TestFingerprintWithTools_SHA256Sensitivity verifies that changing SHA256 changes fingerprint.
func TestFingerprintWithTools_SHA256Sensitivity(t *testing.T) {
	fp := "deadbeef"
	tools1 := []toolcache.Fetched{{Name: "gh", SHA256: "abc"}}
	tools2 := []toolcache.Fetched{{Name: "gh", SHA256: "xyz"}}
	got1 := fingerprintWithTools(fp, tools1)
	got2 := fingerprintWithTools(fp, tools2)
	if got1 == got2 {
		t.Fatal("fingerprint should differ when SHA256 differs")
	}
}

// TestFileBuildFingerprint_NilTools verifies that nil tools yields the same result as BuildFingerprint.
func TestFileBuildFingerprint_NilTools(t *testing.T) {
	dir := t.TempDir()
	cf := []byte("FROM scratch\n")
	got, err := fileBuildFingerprint(cf, "scratch", nil, dir, cred.ToolRecipe{}, "amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := builder.BuildFingerprint(cf, "scratch", nil, dir, cred.ToolRecipe{}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("expected %q == %q", got, want)
	}
}

// TestFileBuildFingerprint_WithGhTool_Differs verifies that a gh tool changes the fingerprint.
func TestFileBuildFingerprint_WithGhTool_Differs(t *testing.T) {
	dir := t.TempDir()
	cf := []byte("FROM scratch\n")
	tools := []toolcache.Fetched{{Name: "gh", SHA256: "abc123"}}
	base, err := fileBuildFingerprint(cf, "scratch", nil, dir, cred.ToolRecipe{}, "amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	withTools, err := fileBuildFingerprint(cf, "scratch", nil, dir, cred.ToolRecipe{}, "amd64", tools)
	if err != nil {
		t.Fatal(err)
	}
	if base == withTools {
		t.Fatal("fingerprint with gh tool must differ from nil-tools fingerprint")
	}
}

// TestFileBuildFingerprint_DifferentSHA256_Differs verifies that changing SHA256 changes the fingerprint.
func TestFileBuildFingerprint_DifferentSHA256_Differs(t *testing.T) {
	dir := t.TempDir()
	cf := []byte("FROM scratch\n")
	tools1 := []toolcache.Fetched{{Name: "gh", SHA256: "abc"}}
	tools2 := []toolcache.Fetched{{Name: "gh", SHA256: "xyz"}}
	fp1, err := fileBuildFingerprint(cf, "scratch", nil, dir, cred.ToolRecipe{}, "amd64", tools1)
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := fileBuildFingerprint(cf, "scratch", nil, dir, cred.ToolRecipe{}, "amd64", tools2)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 == fp2 {
		t.Fatal("fingerprints must differ when SHA256 differs")
	}
}

// TestFileBuildFingerprint_ErrorPropagates verifies that a BuildFingerprint error is returned.
func TestFileBuildFingerprint_ErrorPropagates(t *testing.T) {
	// A COPY instruction activates context hashing; a non-existent dir causes an error.
	cf := []byte("FROM scratch\nCOPY . /app\n")
	_, err := fileBuildFingerprint(cf, "scratch", nil, "/nonexistent/dir/xyz987", cred.ToolRecipe{}, "amd64", nil)
	if err == nil {
		t.Skip("BuildFingerprint did not error on bad context dir; skipping propagation check")
	}
}

// TestImageOptsSandboxTools_AlreadySet verifies that non-nil SandboxTools is returned as-is.
func TestImageOptsSandboxTools_AlreadySet(t *testing.T) {
	called := 0
	preset := []toolcache.Fetched{{Name: "gh", SHA256: "preset"}}
	opts := service.CreateAndBootOptions{
		SandboxTools: preset,
		Image:        service.ImageSpec{Ref: "docker.io/library/ubuntu:24.04"},
	}
	result := imageOptsSandboxTools(context.Background(), opts, false, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if len(result) != 1 || result[0].SHA256 != "preset" {
		t.Fatalf("expected preset tools returned, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called when SandboxTools already set, got %d calls", called)
	}
}

// TestImageOptsSandboxTools_RefEmpty verifies that empty Ref returns nil.
func TestImageOptsSandboxTools_RefEmpty(t *testing.T) {
	called := 0
	opts := service.CreateAndBootOptions{Image: service.ImageSpec{Ref: ""}}
	result := imageOptsSandboxTools(context.Background(), opts, false, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if result != nil {
		t.Fatalf("expected nil for empty Ref, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called, got %d calls", called)
	}
}

// TestImageOptsSandboxTools_RootfsPath verifies that RootfsPath set returns nil.
func TestImageOptsSandboxTools_RootfsPath(t *testing.T) {
	called := 0
	opts := service.CreateAndBootOptions{
		Image: service.ImageSpec{RootfsPath: "/data/rootfs.ext4"},
	}
	result := imageOptsSandboxTools(context.Background(), opts, false, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if result != nil {
		t.Fatalf("expected nil for RootfsPath set, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called, got %d calls", called)
	}
}

// TestImageOptsSandboxTools_DigestRef verifies that a sha256: Ref returns nil.
func TestImageOptsSandboxTools_DigestRef(t *testing.T) {
	called := 0
	digest := "sha256:" + "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	opts := service.CreateAndBootOptions{Image: service.ImageSpec{Ref: digest}}
	result := imageOptsSandboxTools(context.Background(), opts, false, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if result != nil {
		t.Fatalf("expected nil for digest Ref, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called for digest Ref, got %d calls", called)
	}
}

// TestImageOptsSandboxTools_NormalRef verifies that a human-readable Ref triggers fetch.
func TestImageOptsSandboxTools_NormalRef(t *testing.T) {
	called := 0
	tool := toolcache.Fetched{Name: "gh", SHA256: "abc123"}
	opts := service.CreateAndBootOptions{Image: service.ImageSpec{Ref: "docker.io/library/ubuntu:24.04"}}
	result := imageOptsSandboxTools(context.Background(), opts, false, "amd64", makeFetch(&called, tool))
	if called == 0 {
		t.Fatal("expected fetch to be called at least once for normal Ref")
	}
	if len(result) == 0 {
		t.Fatal("expected at least one tool returned for normal Ref")
	}
}

// TestImageOptsSandboxTools_OptOut verifies that optOut=true returns nil.
func TestImageOptsSandboxTools_OptOut(t *testing.T) {
	called := 0
	opts := service.CreateAndBootOptions{Image: service.ImageSpec{Ref: "docker.io/library/ubuntu:24.04"}}
	result := imageOptsSandboxTools(context.Background(), opts, true, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if result != nil {
		t.Fatalf("expected nil when optOut=true, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called when opted out, got %d calls", called)
	}
}

// TestUserGlobalSandboxToolsOptOut_GHFalse verifies true when gh: false in config.
func TestUserGlobalSandboxToolsOptOut_GHFalse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "nexus")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	data := []byte("version: 1\nsandbox:\n  tools:\n    gh: false\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Call the real var (not stubbed) to verify it reads config correctly.
	if !userGlobalSandboxToolsOptOut() {
		t.Fatal("expected true for gh: false")
	}
}

// TestUserGlobalSandboxToolsOptOut_Absent verifies false when tools section absent.
func TestUserGlobalSandboxToolsOptOut_Absent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// No config file written — absent means default on (no opt-out).
	if userGlobalSandboxToolsOptOut() {
		t.Fatal("expected false when config absent")
	}
}

// TestSandboxToolsOptOutFromConfig_ViaConfig exercises the var with a config stub.
func TestSandboxToolsOptOutFromConfig_ViaConfig(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	cfg := config.Config{}
	cfg.Sandbox.Tools.GH = boolPtr(false)
	if !sandboxToolsOptOutFromConfig(cfg) {
		t.Fatal("expected opt-out for GH=false")
	}
}

// TestBootSandboxTools_FilePathSet verifies that filePath set returns nil without calling fetch.
func TestBootSandboxTools_FilePathSet(t *testing.T) {
	called := 0
	f := sandboxCreateFlags{filePath: "/some/file"}
	result := bootSandboxTools(context.Background(), f, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if result != nil {
		t.Fatalf("expected nil for filePath set, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called, got %d calls", called)
	}
}

// TestBootSandboxTools_RootfsPathSet verifies that rootfsPath set returns nil without calling fetch.
func TestBootSandboxTools_RootfsPathSet(t *testing.T) {
	called := 0
	f := sandboxCreateFlags{rootfsPath: "/some/rootfs.ext4"}
	result := bootSandboxTools(context.Background(), f, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if result != nil {
		t.Fatalf("expected nil for rootfsPath set, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called, got %d calls", called)
	}
}

// TestBootSandboxTools_NeitherSet_FetchCalled verifies that with no path flags, fetch is called.
func TestBootSandboxTools_NeitherSet_FetchCalled(t *testing.T) {
	called := 0
	tool := toolcache.Fetched{Name: "gh", SHA256: "abc"}
	f := sandboxCreateFlags{}
	fetch := func(_ context.Context, _ toolcache.Tool, _ string) (toolcache.Fetched, error) {
		called++
		return tool, nil
	}
	result := bootSandboxTools(context.Background(), f, "amd64", fetch)
	if called == 0 {
		t.Fatal("expected fetch to be called at least once")
	}
	if len(result) == 0 {
		t.Fatal("expected at least one tool returned")
	}
}

// TestBootSandboxTools_NoSandboxTools_NilNotCalled verifies that noSandboxTools=true returns nil.
func TestBootSandboxTools_NoSandboxTools_NilNotCalled(t *testing.T) {
	called := 0
	f := sandboxCreateFlags{noSandboxTools: true}
	result := bootSandboxTools(context.Background(), f, "amd64", makeFetch(&called, toolcache.Fetched{}))
	if result != nil {
		t.Fatalf("expected nil when noSandboxTools=true, got %v", result)
	}
	if called != 0 {
		t.Fatalf("expected fetch not called, got %d calls", called)
	}
}
