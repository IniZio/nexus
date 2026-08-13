//go:build integration

package selfhost

// herdr_hello_test.go — S0 (Wave 0 tracer, re-scoped): herdr→sandbox hello-world proof.
//
// Proves the herdr integration works for a SIMPLE, non-nested sandbox before
// any nested-boot (S0N) work is attempted. The test mirrors the exact call
// sequence that herdrPluginLaunch (internal/cli/cmd_herdr_plugin.go:271) uses:
//
//  1. service.CreateAndBoot with motiveID="herdr", image digest, 60 s
//     reachability timeout.
//  2. svc.Exec with an ABSOLUTE argv (["/bin/echo", wantMsg]) and an explicit
//     PATH+HOME environment — the guest agent does NOT do PATH lookup.
//  3. Capture stdout, assert it contains the expected message.
//  4. svc.Remove on cleanup — mirrors the defer in herdrPluginLaunch.
//
// The herdr binary is NOT required for this proof; `__herdr-plugin launch` is
// the nexus3-side of the herdr contract and this test exercises that same
// service-layer call path directly, keeping image cache and store isolated to
// t.TempDir() (herdrPluginLaunch uses ~/.nexus3 in production).
//
// # Skip conditions (same as TestTracerLaunch)
//
//   - /dev/kvm absent or inaccessible
//   - cloud-hypervisor binary not found (CLOUD_HYPERVISOR_BIN or PATH)
//   - mke2fs not in PATH
//   - images/kernel/vmlinux-x86_64 absent and NEXUS3_KERNEL_PATH not set
//   - docker unavailable (needed by BuildSelfHostBaseImage)
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestHerdrHello \
//	    ./internal/test/selfhost/ -v -timeout 30m

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
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
)

// TestHerdrHello boots a sandbox with motiveID="herdr" (matching
// herdrPluginLaunch) and runs /bin/echo in-guest to prove the herdr→sandbox
// integration path works end-to-end for a simple, non-nested sandbox.
func TestHerdrHello(t *testing.T) {
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
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	t.Log("building self-host base image (may take several minutes) …")
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

	// ── Step 2: create service + driver infrastructure ────────────────────────
	// Socket dir in /tmp to stay within the 107-byte Linux sun_path limit.
	socketDir, err := os.MkdirTemp("/tmp", "herdr-hello-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir path too long for AF_UNIX: %s", socketDir)
	}

	storeRoot := t.TempDir()
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	// svcDrv: used by svc.Exec / svc.Remove (DialGuest via SocketDir+SandboxID).
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
			if err := svc.Remove(rmCtx, sandboxID.String()); err != nil {
				t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, err)
			} else {
				t.Logf("cleanup: sandbox %s removed", sandboxID)
			}
		}
		os.RemoveAll(socketDir)
	})

	// bootDrv: captured by factory for realProbeSH / waitForAgentSH.
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

	// ── Step 3: boot — mirrors herdrPluginLaunch (cmd_herdr_plugin.go:327) ────
	t.Log("creating and booting sandbox (motiveID=herdr) …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()

	sb, err := service.CreateAndBoot(
		bootCtx,
		svc,
		cache,
		factory,
		probe,
		"herdr", "hello", // motiveID + name matching herdrPluginLaunch's "herdr" motive
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

	t.Log("waiting for guest agent …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable")

	// ── Step 4: exec hello-world — mirrors herdrPluginLaunch svc.Exec call ────
	// argv[0] MUST be an absolute path; the guest agent resolves via
	// exec.LookPath in the agent binary's own PATH, NOT the injected PATH.
	const wantMsg = "hello from nexus3 sandbox"
	var stdout bytes.Buffer
	execCtx, execCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer execCancel()

	exitCode, execErr := svc.Exec(execCtx, sb.ID.String(), agent.ExecOptions{
		Argv: []string{"/bin/echo", wantMsg},
		Env: map[string]string{
			"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"HOME": "/root",
		},
		Stdout: &stdout,
		Stderr: os.Stderr,
	})
	if execErr != nil {
		t.Fatalf("svc.Exec /bin/echo: %v", execErr)
	}
	if exitCode != 0 {
		t.Fatalf("svc.Exec /bin/echo: exit %d", exitCode)
	}

	got := strings.TrimSpace(stdout.String())
	if !strings.Contains(got, wantMsg) {
		t.Fatalf("herdr hello-world output mismatch: got %q, want substring %q", got, wantMsg)
	}
	t.Logf("herdr hello-world PASS: output = %q", got)
}
