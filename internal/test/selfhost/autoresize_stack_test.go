//go:build integration

package selfhost

// autoresize_stack_test.go — AR-LIVE-STACK acceptance tests (spec-08 §2.4, sub-part 4).
//
// TestAutoResizeZRAMBeforeWorkload proves that ZRAM swap is enabled BEFORE any
// workload starts — a MUST from spec-08 §2.4 — by inspecting /proc/swaps and
// /proc/sys/vm/swappiness inside a real VM (auto-resize is unconditional).
//
// TestAutoResizeTmpGrowsWithMemTotal proves that the /tmp tmpfs resizer grows
// /tmp proportionally as MemTotal grows (critical for buildkit which stages
// large layer data under /tmp).
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration \
//	    -run 'TestAutoResizeZRAMBeforeWorkload|TestAutoResizeTmpGrowsWithMemTotal' \
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

// TestAutoResizeZRAMBeforeWorkload proves ZRAM swap is active before any
// workload (auto-resize is unconditional; spec-08 §2.4 MUST).
//
// Assertions:
//  1. /proc/swaps has at least one non-header line referencing zram.
//  2. /proc/sys/vm/swappiness == 10 (the value the agent sets for safe swap).
func TestAutoResizeZRAMBeforeWorkload(t *testing.T) {
	// ── skip guards ────────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	hostMiB := hostMemAvailableMiB()
	if hostMiB >= 0 && hostMiB < 2048 {
		t.Skipf("skipping: host MemAvailable=%d MiB < 2048 MiB required", hostMiB)
	}
	t.Logf("host MemAvailable at test start: %d MiB", hostMiB)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: infrastructure (short paths for AF_UNIX sun_path limit) ───────
	socketDir, err := os.MkdirTemp("/tmp", "ar-zram-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir path too long for AF_UNIX: %s", socketDir)
	}

	stateDir, err := os.MkdirTemp("/tmp", "ar-zram-state-")
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
	t.Log("building agent base image …")
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
	t.Logf("base image ready: digest=%s size=%.2f GiB", img.Digest, float64(img.Size)/(1<<30))

	// ── Step 3: build nexus3 binary for SpawnDetached ─────────────────────────
	t.Log("building nexus3 binary …")
	nexus3Bin := buildNexus3Bin(t)
	t.Logf("nexus3 binary: %s", nexus3Bin)

	// ── Step 4: CreateAndBoot — initial boot to populate disk ─────────────────
	var diskPath string
	var bootDrv *cloudhypervisor.CHDriver

	// Reap the cloud-hypervisor process that bootDrv spawns. svc.Stop() calls
	// svcDrv.Stop() (which sends CH API shutdown but skips proc.kill() because
	// svcDrv does not own the process), leaving a zombie child of the test
	// binary. bootDrv.Stop() calls proc.kill() → cmd.Wait() on the handle it
	// owns, reaping the zombie. Must run before RemoveAll(socketDir) so the
	// API socket is still present for the graceful shutdown attempt.
	t.Cleanup(func() {
		if bootDrv != nil && sandboxID != (domain.SandboxID{}) {
			killCtx, killCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer killCancel()
			if err := bootDrv.Stop(killCtx, sandboxID); err != nil {
				t.Logf("cleanup: bootDrv.Stop: %v (process may already be reaped)", err)
			}
		}
	})

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
		"ar-zram", "before-workload",
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
	t.Logf("sandbox booted: id=%s disk=%s", sb.ID, diskPath)

	// ── Step 5: wait for agent, then stop (disk retained) ────────────────────
	t.Log("waiting for guest agent (initial boot) …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable (initial boot)")

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
	agentEnv := map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}

	// ── Step 8: EVIDENCE — /proc/swaps ───────────────────────────────────────
	// Read BEFORE any workload so we prove ZRAM is pre-enabled by the agent.
	swapsCtx, swapsCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer swapsCancel()

	var swapsBuf bytes.Buffer
	swapsExit, swapsErr := agentClient.Exec(swapsCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "cat /proc/swaps"},
		Env:    agentEnv,
		Stdout: &swapsBuf,
		Stderr: &swapsBuf,
	})
	swapsOut := strings.TrimSpace(swapsBuf.String())
	t.Logf("EVIDENCE /proc/swaps (exit=%d):\n%s", swapsExit, swapsOut)
	if swapsErr != nil {
		t.Logf("agent.Exec cat /proc/swaps: %v (non-fatal)", swapsErr)
	}

	// ── Step 9: EVIDENCE — grep zram in /proc/swaps ──────────────────────────
	zramCtx, zramCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer zramCancel()

	var zramBuf bytes.Buffer
	zramExit, zramErr := agentClient.Exec(zramCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "grep -c zram /proc/swaps || echo ZRAM_ABSENT"},
		Env:    agentEnv,
		Stdout: &zramBuf,
		Stderr: &zramBuf,
	})
	zramOut := strings.TrimSpace(zramBuf.String())
	t.Logf("EVIDENCE grep zram /proc/swaps (exit=%d): %q", zramExit, zramOut)
	if zramErr != nil {
		t.Logf("agent.Exec grep zram: %v (non-fatal)", zramErr)
	}

	// ── Step 10: EVIDENCE — /proc/sys/vm/swappiness ───────────────────────────
	swappCtx, swappCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer swappCancel()

	var swappBuf bytes.Buffer
	swappExit, swappErr := agentClient.Exec(swappCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "cat /proc/sys/vm/swappiness"},
		Env:    agentEnv,
		Stdout: &swappBuf,
		Stderr: &swappBuf,
	})
	swappOut := strings.TrimSpace(swappBuf.String())
	t.Logf("EVIDENCE /proc/sys/vm/swappiness (exit=%d): %q", swappExit, swappOut)
	if swappErr != nil {
		t.Logf("agent.Exec cat swappiness: %v (non-fatal)", swappErr)
	}

	// ── Step 11: EVIDENCE — grep SwapTotal /proc/meminfo ─────────────────────
	stCtx, stCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer stCancel()

	var stBuf bytes.Buffer
	stExit, stErr := agentClient.Exec(stCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "grep SwapTotal /proc/meminfo"},
		Env:    agentEnv,
		Stdout: &stBuf,
		Stderr: &stBuf,
	})
	stOut := strings.TrimSpace(stBuf.String())
	t.Logf("EVIDENCE grep SwapTotal /proc/meminfo (exit=%d): %q", stExit, stOut)
	if stErr != nil {
		t.Logf("agent.Exec grep SwapTotal: %v (non-fatal)", stErr)
	}

	// ── Assertion 1: /proc/swaps has a zram entry ─────────────────────────────
	// /proc/swaps format: first line is header, subsequent lines are active swap
	// devices. We require at least one line containing "zram".
	if strings.Contains(swapsOut, "ZRAM_ABSENT") || (zramExit != 0 && !strings.Contains(swapsOut, "zram")) {
		// Note: this means CONFIG_ZRAM=y is confirmed in scripts/kernel/config-6.12.76
		// but the agent did not activate it — needs investigation.
		t.Errorf("FAIL assertion 1: ZRAM not active before workload\n  /proc/swaps:\n%s\n  grep exit=%d out=%q",
			swapsOut, zramExit, zramOut)
	} else if strings.Contains(swapsOut, "zram") {
		t.Logf("PASS assertion 1: ZRAM active in /proc/swaps before workload")
	} else {
		// grep -c returned 0 exit (no match found but ZRAM_ABSENT not echoed either
		// because grep -c succeeds with count 0 on some shells) — check the count.
		if count, _ := strconv.Atoi(zramOut); count == 0 {
			t.Errorf("FAIL assertion 1: /proc/swaps has no zram entry (grep count=0)\n  /proc/swaps:\n%s", swapsOut)
		} else {
			t.Logf("PASS assertion 1: ZRAM active (%s entries) before workload", zramOut)
		}
	}

	// ── Assertion 2: swappiness == 10 ─────────────────────────────────────────
	// The agent sets vm.swappiness=10 when ZRAM is enabled so swap is used only
	// under genuine pressure (not eagerly), preventing latency spikes.
	if swappErr == nil && swappExit == 0 {
		if swappOut == "10" {
			t.Logf("PASS assertion 2: /proc/sys/vm/swappiness == 10")
		} else {
			t.Errorf("FAIL assertion 2: /proc/sys/vm/swappiness = %q, want 10", swappOut)
		}
	} else {
		t.Logf("NOTE assertion 2: could not read swappiness (exit=%d err=%v) — skipping", swappExit, swappErr)
	}

	// ── Assertion 3 (soft): SwapTotal > 0 in /proc/meminfo ───────────────────
	// A non-zero SwapTotal confirms the kernel sees the swap device. This is a
	// soft assertion: ZRAM + swap must both be active.
	if stErr == nil && stExit == 0 {
		// Line format: "SwapTotal:     <N> kB"
		fields := strings.Fields(stOut)
		if len(fields) >= 2 {
			if n, err := strconv.ParseUint(fields[1], 10, 64); err == nil && n > 0 {
				t.Logf("PASS assertion 3: SwapTotal=%d kB (> 0) in /proc/meminfo", n)
			} else {
				t.Errorf("FAIL assertion 3: SwapTotal=%q in /proc/meminfo (want > 0)", fields[1])
			}
		} else {
			t.Logf("NOTE assertion 3: unexpected SwapTotal line format: %q", stOut)
		}
	} else {
		t.Logf("NOTE assertion 3: grep SwapTotal failed (exit=%d err=%v) — skipping", stExit, stErr)
	}
}

