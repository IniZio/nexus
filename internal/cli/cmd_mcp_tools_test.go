package cli

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/service"
)

// fakeFetch is a toolFetchFn that returns a canned Fetched without hitting
// the network. Used to verify SandboxTools wiring without real downloads.
func fakeFetch(_ context.Context, t toolcache.Tool, goarch string) (toolcache.Fetched, error) {
	return toolcache.Fetched{
		Name:   t.Name,
		GoArch: goarch,
	}, nil
}

// buildTestMCPService returns an mcpService with a fake driver, isolated
// store, and the fake fetch seam wired in.
func buildTestMCPService(t *testing.T) *mcpService {
	t.Helper()
	return &mcpService{
		Service:    newTestService(t),
		cacheRoot:  t.TempDir(),
		fetchTools: fakeFetch,
	}
}

// captureCreateAndBootOpts replaces mcpCreateAndBootFn with a stub that
// stores the incoming opts and returns a zero Sandbox without error. The
// caller must call restore() when done.
func captureCreateAndBootOpts(t *testing.T, dst *service.CreateAndBootOptions) (restore func()) {
	t.Helper()
	orig := mcpCreateAndBootFn
	mcpCreateAndBootFn = func(
		_ context.Context,
		_ *service.Service,
		_ *image.Cache,
		_ service.DriverFactory,
		_ service.ProbeFunc,
		_, _ string,
		opts service.CreateAndBootOptions,
	) (domain.Sandbox, error) {
		*dst = opts
		return domain.Sandbox{}, nil
	}
	return func() { mcpCreateAndBootFn = orig }
}

// captureRunEphemeralOpts replaces mcpRunEphemeralFn with a stub that stores
// the incoming opts and returns 0, nil without error.
func captureRunEphemeralOpts(t *testing.T, dst *service.CreateAndBootOptions) (restore func()) {
	t.Helper()
	orig := mcpRunEphemeralFn
	mcpRunEphemeralFn = func(
		_ context.Context,
		_ *service.Service,
		_ *image.Cache,
		_ service.DriverFactory,
		_ service.ProbeFunc,
		_, _ string,
		opts service.CreateAndBootOptions,
		_ service.ExecOptions,
		_ io.Reader,
		_, _ io.Writer,
	) (int32, error) {
		*dst = opts
		return 0, nil
	}
	return func() { mcpRunEphemeralFn = orig }
}

// setOptOut overrides userGlobalSandboxToolsOptOut and restores it on cleanup.
func setOptOut(t *testing.T, v bool) {
	t.Helper()
	orig := userGlobalSandboxToolsOptOut
	userGlobalSandboxToolsOptOut = func() bool { return v }
	t.Cleanup(func() { userGlobalSandboxToolsOptOut = orig })
}

// mcpKernelStub creates a stub kernel file and returns its path.
func mcpKernelStub(t *testing.T) string {
	t.Helper()
	p := t.TempDir() + "/vmlinux"
	if err := os.WriteFile(p, []byte("stub"), 0o600); err != nil {
		t.Fatalf("mcpKernelStub: %v", err)
	}
	return p
}

// TestMCPCreateAndBoot_ImageRef_InjectsSandboxTools asserts that when
// Image.Ref is set, CreateAndBoot wires a non-nil SandboxTools slice
// containing at least the "gh" tool.
func TestMCPCreateAndBoot_ImageRef_InjectsSandboxTools(t *testing.T) {
	t.Setenv("NEXUS_KERNEL_PATH", mcpKernelStub(t))
	setOptOut(t, false)

	msvc := buildTestMCPService(t)
	var captured service.CreateAndBootOptions
	restore := captureCreateAndBootOpts(t, &captured)
	defer restore()

	opts := service.CreateAndBootOptions{
		Image: service.ImageSpec{Ref: "nexus-base:test"},
	}
	_, _ = msvc.CreateAndBoot(context.Background(), "proj", "box", opts)

	if len(captured.SandboxTools) == 0 {
		t.Fatal("expected SandboxTools to be non-empty when Image.Ref is set")
	}
	hasGH := false
	for _, f := range captured.SandboxTools {
		if f.Name == "gh" {
			hasGH = true
		}
	}
	if !hasGH {
		t.Errorf("SandboxTools does not contain gh; got %v", captured.SandboxTools)
	}
}

// TestMCPRunEphemeral_ImageRef_InjectsSandboxTools asserts the same for
// RunEphemeral when Image.Ref is set.
func TestMCPRunEphemeral_ImageRef_InjectsSandboxTools(t *testing.T) {
	t.Setenv("NEXUS_KERNEL_PATH", mcpKernelStub(t))
	setOptOut(t, false)

	msvc := buildTestMCPService(t)
	var captured service.CreateAndBootOptions
	restore := captureRunEphemeralOpts(t, &captured)
	defer restore()

	opts := service.CreateAndBootOptions{
		Image: service.ImageSpec{Ref: "nexus-base:test"},
	}
	_, _, _, _ = msvc.RunEphemeral(context.Background(), "proj", "box", opts, nil, nil, "", "")

	if len(captured.SandboxTools) == 0 {
		t.Fatal("expected SandboxTools to be non-empty when Image.Ref is set")
	}
	hasGH := false
	for _, f := range captured.SandboxTools {
		if f.Name == "gh" {
			hasGH = true
		}
	}
	if !hasGH {
		t.Errorf("SandboxTools does not contain gh; got %v", captured.SandboxTools)
	}
}

