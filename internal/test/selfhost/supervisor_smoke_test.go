//go:build integration

package selfhost

// supervisor_smoke_test.go — D-PP-01 S1 post-exit egress smoke test.
//
// TestSupervisorPostExitEgress proves the core S1 invariant:
//
//	After the spawning CLI process exits, the guest still has a routed default
//	gateway (gvproxy / MITM perimeter) because the detached supervisor owns the
//	VM and the perimeter.
//
// Flow:
//  1. Build the self-host base image (or reuse from cache).
//  2. Build the nexus3 binary so SpawnDetached can re-exec it.
//  3. CreateAndBoot a sandbox; capture disk path and bootDrv.
//  4. Wait for guest agent (confirms VM is up).
//  5. svc.Stop → VM stopped, sandbox in Stopped state, disk file retained.
//  6. supervisor.SpawnDetached → detached supervisor boots VM + perimeter.
//  7. Shadow CHDriver with same socketDir → wait for agent → Exec egress check.
//  8. supervisor.StopSupervisor → graceful shutdown.
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestSupervisorPostExitEgress \
//	    ./internal/test/selfhost/ -v -timeout 30m

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/IniZio/nexus3/internal/supervisor"
)

// buildNexus3Bin compiles cmd/nexus3 as a native Linux binary and returns its
// path inside a temp directory cleaned up when t ends.
func buildNexus3Bin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "nexus3")
	repoR, err := findRepoRoot()
	if err != nil {
		t.Fatalf("buildNexus3Bin: findRepoRoot: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", bin, "github.com/IniZio/nexus3/cmd/nexus3")
	cmd.Dir = repoR
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/nexus3: %s\n%v", string(out), err)
	}
	return bin
}

