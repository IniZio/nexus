//go:build integration

package selfhost

// tracer_launch_test.go — Wave-0 tracer for S-LAUNCH.
//
// Proves the herdr→boot→exec control path end-to-end against the self-host
// base image BEFORE Wave-1 builds the real agent image. Three assertions:
//
//  1. echo hello round-trips (stdout contains "hello") — basic exec path alive.
//  2. Write a file to /root then read it back — per-sandbox CoW disk is writable.
//  3. Auth-header probe finding recorded: UNKNOWN (see below).
//
// Auth-header probe finding (D-P4-02 empirical status): UNKNOWN.
// CreateAndBootOptions.Broker is nil, so service.startSupervisor is never
// called — proxy.go's OnRequest swap hook is NOT on the data path at all.
// Code inspection confirms proxy.go (~line 146) OnRequest DOES intercept
// "Authorization: Bearer" headers on allowlisted hosts and calls
// broker.ResolveScoped to swap the placeholder for the real token. Empirical
// YES/NO confirmation deferred to S-EGRESS where a Broker will be wired.
//
// Skip conditions (same as TestSelfHostE2E):
//   - /dev/kvm absent or inaccessible
//   - cloud-hypervisor binary not found
//   - mke2fs not found in PATH
//   - images/kernel/vmlinux-x86_64 or NEXUS3_KERNEL_PATH not set
//   - docker unavailable (needed to build the base image)

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

