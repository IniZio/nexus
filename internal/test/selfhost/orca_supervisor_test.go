//go:build integration

package selfhost

// orca_supervisor_test.go — D-PP-01 S2 acceptance test.
//
// TestOrcaSupervisorWiring proves the S2 wiring invariants:
//
//  1. SpawnDetached (the core of orcaCreate's supervisor path) successfully
//     forks a detached supervisor that boots the VM + perimeter.
//  2. SetSupervisor persists SupervisorPID + SupervisorSock onto the sandbox
//     record so a subsequent orcaDestroy can find the supervisor.
//  3. StopSupervisor (the core of orcaDestroy's teardown path) gracefully
//     shuts down the supervisor and its VM.
//  4. After StopSupervisor the sandbox state is Stopped (supervisor called
//     svc.Stop before exiting) so svc.Remove can clean up.
//
// This test does NOT require the Orca AppImage or ORCA_VM_INSTANCE_ID — it
// drives the spawn+persist+stop cycle directly, at the highest fidelity
// possible without live Orca infrastructure.  A full live orca create/destroy
// round-trip (including Orca IDE) is deferred to S4.
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestOrcaSupervisorWiring \
//	    ./internal/test/selfhost/ -v -timeout 30m

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

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

// TestOrcaSupervisorWiring is the S2 acceptance proof. It mirrors orcaCreate's
// spawn+persist+stop flow without the CLI layer (no cobra, no env vars).
func TestOrcaSupervisorWiring(t *testing.T) {
	// ── skip guards ────────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}

	// ── Step 1: build or reuse base image ─────────────────────────────────────
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("store.DefaultRoot: %v", err)
	}
	cacheRoot := storeRoot + "/images"

	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	t.Log("building base image (cached if present) …")
	img, buildErr := BuildAgentBaseImage(context.Background(), cache)
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

	// ── Step 3: infrastructure dirs (short paths for AF_UNIX sun_path limit) ──
	socketDir, err := os.MkdirTemp("/tmp", "orca-sv-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}

	stateDir, err := os.MkdirTemp("/tmp", "orca-sv-state-")
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("MkdirTemp stateDir: %v", err)
	}

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New(svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var supervisorPID int
	var sandboxID domain.SandboxID

	t.Cleanup(func() {
		// Best-effort: stop supervisor and clean up.
		if supervisorPID > 0 {
			sock := supervisor.SockPath(stateDir)
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := supervisor.StopSupervisor(stopCtx, sock); err != nil {
				t.Logf("cleanup: StopSupervisor: %v (may be already gone)", err)
			}
		}
		if content, err := os.ReadFile(supervisor.SockPath(stateDir)); err == nil && len(content) > 0 {
			t.Logf("supervisor.sock content on failure: %s", content)
		}
		if content, err := os.ReadFile(stateDir + "/supervisor.log"); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== supervisor.log ===\n%s", content)
		}
		os.RemoveAll(socketDir)
		os.RemoveAll(stateDir)
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = svc.Remove(rmCtx, sandboxID.String())
		}
	})

	// ── Step 4: CreateAndBoot — provision disk + brief initial boot ────────────
	//
	// Mirrors orcaCreate's CreateAndBoot call: creates the disk CoW copy, mints
	// the sandbox record, boots the VM briefly so we can capture the disk path.
	// No WireClaudeEgress — the supervisor owns the perimeter (S2 invariant).
	kernelPath := kernelPathSH(t, repoRoot)

	var diskPath string
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(resolvedExt4 string, _ []service.ExtraDisk) (driver.Driver, error) {
		diskPath = resolvedExt4
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:   chBin,
			SocketDir:    socketDir,
			KernelPath:   kernelPath,
			DiskImagePath: resolvedExt4,
			StartTimeout: 30 * time.Second,
		})
		return bootDrv, newErr
	})

	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return reachableViaDrv(ctx, drv, id)
	})

	t.Log("CreateAndBoot (initial brief boot) …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()
	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"orca-sv-test", fmt.Sprintf("osv-%d", time.Now().UnixNano()),
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
	t.Logf("sandbox provisioned: id=%s disk=%s", sb.ID, diskPath)

	t.Log("waiting for guest agent (initial boot) …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)

	// ── Step 5: Stop — supervisor will re-boot ────────────────────────────────
	t.Log("stopping sandbox (disk retained, supervisor will re-boot) …")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopCancel()
	if _, err := svc.Stop(stopCtx, sb.ID.String()); err != nil {
		t.Fatalf("svc.Stop: %v", err)
	}
	t.Log("sandbox stopped; disk retained at", diskPath)

	if diskPath == "" {
		t.Fatal("diskPath not captured from factory — cannot spawn supervisor")
	}

	// ── Step 6: SpawnDetached — supervisor takes ownership (orcaCreate path) ──
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
	sockPath := supervisor.SockPath(stateDir)
	t.Logf("supervisor ready: pid=%d sock=%s", pid, sockPath)

	// ── Step 7: SetSupervisor — persist PID+sock (orcaCreate path) ────────────
	if err := svc.SetSupervisor(context.Background(), sb.ID, pid, sockPath); err != nil {
		t.Fatalf("svc.SetSupervisor: %v", err)
	}

	// Verify persistence: re-read from store.
	updated, err := svc.GetSandboxByID(context.Background(), sb.ID)
	if err != nil {
		t.Fatalf("GetSandboxByID: %v", err)
	}
	if updated.SupervisorPID != pid {
		t.Errorf("SupervisorPID: got %d, want %d", updated.SupervisorPID, pid)
	}
	if updated.SupervisorSock != sockPath {
		t.Errorf("SupervisorSock: got %q, want %q", updated.SupervisorSock, sockPath)
	}
	t.Logf("persistence verified: SupervisorPID=%d SupervisorSock=%s", updated.SupervisorPID, updated.SupervisorSock)

	// ── Step 8: Assert supervisor process is alive ─────────────────────────────
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("supervisor process pid=%d not alive after SpawnDetached: %v", pid, err)
	} else {
		t.Logf("supervisor process pid=%d is alive ✓", pid)
	}

	// ── Step 9: Verify guest agent reachable under supervisor-owned VM ─────────
	shadowDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:    chBin,
		SocketDir:     socketDir,
		KernelPath:    kernelPath,
		DiskImagePath: diskPath,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New(shadowDrv): %v", err)
	}
	t.Log("waiting for guest agent under supervisor-owned VM …")
	waitForAgentSH(t, shadowDrv, sb.ID, 60*time.Second)
	t.Log("guest agent reachable ✓")

	// ── Step 10: StopSupervisor — orcaDestroy path ────────────────────────────
	t.Logf("stopping supervisor via IPC: sock=%s", sockPath)
	stopSupCtx, stopSupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopSupCancel()
	if err := supervisor.StopSupervisor(stopSupCtx, sockPath); err != nil {
		t.Fatalf("StopSupervisor: %v", err)
	}
	t.Log("StopSupervisor returned OK ✓")
	supervisorPID = 0 // cleanup already done

	// ── Step 11: Assert supervisor process is gone ─────────────────────────────
	// Give the process a moment to fully exit.
	time.Sleep(500 * time.Millisecond)
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Errorf("supervisor process pid=%d still alive after StopSupervisor", pid)
	} else {
		t.Logf("supervisor process pid=%d is gone ✓", pid)
	}

	// ── Step 12: sandbox state is Stopped after supervisor shutdown ────────────
	final, err := svc.GetSandboxByID(context.Background(), sb.ID)
	if err != nil {
		t.Fatalf("GetSandboxByID (final): %v", err)
	}
	if final.State != domain.Stopped {
		t.Errorf("sandbox state after StopSupervisor: got %s, want %s", final.State, domain.Stopped)
	} else {
		t.Logf("sandbox state = Stopped ✓")
	}
}

// reachableViaDrv polls the agent control port until ctx expires or it answers.
func reachableViaDrv(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
	gd, ok := drv.(driver.GuestDialer)
	if !ok {
		return nil
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := gd.DialGuest(dialCtx, id, driver.AgentControlPort)
		cancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
}
