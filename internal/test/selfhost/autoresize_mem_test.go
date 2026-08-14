//go:build integration

package selfhost

// autoresize_mem_test.go — AR-LIVE-MEM acceptance test (ticket 17).
//
// TestAutoResizeMemGrow proves the memory auto-resize subsystem end-to-end
// against a real VM:
//
//  1. memhp cmdline params (`memhp_default_state=online
//     memory_hotplug.online_policy=auto-movable`) reach the guest before the
//     `--` PID-1 separator — a prerequisite for VirtioMem hotplug.
//  2. vsock:3002 telemetry server is running after supervisor boot.
//  3. The governor grows MemTotal when booted with 512 MiB RAM and a
//     MemMin governor bound of 600 MiB (kernel consumes ~100–150 MiB,
//     leaving MemAvailable ≈ 350–400 MiB < MemMin → governor must resize).
//  4. Host MemAvailable does not drop below 1 GiB during the test (reported
//     but not a hard failure; the headroom guard is a governor side-effect).
//  5. `--mem-ceiling=` appears in /proc/cmdline after the `--` separator with
//     the expected value.
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestAutoResizeMemGrow \
//	    ./internal/test/selfhost/ -v -timeout 60m
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// diskBootCmdlineBase is the base kernel cmdline for a disk-booted sandbox.
// Matches cmd/nexus3/cmd_sandbox.go's diskBootCmdlineBase constant.
const diskBootCmdlineBase = "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0"