func TestTracerLaunch(t *testing.T) {
	// ── skip guards ────────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: build/obtain the self-host base image ─────────────────────────
	t.Log("setting up image cache …")
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	t.Log("building self-host base image (first run: 5–10 min; subsequent: seconds) …")
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer imgCancel()

	img, err := BuildSelfHostBaseImage(imgCtx, cache)
	if err != nil {
		switch {
		case errors.Is(err, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", err)
		case errors.Is(err, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", err)
		}
		t.Fatalf("BuildSelfHostBaseImage: %v", err)
	}
	t.Logf("base image ready: digest=%s size=%.0f MiB", img.Digest, float64(img.Size)/(1024*1024))

	// ── Step 2: boot a sandbox ─────────────────────────────────────────────────

	// Socket dir in /tmp to stay within the 107-byte Linux sun_path limit.
	socketDir, err := os.MkdirTemp("/tmp", "tracer-launch-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir too long for unix socket: %s", socketDir)
	}

	storeRoot := t.TempDir()
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	// svcDrv is used by the Service for lifecycle operations (Remove).
	// DialGuest on svcDrv works because it derives the vsock path from
	// SocketDir+SandboxID — the same path written by bootDrv.
	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
	})
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// Remove internally stops the VM.
			if err := svc.Remove(rmCtx, sandboxID.String()); err != nil {
				t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, err)
			} else {
				t.Logf("cleanup: sandbox %s removed", sandboxID)
			}
		}
		os.RemoveAll(socketDir)
	})

	// bootDrv is captured by the factory for use in agentClient below.
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(resolvedExt4 string, _ []service.ExtraDisk) (driver.Driver, error) {
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:    chBin,
			SocketDir:     socketDir,
			KernelPath:    kernelPath,
			DiskImagePath: resolvedExt4,
		})
		return bootDrv, newErr
	})

	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting sandbox …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()

	sb, err := service.CreateAndBoot(
		bootCtx,
		svc,
		cache,
		factory,
		probe,
		"tracer", "launch",
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("sandbox booted: id=%s state=%s", sb.ID, sb.State)

	if sb.State != domain.Running {
		t.Fatalf("expected state=Running after CreateAndBoot, got %s", sb.State)
	}

	// Wait for the agent to be fully reachable before issuing RPCs.
	t.Log("waiting for guest agent …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable")

	ac := agent.NewClient(bootDrv, sb.ID)
	execCtx, execCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer execCancel()

	// ── Assertion 1: echo hello round-trip ────────────────────────────────────
	// Use absolute paths — the guest exec env has no PATH set by default.
	t.Log("exec: /bin/echo hello …")
	var echoOut bytes.Buffer
	exitCode, err := ac.Exec(execCtx, agent.ExecOptions{
		Argv:   []string{"/bin/echo", "hello"},
		Stdout: &echoOut,
	})
	if err != nil {
		t.Fatalf("exec echo hello: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("echo hello: exit code %d", exitCode)
	}
	if !strings.Contains(echoOut.String(), "hello") {
		t.Fatalf("echo hello: stdout %q does not contain 'hello'", echoOut.String())
	}
	t.Logf("echo hello: stdout=%q", strings.TrimSpace(echoOut.String()))

	// ── Assertion 2: write file to $HOME, read it back (proves CoW writable) ──
	// Uses $HOME (injected via Env) — satisfies acceptance criterion 3 verbatim.
	t.Log("exec: write and read $HOME/tracer.txt …")
	const tracerContent = "tracer-ok"
	var fileOut bytes.Buffer
	exitCode, err = ac.Exec(execCtx, agent.ExecOptions{
		Argv: []string{"/bin/sh", "-c", `printf '%s' 'tracer-ok' > "$HOME/tracer.txt" && cat "$HOME/tracer.txt"`},
		Env:  map[string]string{"HOME": "/root"},
		Stdout: &fileOut,
	})
	if err != nil {
		t.Fatalf("exec write+read $HOME/tracer.txt: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("write+read $HOME/tracer.txt: exit code %d", exitCode)
	}
	if !strings.Contains(fileOut.String(), tracerContent) {
		t.Fatalf("$HOME/tracer.txt: got %q, want it to contain %q", fileOut.String(), tracerContent)
	}
	t.Logf("CoW writable: $HOME/tracer.txt content=%q", fileOut.String())

	// ── Discriminator: agent's initial HOME (pre-injection) ───────────────────
	// Run with no Env to measure the agent binary's own pre-injection HOME.
	var bareHomeOut bytes.Buffer
	_, _ = ac.Exec(execCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", `printf '[%s]' "$HOME"`},
		Stdout: &bareHomeOut,
	})
	bareHome := strings.TrimSpace(bareHomeOut.String())
	t.Logf("discriminator: agent initial HOME (pre-injection)=%s", bareHome)

	// ── Assertion 4: verify control.go replace semantics ─────────────────────
	// Run /usr/bin/env with HOME=/root injected and count HOME= lines.
	// Replace semantics (control.go fix): exactly one HOME= line == HOME=/root.
	// Append-only (old bug): two HOME= lines, glibc getenv returns the first (/).
	t.Log("exec: verify Env replace semantics via /usr/bin/env …")
	var envOut bytes.Buffer
	envExitCode, envErr := svc.Exec(execCtx, sb.ID.String(), agent.ExecOptions{
		Argv: []string{"/usr/bin/env"},
		Env: map[string]string{
			"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"HOME": "/root",
		},
		Stdout: &envOut,
	})
	if envErr != nil {
		t.Fatalf("env replace-semantics: exec /usr/bin/env: %v", envErr)
	}
	if envExitCode != 0 {
		t.Fatalf("env replace-semantics: /usr/bin/env exit code %d", envExitCode)
	}
	var homeLines []string
	for _, line := range strings.Split(envOut.String(), "\n") {
		if strings.HasPrefix(line, "HOME=") {
			homeLines = append(homeLines, strings.TrimSpace(line))
		}
	}
	if len(homeLines) != 1 {
		t.Fatalf("env replace-semantics: got %d HOME= lines, want 1 (append bug?): %v", len(homeLines), homeLines)
	}
	if homeLines[0] != "HOME=/root" {
		t.Fatalf("env replace-semantics: HOME line = %q, want HOME=/root", homeLines[0])
	}
	t.Logf("env replace-semantics: exactly one HOME= line = %q ✓ (control.go replace works: glibc getenv sees /root)", homeLines[0])

	// ── Assertion 3: svc.Exec + Env plumbing (different-driver-instance dial) ──
	// Two things verified in one exec:
	//  a) svc.Exec (svc.driver, different CHDriver instance) can dial a VM
	//     booted by the factory-created bootDrv — both share SocketDir, so
	//     DialGuest resolves the vsock path by filesystem (SocketDir+ID).
	//  b) Env entries are injected into the guest process environment: HOME is
	//     set in the child via req.Env merge (control.go:39 appends to agent's
	//     os.Environ). exec.Command uses the agent binary's own PATH for initial
	//     lookup, so argv[0] must be an absolute path (/bin/sh); bare names
	//     fail regardless of the injected PATH.
	t.Log("exec: via svc.Exec + Env plumbing (different-driver-instance dial) …")
	var svcOut bytes.Buffer
	svcExitCode, svcErr := svc.Exec(execCtx, sb.ID.String(), agent.ExecOptions{
		Argv: []string{"/bin/sh", "-c", `printf '%s' "$HOME"`},
		Env: map[string]string{
			"HOME": "/root",
		},
		Stdout: &svcOut,
	})
	if svcErr != nil {
		t.Fatalf("svc.Exec Env-plumbing: %v", svcErr)
	}
	if svcExitCode != 0 {
		t.Fatalf("svc.Exec Env-plumbing: exit code %d", svcExitCode)
	}
	got := strings.TrimSpace(svcOut.String())
	if got != "/root" {
		t.Fatalf("svc.Exec Env-plumbing: HOME=%q want %q (HOME not plumbed into guest env)", got, "/root")
	}
	t.Logf("svc.Exec Env-plumbing: HOME=%q ✓ (Env merged into guest process)", got)

	// ── Auth-header probe finding (D-P4-02) ───────────────────────────────────
	// UNKNOWN — proxy.go OnRequest not active: no Broker wired in this test.
	// See package-level comment for full rationale.
	t.Log("auth-header probe: UNKNOWN — no Broker configured, MITM proxy not active; deferred to S-EGRESS")
}