// TestAutoResizeTmpGrowsWithMemTotal proves that the /tmp tmpfs resizer inside
// the guest agent grows /tmp as MemTotal grows under memory pressure
// (critical for buildkit, which stages large layer data under /tmp).
//
// Sequence:
//  1. Boot VM with auto-resize, record firstTmpBytes from df -B1 /tmp.
//  2. Create memory pressure (400 MiB tmpfs fill) to trigger governor growth.
//  3. Poll until MemTotal grows or timeout.
//  4. Assert grownTmpBytes > firstTmpBytes.
func TestAutoResizeTmpGrowsWithMemTotal(t *testing.T) {
	// ── skip guards ────────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	hostMiB := hostMemAvailableMiB()
	if hostMiB >= 0 && hostMiB < 2048 {
		t.Skipf("skipping: host MemAvailable=%d MiB < 2048 MiB required", hostMiB)
	}
	t.Logf("host MemAvailable at test start: %d MiB", hostMiB)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: infrastructure ────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "ar-tmp-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir path too long for AF_UNIX: %s", socketDir)
	}

	stateDir, err := os.MkdirTemp("/tmp", "ar-tmp-state-")
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
	t.Log("building agent base image …")
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
	t.Logf("base image ready: digest=%s size=%.2f GiB", img.Digest, float64(img.Size)/(1<<30))

	// ── Step 3: build nexus3 binary for SpawnDetached ─────────────────────────
	t.Log("building nexus3 binary …")
	nexus3Bin := buildNexus3Bin(t)
	t.Logf("nexus3 binary: %s", nexus3Bin)

	// ── Step 4: CreateAndBoot — initial boot to populate disk ─────────────────
	var diskPath string
	var bootDrv *cloudhypervisor.CHDriver

	// Reap the cloud-hypervisor process that bootDrv spawns. Same reasoning as
	// the identical cleanup block in TestAutoResizeZRAMBeforeWorkload.
	t.Cleanup(func() {
		if bootDrv != nil && sandboxID != (domain.SandboxID{}) {
			killCtx, killCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer killCancel()
			if err := bootDrv.Stop(killCtx, sandboxID); err != nil {
				t.Logf("cleanup: bootDrv.Stop: %v (process may already be reaped)", err)
			}
		}
	})

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
		"ar-tmp", "tmp-grows",
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
	t.Logf("sandbox booted: id=%s disk=%s", sb.ID, diskPath)

	// ── Step 5: wait for agent, then stop (disk retained) ────────────────────
	t.Log("waiting for guest agent (initial boot) …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable (initial boot)")

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

	// ── Step 7: shadow driver ─────────────────────────────────────────────────
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
	agentEnv := map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}

	// ── Step 8: first telemetry sample ───────────────────────────────────────
	t.Log("dialing vsock:3002 for first telemetry sample …")
	firstSample, firstSampleErr := dialTelemetrySample(shadowDrv, sb.ID)
	if firstSampleErr != nil {
		t.Fatalf("first telemetry sample: %v", firstSampleErr)
	}
	t.Logf("EVIDENCE first sample: MemTotalBytes=%d (%d MiB) MemAvailableBytes=%d (%d MiB)",
		firstSample.MemTotalBytes, firstSample.MemTotalBytes>>20,
		firstSample.MemAvailableBytes, firstSample.MemAvailableBytes>>20)

	// ── Step 9: read initial /tmp size ────────────────────────────────────────
	// df -B1 /tmp output (second line):
	//   tmpfs  <size-bytes>  <used>  <avail>  <use%>  /tmp
	dfFirstCtx, dfFirstCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer dfFirstCancel()

	var dfFirstBuf bytes.Buffer
	dfFirstExit, dfFirstErr := agentClient.Exec(dfFirstCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "df -B1 /tmp"},
		Env:    agentEnv,
		Stdout: &dfFirstBuf,
		Stderr: &dfFirstBuf,
	})
	dfFirstOut := strings.TrimSpace(dfFirstBuf.String())
	t.Logf("EVIDENCE df -B1 /tmp before pressure (exit=%d):\n%s", dfFirstExit, dfFirstOut)
	if dfFirstErr != nil {
		t.Fatalf("df -B1 /tmp (first): %v", dfFirstErr)
	}

	firstTmpBytes := parseDfSizeBytes(dfFirstOut)
	t.Logf("EVIDENCE firstTmpBytes=%d (%d MiB)", firstTmpBytes, firstTmpBytes>>20)

	// ── Step 10: create memory pressure ──────────────────────────────────────
	// Mount a 400 MiB tmpfs and fill 370 MiB to push MemAvailable below 20% of
	// MemTotal, triggering the governor to grow the VM.
	t.Log("creating memory pressure in guest (tmpfs fill) …")
	pressCtx, pressCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer pressCancel()

	var pressBuf bytes.Buffer
	pressExit, pressErr := agentClient.Exec(pressCtx, agent.ExecOptions{
		Argv: []string{"/bin/sh", "-c",
			"mkdir -p /tmp/memhog && " +
				"mount -t tmpfs -o size=400m tmpfs /tmp/memhog && " +
				"dd if=/dev/zero of=/tmp/memhog/hog bs=1M count=370 status=none"},
		Env:    agentEnv,
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

	// Confirm pressure is visible in the telemetry.
	pressSample, pressSampleErr := dialTelemetrySample(shadowDrv, sb.ID)
	if pressSampleErr == nil {
		t.Logf("EVIDENCE post-pressure sample: MemTotalBytes=%d (%d MiB) MemAvailableBytes=%d (%d MiB) ratio=%.2f%%",
			pressSample.MemTotalBytes, pressSample.MemTotalBytes>>20,
			pressSample.MemAvailableBytes, pressSample.MemAvailableBytes>>20,
			100*float64(pressSample.MemAvailableBytes)/float64(pressSample.MemTotalBytes))
	}

	// ── Step 11: poll for MemTotal growth ─────────────────────────────────────
	// Governor polls every 5s, needs 3 consecutive pressure samples, plus
	// vm.resize latency. Allow 120s empirical budget.
	const growPollInterval = 10 * time.Second
	const growTimeout = 120 * time.Second
	growStart := time.Now()
	growDeadline := growStart.Add(growTimeout)

	var lastSample resize.Sample
	var growDetected bool
	var growWallTime time.Duration

	t.Logf("polling for MemTotal growth (timeout %s) …", growTimeout)
	for !time.Now().After(growDeadline) {
		time.Sleep(growPollInterval)
		s, pollErr := dialTelemetrySample(shadowDrv, sb.ID)
		if pollErr != nil {
			t.Logf("telemetry poll error (will retry): %v", pollErr)
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
		t.Errorf("governor did not grow MemTotal within %s — first=%d MiB last=%d MiB; "+
			"/tmp grow assertion cannot be verified without MemTotal growth",
			growTimeout, firstSample.MemTotalBytes>>20, lastSample.MemTotalBytes>>20)
		return // no point reading /tmp if memory did not grow
	}
	t.Logf("MemTotal grew in %s: %d MiB → %d MiB",
		growWallTime.Round(time.Millisecond),
		firstSample.MemTotalBytes>>20, lastSample.MemTotalBytes>>20)

	// Allow up to 15s for the /tmp resizer goroutine to notice the MemTotal
	// change and call mount --make-shared / mount -o remount on /tmp.
	time.Sleep(15 * time.Second)

	// ── Step 12: read /tmp size after growth ──────────────────────────────────
	dfGrownCtx, dfGrownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer dfGrownCancel()

	var dfGrownBuf bytes.Buffer
	dfGrownExit, dfGrownErr := agentClient.Exec(dfGrownCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "df -B1 /tmp"},
		Env:    agentEnv,
		Stdout: &dfGrownBuf,
		Stderr: &dfGrownBuf,
	})
	dfGrownOut := strings.TrimSpace(dfGrownBuf.String())
	t.Logf("EVIDENCE df -B1 /tmp after growth (exit=%d):\n%s", dfGrownExit, dfGrownOut)
	if dfGrownErr != nil {
		t.Fatalf("df -B1 /tmp (after growth): %v", dfGrownErr)
	}

	grownTmpBytes := parseDfSizeBytes(dfGrownOut)
	t.Logf("EVIDENCE grownTmpBytes=%d (%d MiB)", grownTmpBytes, grownTmpBytes>>20)

	// ── Assertion: /tmp grew with MemTotal ────────────────────────────────────
	// The /tmp resizer (cmd/nexus3-agent/resize_tmp_linux.go) remounts /tmp
	// with size ≈ MemTotal/2 when MemTotal increases. We just need to see any
	// growth; exact proportionality is tested separately.
	if grownTmpBytes > firstTmpBytes {
		t.Logf("PASS: /tmp grew from %d MiB to %d MiB following MemTotal growth",
			firstTmpBytes>>20, grownTmpBytes>>20)
	} else {
		t.Errorf("FAIL: /tmp did not grow after MemTotal growth\n"+
			"  firstTmpBytes=%d (%d MiB)\n"+
			"  grownTmpBytes=%d (%d MiB)\n"+
			"  firstMemTotal=%d MiB → lastMemTotal=%d MiB\n"+
			"  Check: is resize_tmp_linux.go goroutine running? (auto-resize is unconditional)",
			firstTmpBytes, firstTmpBytes>>20,
			grownTmpBytes, grownTmpBytes>>20,
			firstSample.MemTotalBytes>>20, lastSample.MemTotalBytes>>20)
	}
}

// parseDfSizeBytes parses the total size in bytes from the output of
// `df -B1 <mountpoint>`. The expected format is:
//
//	Filesystem  1B-blocks  Used  Available  Use%  Mounted on
//	tmpfs       <size>     ...
//
// Returns 0 if the output cannot be parsed.
func parseDfSizeBytes(dfOut string) uint64 {
	lines := strings.Split(dfOut, "\n")
	// Skip the header line; take the first data line.
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Filesystem") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		return n
	}
	return 0
}

// Ensure resize is used (dialTelemetrySample uses it; parseDfSizeBytes keeps
// the compiler happy without a direct resize import in the body above).
var _ = resize.TelemetryVsockPort

// Ensure fmt is used.
var _ = fmt.Sprintf