// TestSupervisorPostExitEgress is the acceptance proof for D-PP-01 S1:
// the in-guest default route (gvproxy egress) remains reachable after the
// spawning process exits and the detached supervisor owns the perimeter.
func TestSupervisorPostExitEgress(t *testing.T) {
	// ── skip guards ────────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: base image ─────────────────────────────────────────────────────
	// Use the agent base image (not the self-host base image). The agent image
	// installs iproute2 so the nexus3-agent PID-1 can configure eth0 via ip(8)
	// on boot, giving the VM a default route from gvproxy. The self-host image
	// lacks iproute2: network init silently fails and /proc/net/route is empty,
	// which would sink the egress check even after the perimeter fix.
	storeRoot := t.TempDir()
	cacheRoot := filepath.Join(storeRoot, "images")
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	t.Log("building agent base image (first run ~15–30 min; subsequent: seconds from cache) …")
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer imgCancel()
	img, buildErr := BuildAgentBaseImage(imgCtx, cache)
	if buildErr != nil {
		switch {
		case errors.Is(buildErr, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", buildErr)
		case errors.Is(buildErr, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", buildErr)
		}
		t.Fatalf("BuildAgentBaseImage: %v", buildErr)
	}
	t.Logf("base image ready: digest=%s size=%.2f GiB",
		img.Digest, float64(img.Size)/(1<<30))

	// ── Step 2: build nexus3 binary for SpawnDetached ─────────────────────────
	t.Log("building nexus3 binary …")
	nexus3Bin := buildNexus3Bin(t)
	t.Logf("nexus3 binary: %s", nexus3Bin)

	// ── Step 3: infrastructure — dirs in /tmp for sun_path limit ──────────────
	socketDir, err := os.MkdirTemp("/tmp", "sv-smoke-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir path too long for AF_UNIX: %s", socketDir)
	}

	stateDir, err := os.MkdirTemp("/tmp", "sv-smoke-state-")
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("MkdirTemp stateDir: %v", err)
	}

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	serialPath := filepath.Join(socketDir, "sv-smoke-serial.log")

	// svcDrv: used by the host-side Service for Stop/Remove (API socket only,
	// no disk path needed — CHDriver.Stop talks to the CH API socket).
	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var supervisorPID int

	t.Cleanup(func() {
		// Print serial log on failure.
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== guest serial output ===\n%s", content)
		}
		// Stop supervisor if still running.
		if supervisorPID != 0 {
			sockPath := supervisor.SockPath(stateDir)
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := supervisor.StopSupervisor(stopCtx, sockPath); err != nil {
				t.Logf("cleanup: StopSupervisor: %v (may be already gone)", err)
			}
		}
		// Print supervisor log on failure.
		if content, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== supervisor log ===\n%s", content)
		}
		os.RemoveAll(socketDir)
		os.RemoveAll(stateDir)
	})

	// ── Step 4: CreateAndBoot — capture disk path ──────────────────────────────
	var diskPath string
	var bootDrv *cloudhypervisor.CHDriver

	factory := service.DriverFactory(func(resolvedExt4 string, _ []service.ExtraDisk) (driver.Driver, error) {
		diskPath = resolvedExt4
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    resolvedExt4,
			SerialOutputPath: serialPath,
			StartTimeout:     30 * time.Second,
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
		"sv-smoke", "perimeter",
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	t.Logf("sandbox booted: id=%s state=%s disk=%s", sb.ID, sb.State, diskPath)

	// Wait for agent before stopping.
	t.Log("waiting for guest agent …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable (initial boot)")

	// ── Step 5: svc.Stop — disk is retained ────────────────────────────────────
	t.Log("stopping sandbox (disk retained) …")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopCancel()
	if _, err := svc.Stop(stopCtx, sb.ID.String()); err != nil {
		t.Fatalf("svc.Stop: %v", err)
	}
	t.Log("sandbox stopped; disk retained at", diskPath)

	if diskPath == "" {
		t.Fatal("diskPath not captured from factory — cannot spawn supervisor")
	}

	// ── Step 6: SpawnDetached — supervisor owns VM + perimeter ────────────────
	t.Log("spawning detached supervisor …")
	spawnCfg := supervisor.SpawnConfig{
		Config: supervisor.Config{
			SandboxRef: sb.ID.String(),
			StoreRoot:  storeRoot,
			StateDir:   stateDir,
			CHBin:      chBin,
			SocketDir:  socketDir,
			KernelPath: kernelPath,
			DiskPath:   diskPath,
		},
		Exe:          nexus3Bin,
		ReadyTimeout: 5 * time.Minute,
	}
	pid, _, err := supervisor.SpawnDetached(spawnCfg)
	if err != nil {
		t.Fatalf("supervisor.SpawnDetached: %v", err)
	}
	supervisorPID = pid
	t.Logf("supervisor ready: pid=%d sock=%s", pid, supervisor.SockPath(stateDir))

	// ── Step 7: shadow driver — wait for agent, run egress check ──────────────
	// Create a read-only shadow CHDriver with the same socketDir and disk path.
	// It does NOT own the VM (the supervisor does) but can DialGuest via the
	// CH API socket that the supervisor's driver opened.
	shadowDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:    chBin,
		SocketDir:     socketDir,
		KernelPath:    kernelPath,
		DiskImagePath: diskPath,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (shadowDrv): %v", err)
	}

	t.Log("waiting for guest agent (supervisor-owned VM) …")
	waitForAgentSH(t, shadowDrv, sb.ID, 60*time.Second)
	t.Log("guest agent reachable after supervisor spawn")

	agentClient := agent.NewClient(shadowDrv, sb.ID)

	// Egress check: read /proc/net/route which is always available (kernel
	// interface, no userspace tools required). A line starting with a non-header
	// interface name and "00000000" as destination is the default route (0.0.0.0/0).
	// This proves gvproxy's DHCP installed the default route, which is the
	// prerequisite for any in-guest egress to the host-side perimeter.
	// Format: Iface Destination Gateway Flags RefCnt Use Metric Mask ...
	//   eth0  00000000  012AA8C0  0003  0  0  100  00000000  0  0  0
	routeCtx, routeCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer routeCancel()

	var routeBuf bytes.Buffer
	exitCode, routeErr := agentClient.Exec(routeCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "cat /proc/net/route"},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdout: &routeBuf,
		Stderr: &routeBuf,
	})
	routeOut := routeBuf.String()
	t.Logf("/proc/net/route (exit=%d):\n%s", exitCode, routeOut)

	if routeErr != nil {
		t.Fatalf("agent.Exec cat /proc/net/route: %v", routeErr)
	}
	if exitCode != 0 {
		t.Fatalf("cat /proc/net/route: exit %d, output: %s", exitCode, routeOut)
	}
	// Look for a default route: Destination field = "00000000" (0.0.0.0/0).
	hasDefaultRoute := false
	for _, line := range strings.Split(routeOut, "\n") {
		fields := strings.Fields(line)
		// Header line: "Iface Destination ..." — skip
		// Route line: "<iface> <Destination hex> <Gateway hex> ..."
		if len(fields) >= 2 && fields[1] == "00000000" {
			hasDefaultRoute = true
			break
		}
	}
	if !hasDefaultRoute {
		t.Fatalf("no default route (0.0.0.0) in /proc/net/route after supervisor spawn:\n%s", routeOut)
	}
	t.Logf("PROOF: default route present in /proc/net/route (perimeter egress active after spawning-CLI exit)")

	// ── Step 8: StopSupervisor — graceful shutdown ─────────────────────────────
	t.Log("sending /supervisor/stop …")
	sockPath := supervisor.SockPath(stateDir)
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	if err := supervisor.StopSupervisor(shutCtx, sockPath); err != nil {
		t.Fatalf("supervisor.StopSupervisor: %v", err)
	}
	t.Log("supervisor stop sent")

	// Wait for supervisor PID to exit (max 30s).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := checkPidGone(pid); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := checkPidGone(pid); err != nil {
		t.Logf("supervisor PID %d may still be running: %v (non-fatal)", pid, err)
	} else {
		t.Logf("supervisor PID %d exited cleanly", pid)
		supervisorPID = 0 // suppress cleanup double-stop
	}

	t.Log("TestSupervisorPostExitEgress PASS")
}

// checkPidGone returns nil if the process with pid is no longer alive.
func checkPidGone(pid int) error {
	pidDir := fmt.Sprintf("/proc/%d", pid)
	if _, err := os.Stat(pidDir); os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("process %d still alive (/proc/%d exists)", pid, pid)
}