// TestMCPCreateAndBoot_OptOut_NilSandboxTools asserts that when the user-global
// opt-out is set, SandboxTools is nil even with Image.Ref set.
func TestMCPCreateAndBoot_OptOut_NilSandboxTools(t *testing.T) {
	t.Setenv("NEXUS_KERNEL_PATH", mcpKernelStub(t))
	setOptOut(t, true)

	msvc := buildTestMCPService(t)
	var captured service.CreateAndBootOptions
	restore := captureCreateAndBootOpts(t, &captured)
	defer restore()

	opts := service.CreateAndBootOptions{
		Image: service.ImageSpec{Ref: "nexus-base:test"},
	}
	_, _ = msvc.CreateAndBoot(context.Background(), "proj", "box", opts)

	if len(captured.SandboxTools) != 0 {
		t.Errorf("expected nil SandboxTools on opt-out; got %v", captured.SandboxTools)
	}
}

// TestMCPCreateAndBoot_NoCacheRoot_NilSandboxTools asserts that when
// cacheRoot is "" and no fetchTools override is set, SandboxTools is nil and
// no "tools" directory is created in the working directory.
func TestMCPCreateAndBoot_NoCacheRoot_NilSandboxTools(t *testing.T) {
	t.Setenv("NEXUS_KERNEL_PATH", mcpKernelStub(t))
	setOptOut(t, false)

	// Change into a fresh temp dir so any accidental download under "./tools"
	// would be visible after the call.
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) }) //nolint:errcheck

	// cacheRoot="" and fetchTools=nil — the no-store-root path.
	msvc := &mcpService{
		Service:    newTestService(t),
		cacheRoot:  "",
		fetchTools: nil,
	}
	var captured service.CreateAndBootOptions
	restore := captureCreateAndBootOpts(t, &captured)
	defer restore()

	opts := service.CreateAndBootOptions{
		Image: service.ImageSpec{Ref: "nexus-base:test"},
	}
	_, _ = msvc.CreateAndBoot(context.Background(), "proj", "box", opts)

	if captured.SandboxTools != nil {
		t.Errorf("expected nil SandboxTools with empty cacheRoot; got %v", captured.SandboxTools)
	}
	if _, statErr := os.Stat(tmpDir + "/tools"); !os.IsNotExist(statErr) {
		t.Error("expected no 'tools' dir to be created in cwd, but it exists")
	}
}

// TestMCPRunEphemeral_NoCacheRoot_NilSandboxTools asserts the same for
// RunEphemeral when cacheRoot is "" and no fetchTools override is set.
func TestMCPRunEphemeral_NoCacheRoot_NilSandboxTools(t *testing.T) {
	t.Setenv("NEXUS_KERNEL_PATH", mcpKernelStub(t))
	setOptOut(t, false)

	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) }) //nolint:errcheck

	msvc := &mcpService{
		Service:    newTestService(t),
		cacheRoot:  "",
		fetchTools: nil,
	}
	var captured service.CreateAndBootOptions
	restore := captureRunEphemeralOpts(t, &captured)
	defer restore()

	opts := service.CreateAndBootOptions{
		Image: service.ImageSpec{Ref: "nexus-base:test"},
	}
	_, _, _, _ = msvc.RunEphemeral(context.Background(), "proj", "box", opts, nil, nil, "", "")

	if captured.SandboxTools != nil {
		t.Errorf("expected nil SandboxTools with empty cacheRoot; got %v", captured.SandboxTools)
	}
	if _, statErr := os.Stat(tmpDir + "/tools"); !os.IsNotExist(statErr) {
		t.Error("expected no 'tools' dir to be created in cwd, but it exists")
	}
}

// TestMCPCreateAndBoot_RootfsPath_NilSandboxTools asserts that when
// Image.RootfsPath is set (opaque ext4), SandboxTools is nil.
func TestMCPCreateAndBoot_RootfsPath_NilSandboxTools(t *testing.T) {
	t.Setenv("NEXUS_KERNEL_PATH", mcpKernelStub(t))
	setOptOut(t, false)

	msvc := buildTestMCPService(t)
	var captured service.CreateAndBootOptions
	restore := captureCreateAndBootOpts(t, &captured)
	defer restore()

	opts := service.CreateAndBootOptions{
		Image: service.ImageSpec{RootfsPath: "/some/rootfs.ext4"},
	}
	_, _ = msvc.CreateAndBoot(context.Background(), "proj", "box", opts)

	if len(captured.SandboxTools) != 0 {
		t.Errorf("expected nil SandboxTools for RootfsPath image; got %v", captured.SandboxTools)
	}
}
