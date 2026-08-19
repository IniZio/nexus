package cli

// TBD-PD-31: drive the real launch path with a fake driver and observe whether
// the perimeter handoff actually happens.
//
// This replaces an AST tripwire that parsed cmd_herdr_plugin.go and asserted the
// three wiring calls appeared in it. That proved the call sites EXISTED — not
// that they run, not that their arguments are right, not that they run only on
// the egress path. These tests run the function.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/service"
)

// launchProbe records what the injected launch path did.
type launchProbe struct {
	handoffCalls int
	handoffDisk  string
	verifyCalls  int
	stopCalls    int
	execArgv     []string
	handoffErr   error
}

// newLaunchTestDeps builds a launchDeps backed by a fake driver and an
// in-memory store. Nothing here spawns a process or boots a VM.
func newLaunchTestDeps(t *testing.T, p *launchProbe) (launchDeps, string) {
	t.Helper()

	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	content := []byte("fake-ext4-rootfs")
	sum := sha256.Sum256(content)
	img := domain.Image{
		Digest: domain.MustDigest(fmt.Sprintf("sha256:%x", sum)),
		Ref:    "launch-test-base",
		Kind:   domain.KindBase,
	}
	if err := cache.Put(context.Background(), img, bytes.NewReader(content)); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}

	var capturedDisk string
	d := launchDeps{
		svc:      newTestHerdrService(t),
		imgCache: cache,
		newDriver: service.DriverFactory(func(ext4Path string, _ []service.ExtraDisk) (driver.Driver, error) {
			capturedDisk = ext4Path
			return fake.New(), nil
		}),
		probe:      func(context.Context, driver.Driver, domain.SandboxID) error { return nil },
		storeRoot:  t.TempDir(),
		kernelPath: "/nonexistent/vmlinux",
		cacheRoot:  cacheRoot,

		capturedDiskPath: func() string { return capturedDisk },

		handoff: func(_ context.Context, _ *service.Service, _ domain.Sandbox,
			_, _, diskPath string, _ []string) (*os.File, string, error) {
			p.handoffCalls++
			p.handoffDisk = diskPath
			if p.handoffErr != nil {
				return nil, "", p.handoffErr
			}
			// A non-empty socket path drives the teardown branch.
			return nil, t.TempDir() + "/supervisor.sock", nil
		},
		verifySeed: func(context.Context, *service.Service, string) error {
			p.verifyCalls++
			return nil
		},
		stopSupervisor: func(context.Context, string) error { p.stopCalls++; return nil },
		waitForExit:    func(context.Context, string) error { return nil },
		execInGuest: func(_ context.Context, _ string, opts agent.ExecOptions) (int32, error) {
			p.execArgv = opts.Argv
			return 0, nil
		},
	}
	return d, img.Ref
}

// The load-bearing test: --agent-egress must hand the VM to a supervisor.
// Deleting the handoff call makes this RED — which the AST tripwire could only
// approximate.
func TestRunHerdrLaunch_AgentEgress_HandsOffToSupervisor(t *testing.T) {
	var p launchProbe
	d, ref := newLaunchTestDeps(t, &p)
	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)

	if err := runHerdrLaunch(context.Background(), d, ref, []string{"/usr/local/bin/claude"}, true, out); err != nil {
		t.Fatalf("runHerdrLaunch: %v", err)
	}

	if p.handoffCalls != 1 {
		t.Errorf("perimeter handoff ran %d times, want exactly 1 — without it the sandbox has no proxy and no credential", p.handoffCalls)
	}
	if p.verifyCalls != 1 {
		t.Errorf("seed verification ran %d times, want exactly 1", p.verifyCalls)
	}
	// The supervisor re-boots from the same ext4 the CLI booted; handing it the
	// wrong path (or an empty one) is a silent boot of the wrong disk.
	if p.handoffDisk == "" || p.handoffDisk != d.capturedDiskPath() {
		t.Errorf("handoff disk path = %q, want the path the driver factory captured (%q)", p.handoffDisk, d.capturedDiskPath())
	}
	// The guest command must source the supervisor-seeded credential file.
	if len(p.execArgv) == 0 || p.execArgv[0] != "/bin/sh" {
		t.Errorf("exec argv = %v, want the credential-sourcing shell wrapper", p.execArgv)
	}
	if p.stopCalls != 1 {
		t.Errorf("supervisor stopped %d times, want exactly 1 on teardown", p.stopCalls)
	}
}

// Without --agent-egress nothing may be handed off, and the command must run
// verbatim: wrapping it in the credential shell would source a file that the
// plain path never seeds.
func TestRunHerdrLaunch_NoEgress_NoHandoff(t *testing.T) {
	var p launchProbe
	d, ref := newLaunchTestDeps(t, &p)
	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)

	if err := runHerdrLaunch(context.Background(), d, ref, []string{"/bin/true"}, false, out); err != nil {
		t.Fatalf("runHerdrLaunch: %v", err)
	}

	if p.handoffCalls != 0 {
		t.Errorf("perimeter handoff ran %d times without --agent-egress, want 0", p.handoffCalls)
	}
	if p.verifyCalls != 0 {
		t.Errorf("seed verification ran %d times without --agent-egress, want 0", p.verifyCalls)
	}
	if len(p.execArgv) != 1 || p.execArgv[0] != "/bin/true" {
		t.Errorf("exec argv = %v, want the caller's argv verbatim", p.execArgv)
	}
}

// A failed handoff must abort the launch. Running the agent anyway would reach
// the API with an unexchanged placeholder and fail with an error that says
// nothing about the cause.
func TestRunHerdrLaunch_HandoffFailure_AbortsBeforeExec(t *testing.T) {
	p := launchProbe{handoffErr: errors.New("spawn supervisor: boom")}
	d, ref := newLaunchTestDeps(t, &p)
	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)

	err := runHerdrLaunch(context.Background(), d, ref, []string{"/usr/local/bin/claude"}, true, out)
	if err == nil {
		t.Fatal("expected an error when the perimeter handoff fails, got nil")
	}
	if p.execArgv != nil {
		t.Errorf("the agent ran despite a failed handoff: argv = %v", p.execArgv)
	}
}
