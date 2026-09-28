package cli

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/builder/toolcache"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/service"
)

// fakeGHFetch returns a toolFetchFn that always returns a Fetched with Name="gh".
func fakeGHFetch(calls *int) toolFetchFn {
	return func(_ context.Context, t toolcache.Tool, _ string) (toolcache.Fetched, error) {
		if calls != nil {
			*calls++
		}
		return toolcache.Fetched{Name: "gh"}, nil
	}
}

// newLaunchToolsTestDeps builds a launchDeps suitable for sandbox-tools tests.
// createAndBoot is replaced with a stub that captures the opts and returns a
// zero-value sandbox — no real VM is ever booted.
func newLaunchToolsTestDeps(t *testing.T, capOpts *service.CreateAndBootOptions) launchDeps {
	t.Helper()

	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	return launchDeps{
		svc:      newTestHerdrService(t),
		imgCache: cache,
		newDriver: service.DriverFactory(func(_ string, _ []service.ExtraDisk) (driver.Driver, error) {
			return fake.New(), nil
		}),
		probe:            func(context.Context, driver.Driver, domain.SandboxID) error { return nil },
		storeRoot:        t.TempDir(),
		kernelPath:       "/nonexistent/vmlinux",
		cacheRoot:        cacheRoot,
		capturedDiskPath: func() string { return "" },

		handoff: func(_ context.Context, _ *service.Service, _ domain.Sandbox,
			_, _, _ string, _ []string) (*os.File, string, error) {
			return nil, t.TempDir() + "/supervisor.sock", nil
		},
		verifySeed:     func(context.Context, *service.Service, string) error { return nil },
		stopSupervisor: func(context.Context, string) error { return nil },
		waitForExit:    func(context.Context, string) error { return nil },
		execInGuest: func(_ context.Context, _ string, _ agent.ExecOptions) (int32, error) {
			return 0, nil
		},

		createAndBoot: func(
			ctx context.Context,
			svc *service.Service,
			cache *image.Cache,
			newDriver service.DriverFactory,
			probe service.ProbeFunc,
			project, name string,
			opts service.CreateAndBootOptions,
		) (domain.Sandbox, error) {
			if capOpts != nil {
				*capOpts = opts
			}
			// Return a minimal sandbox so runHerdrLaunch can proceed.
			return domain.Sandbox{ID: domain.NewSandboxID()}, nil
		},
	}
}

// TestRunHerdrLaunch_SandboxTools_ImageRef verifies that booting from an OCI
// image ref causes SandboxTools to be populated via the fake fetch function.
// Removing the imageOptsSandboxTools assignment in runHerdrLaunch makes this RED.
func TestRunHerdrLaunch_SandboxTools_ImageRef(t *testing.T) {
	var calls int
	var capOpts service.CreateAndBootOptions

	d := newLaunchToolsTestDeps(t, &capOpts)
	d.fetchTools = fakeGHFetch(&calls)

	// Stub opt-out to false (default on).
	orig := userGlobalSandboxToolsOptOut
	userGlobalSandboxToolsOptOut = func() bool { return false }
	defer func() { userGlobalSandboxToolsOptOut = orig }()

	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)
	// Use "launch-test-base" which matches what the image cache knows about.
	// Any non-empty, non-digest ref triggers tool injection.
	const ref = "ociref/base:latest"
	_ = runHerdrLaunch(context.Background(), d, ref, []string{"/bin/true"}, false, out)

	if calls == 0 {
		t.Error("fetch was never called — SandboxTools not wired in runHerdrLaunch")
	}
	if len(capOpts.SandboxTools) == 0 {
		t.Fatal("SandboxTools is empty; expected at least the gh tool to be injected")
	}
	var hasGH bool
	for _, f := range capOpts.SandboxTools {
		if f.Name == "gh" {
			hasGH = true
			break
		}
	}
	if !hasGH {
		t.Errorf("SandboxTools = %v, want entry with Name=gh", capOpts.SandboxTools)
	}
}

// TestRunHerdrLaunch_SandboxTools_OptOut verifies that when the user has opted
// out of sandbox tools, SandboxTools is nil (not injected).
func TestRunHerdrLaunch_SandboxTools_OptOut(t *testing.T) {
	var calls int
	var capOpts service.CreateAndBootOptions

	d := newLaunchToolsTestDeps(t, &capOpts)
	d.fetchTools = fakeGHFetch(&calls)

	// Stub opt-out to true.
	orig := userGlobalSandboxToolsOptOut
	userGlobalSandboxToolsOptOut = func() bool { return true }
	defer func() { userGlobalSandboxToolsOptOut = orig }()

	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)
	const ref = "ociref/base:latest"
	_ = runHerdrLaunch(context.Background(), d, ref, []string{"/bin/true"}, false, out)

	if calls != 0 {
		t.Errorf("fetch called %d times despite opt-out; want 0", calls)
	}
	if capOpts.SandboxTools != nil {
		t.Errorf("SandboxTools = %v, want nil when opted out", capOpts.SandboxTools)
	}
}