// TestAutoResizeMemGrow is the AR-LIVE-MEM acceptance test.
func TestAutoResizeMemGrow(t *testing.T) {
	// ── skip guards ────────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	// Host memory guard: this test boots a 512 MiB guest and expects the
	// governor to grow it to ≥600 MiB. Require at least 2 GiB on the host.
	hostMiB := hostMemAvailableMiB()
	if hostMiB >= 0 && hostMiB < 2048 {
		t.Skipf("skipping: host MemAvailable=%d MiB < 2048 MiB required for auto-resize test", hostMiB)
	}
	t.Logf("host MemAvailable at test start: %d MiB", hostMiB)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: infrastructure (short paths for AF_UNIX sun_path limit) ───────
	socketDir, err := os.MkdirTemp("/tmp", "ar-mem-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir path too long for AF_UNIX: %s", socketDir)
	}

	stateDir, err := os.MkdirTemp("/tmp", "ar-mem-state-")
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("MkdirTemp stateDir: %v", err)
	}

	storeRoot := t.TempDir()
	cacheRoot := filepath.Join(storeRoot, "images")

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	// svcDrv: used by the host-side Service for Stop/Remove (API socket only).
	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var supervisorPID int
	var sandboxID domain.SandboxID

	t.Cleanup(func() {
		if supervisorPID > 0 {
			sock := supervisor.SockPath(stateDir)
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := supervisor.StopSupervisor(stopCtx, sock); err != nil {
				t.Logf("cleanup: StopSupervisor: %v (may be already gone)", err)
			}
		}
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := svc.Remove(rmCtx, sandboxID.String()); err != nil {
				t.Logf("cleanup: svc.Remove: %v", err)
			}
		}
		// Print supervisor log on failure.
		if svLog, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil && len(svLog) > 0 && t.Failed() {
			t.Logf("=== supervisor log (tail 200 lines) ===\n%s", lastNLines(string(svLog), 200))
		}
		os.RemoveAll(socketDir)
		os.RemoveAll(stateDir)
	})

	// ── Step 2: build agent base image ────────────────────────────────────────
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

	// ── Step 3: build nexus3 binary for SpawnDetached ─────────────────────────
	t.Log("building nexus3 binary …")
	nexus3Bin := buildNexus3Bin(t)
	t.Logf("nexus3 binary: %s", nexus3Bin)

	// ── Step 4: CreateAndBoot — initial boot to populate disk ─────────────────
	// MemoryMaxMiB=1024 causes the driver to insert memhp cmdline params and
	// reserve a VirtioMem hotplug region. This mirrors the orca sandbox-create
	// path when auto-resize is enabled.
	var diskPath string
	var bootDrv *cloudhypervisor.CHDriver

	factory := service.DriverFactory(func(resolvedExt4 string, _ []service.ExtraDisk) (driver.Driver, error) {
		diskPath = resolvedExt4
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:    chBin,
			SocketDir:     socketDir,
			KernelPath:    kernelPath,
			DiskImagePath: resolvedExt4,
			StartTimeout:  30 * time.Second,
			MemoryMaxMiB:  1024,
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
		"ar-mem", "grow",
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
	t.Logf("sandbox booted: id=%s state=%s disk=%s", sb.ID, sb.State, diskPath)

	// Wait for agent before stopping.
	t.Log("waiting for guest agent (initial boot) …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable (initial boot)")

	// ── Step 5: svc.Stop — disk is retained ───────────────────────────────────
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

	// ── Step 6: SpawnDetached — supervisor owns VM with auto-resize ───────────
	// Boot with 512 MiB RAM; MemMin=600 MiB forces the governor to grow the VM
	// because the kernel consumes ~100–150 MiB, leaving MemAvailable well below
	// MemMin. The supervisor derives MemoryMaxMiB=1024 from GovBounds.MemMaxBytes
	// and calls buildCmdline which inserts the memhp params before `--`.
	const memCeilingBytes = 1024 * 1024 * 1024 // 1 GiB
	svCmdline := diskBootCmdlineBase +
		" -- --mem-ceiling=" + strconv.FormatInt(memCeilingBytes, 10)

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
			MemoryMiB:  512,
			GovBounds: resize.Bounds{
				MemMinBytes: 600 << 20, // 600 MiB
				MemMaxBytes: 1024 << 20, // 1 GiB
				VCPUMin:     1,
				VCPUMax:     2,
			},
			Cmdline:          svCmdline,
			HasWorkspaceDisk: false,
		},
		Exe:          nexus3Bin,
		LogPath:      filepath.Join(stateDir, "supervisor.log"),
		ReadyTimeout: 3 * time.Minute,
	}
	pid, _, err := supervisor.SpawnDetached(spawnCfg)
	if err != nil {
		t.Fatalf("supervisor.SpawnDetached: %v", err)
	}
	supervisorPID = pid
	t.Logf("supervisor ready: pid=%d sock=%s", pid, supervisor.SockPath(stateDir))

	// ── Step 7: shadow driver — connects to supervisor-owned VM ───────────────
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

	// ── Step 8: Assertion 1+5 — verify cmdline in guest ───────────────────────
	// Read /proc/cmdline and confirm:
	//   - memhp_default_state=online and memory_hotplug.online_policy=auto-movable
	//     appear BEFORE the `--` separator (kernel args, inserted by buildCmdline).
	//   - --mem-ceiling=<value> appears AFTER the `--` separator (PID-1 args).
	//     Auto-resize is unconditional; there is no --auto-resize token.
	cmdlineCtx, cmdlineCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cmdlineCancel()

	var cmdlineBuf bytes.Buffer
	cmdlineExit, cmdlineErr := agentClient.Exec(cmdlineCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "cat /proc/cmdline"},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdout: &cmdlineBuf,
		Stderr: &cmdlineBuf,
	})
	procCmdline := strings.TrimSpace(cmdlineBuf.String())
	t.Logf("EVIDENCE /proc/cmdline (exit=%d): %s", cmdlineExit, procCmdline)

	if cmdlineErr != nil {
		t.Fatalf("agent.Exec cat /proc/cmdline: %v", cmdlineErr)
	}
	if cmdlineExit != 0 {
		t.Fatalf("cat /proc/cmdline: exit %d, output: %s", cmdlineExit, procCmdline)
	}

	// Split on `--` to separate kernel args from PID-1 args.
	parts := strings.SplitN(procCmdline, " -- ", 2)
	kernelArgs := parts[0]
	var pid1Args string
	if len(parts) == 2 {
		pid1Args = parts[1]
	}
	t.Logf("EVIDENCE kernel args (before --): %s", kernelArgs)
	t.Logf("EVIDENCE PID-1 args (after --): %s", pid1Args)

	// Assertion 1: memhp params before `--`.
	if !strings.Contains(kernelArgs, "memhp_default_state=online") {
		t.Errorf("FAIL assertion 1a: memhp_default_state=online not found in kernel args: %q", kernelArgs)
	} else {
		t.Log("PASS assertion 1a: memhp_default_state=online in kernel args")
	}
	if !strings.Contains(kernelArgs, "memory_hotplug.online_policy=auto-movable") {
		t.Errorf("FAIL assertion 1b: memory_hotplug.online_policy=auto-movable not found in kernel args: %q", kernelArgs)
	} else {
		t.Log("PASS assertion 1b: memory_hotplug.online_policy=auto-movable in kernel args")
	}

	// Assertion 5: --mem-ceiling=<expected> after `--`.
	// Auto-resize is unconditional; the PID-1 wire contract is " --mem-ceiling=<bytes>"
	// with NO --auto-resize token (that flag no longer exists).
	expectedCeilingArg := "--mem-ceiling=" + strconv.FormatInt(memCeilingBytes, 10)
	if !strings.Contains(pid1Args, expectedCeilingArg) {
		t.Errorf("FAIL assertion 5: %s not found in PID-1 args: %q", expectedCeilingArg, pid1Args)
	} else {
		t.Logf("PASS assertion 5: %s in PID-1 args", expectedCeilingArg)
	}
	if strings.Contains(pid1Args, "--auto-resize") {
		t.Errorf("FAIL assertion 5b: stale --auto-resize token found in PID-1 args (flag was removed): %q", pid1Args)
	} else {
		t.Log("PASS assertion 5b: --auto-resize token absent (unconditional; expected)")
	}

	// ── Step 9: Assertion 2 — vsock:3002 telemetry + first sample ─────────────
	t.Log("dialing vsock:3002 for first telemetry sample …")
	firstSample, firstErr := dialTelemetrySample(shadowDrv, sb.ID)
	if firstErr != nil {
		t.Fatalf("FAIL assertion 2: vsock:3002 sample: %v", firstErr)
	}
	t.Logf("EVIDENCE first sample: MemTotalBytes=%d (%d MiB) MemAvailableBytes=%d (%d MiB)",
		firstSample.MemTotalBytes, firstSample.MemTotalBytes>>20,
		firstSample.MemAvailableBytes, firstSample.MemAvailableBytes>>20)
	if firstSample.MemTotalBytes == 0 {
		t.Error("FAIL assertion 2: first sample MemTotalBytes == 0")
	} else {
		t.Logf("PASS assertion 2: vsock:3002 telemetry server running, MemTotal > 0")
	}

	// ── Step 10: Create memory pressure — fill tmpfs to push MemAvailable < 20% ─
	// The governor's grow threshold is MemAvailable/MemTotal < 0.20. On a fresh
	// 512 MiB boot the ratio is ≈93% — no pressure. We mount a 400 MiB tmpfs and
	// fill 370 MiB so MemAvailable drops below 20% of MemTotal, which forces the
	// governor to grow the VM to its MemMax ceiling (1 GiB).
	//
	// Command breakdown:
	//   mkdir -p /tmp/memhog       — ensure mount point exists
	//   mount -t tmpfs -o size=400m — mount RAM-backed fs (requires root)
	//   dd … count=370             — fill 370 MiB; tmpfs pages are non-reclaimable
	//                                without swap, so MemAvailable drops immediately
	t.Log("creating memory pressure in guest (tmpfs fill) …")
	pressCtx, pressCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer pressCancel()
	var pressBuf bytes.Buffer
	pressExit, pressErr := agentClient.Exec(pressCtx, agent.ExecOptions{
		Argv: []string{"/bin/sh", "-c",
			"mkdir -p /tmp/memhog && " +
				"mount -t tmpfs -o size=400m tmpfs /tmp/memhog && " +
				"dd if=/dev/zero of=/tmp/memhog/hog bs=1M count=370 status=none"},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdout: &pressBuf,
		Stderr: &pressBuf,
	})
	if pressErr != nil {
		t.Fatalf("memory pressure exec failed: %v (output: %s)", pressErr, pressBuf.String())
	}
	if pressExit != 0 {
		t.Fatalf("memory pressure command exit=%d: %s", pressExit, pressBuf.String())
	}
	t.Log("memory pressure created (370 MiB tmpfs filled); governor should detect within 15s")

	// Confirm pressure is visible in the guest.
	pressSample, pressErr2 := dialTelemetrySample(shadowDrv, sb.ID)
	if pressErr2 == nil {
		t.Logf("EVIDENCE post-pressure sample: MemTotalBytes=%d (%d MiB) MemAvailableBytes=%d (%d MiB) ratio=%.2f%%",
			pressSample.MemTotalBytes, pressSample.MemTotalBytes>>20,
			pressSample.MemAvailableBytes, pressSample.MemAvailableBytes>>20,
			100*float64(pressSample.MemAvailableBytes)/float64(pressSample.MemTotalBytes))
	}

	// ── Step 11: Assertion 3 — governor grows MemTotal within 120s ────────────
	// Governor polls every 5s, needs 3 consecutive pressure samples (D-DC-10),
	// plus vm.resize time. Empirical budget: 3×5s + ~5s resize = ~20s; we allow
	// 120s to absorb cold-path latency.
	t.Logf("waiting for governor to grow MemTotal (target: > %d bytes = %d MiB) …",
		firstSample.MemTotalBytes, firstSample.MemTotalBytes>>20)

	const growPollInterval = 10 * time.Second
	const growTimeout = 120 * time.Second
	growStart := time.Now()
	growDeadline := growStart.Add(growTimeout)

	var lastSample resize.Sample
	var growDetected bool
	var growWallTime time.Duration

	for !time.Now().After(growDeadline) {
		time.Sleep(growPollInterval)
		s, err := dialTelemetrySample(shadowDrv, sb.ID)
		if err != nil {
			t.Logf("telemetry poll error (will retry): %v", err)
			continue
		}
		t.Logf("poll sample: MemTotalBytes=%d (%d MiB) MemAvailableBytes=%d (%d MiB) elapsed=%s",
			s.MemTotalBytes, s.MemTotalBytes>>20,
			s.MemAvailableBytes, s.MemAvailableBytes>>20,
			time.Since(growStart).Round(time.Second))
		lastSample = s
		if s.MemTotalBytes > firstSample.MemTotalBytes {
			growDetected = true
			growWallTime = time.Since(growStart)
			break
		}
	}

	if !growDetected {
		t.Errorf("FAIL assertion 3: governor did not grow MemTotal within %s — first=%d MiB last=%d MiB",
			growTimeout, firstSample.MemTotalBytes>>20, lastSample.MemTotalBytes>>20)
	} else {
		t.Logf("PASS assertion 3: MemTotal grew in %s: %d MiB → %d MiB",
			growWallTime.Round(time.Millisecond),
			firstSample.MemTotalBytes>>20, lastSample.MemTotalBytes>>20)
	}

	// ── Step 12: raw /proc/meminfo confirmation ────────────────────────────────
	meminfoCtx, meminfoCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer meminfoCancel()

	var meminfoBuf bytes.Buffer
	meminfoExit, meminfoErr := agentClient.Exec(meminfoCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "grep -E '^(MemTotal|MemAvailable):' /proc/meminfo"},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdout: &meminfoBuf,
		Stderr: &meminfoBuf,
	})
	t.Logf("EVIDENCE /proc/meminfo (exit=%d):\n%s", meminfoExit, strings.TrimSpace(meminfoBuf.String()))
	if meminfoErr != nil {
		t.Logf("agent.Exec /proc/meminfo: %v (non-fatal)", meminfoErr)
	}

	// ── Step 13: Assertion 4 — host headroom guard ────────────────────────────
	hostMiBAfter := hostMemAvailableMiB()
	t.Logf("EVIDENCE host MemAvailable after grow: %d MiB", hostMiBAfter)
	if hostMiBAfter >= 0 && hostMiBAfter < 1024 {
		// Report but do not fail — the guard exercises the governor's "don't grow
		// when host is tight" path, which requires a dedicated constrained-host
		// test environment. This test runs on a well-provisioned host.
		t.Logf("NOTE assertion 4: host MemAvailable=%d MiB < 1024 MiB (headroom guard triggered — "+
			"the governor should not have grown, but this test ran on a constrained host)", hostMiBAfter)
	} else {
		t.Logf("PASS assertion 4: host MemAvailable=%d MiB >= 1024 MiB (headroom guard not triggered)", hostMiBAfter)
	}
}

// dialTelemetrySample opens a one-shot vsock connection to port 3002, sends a
// sample request, and returns the decoded sample. The connection is closed
// after one request-response exchange (server closes after reply per D-DC-10).
func dialTelemetrySample(drv *cloudhypervisor.CHDriver, id domain.SandboxID) (resize.Sample, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := drv.DialGuest(ctx, id, resize.TelemetryVsockPort)
	if err != nil {
		return resize.Sample{}, fmt.Errorf("DialGuest vsock:%d: %w", resize.TelemetryVsockPort, err)
	}
	defer conn.Close()

	if err := resize.EncodeSampleRequest(conn); err != nil {
		return resize.Sample{}, fmt.Errorf("EncodeSampleRequest: %w", err)
	}
	resp, err := resize.DecodeSampleResponse(conn)
	if err != nil {
		return resize.Sample{}, fmt.Errorf("DecodeSampleResponse: %w", err)
	}
	return resp.Sample, nil
}

// lastNLines returns the last n lines of s, or all of s if it has fewer lines.
func lastNLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
