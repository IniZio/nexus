//go:build integration

package selfhost

// Package supervisor_s4_test — D-PP-04 S4 acceptance tests.
// Tests: BoundedRetry (Part 2), OrphanReconcile (Part 3), PlaceholderInGuest (AC-7),
// LiveEgress (AC-5). See test functions for assertions and operator setup steps.

import (
	"bytes"
	"context"
	"crypto/x509"
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
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// Proves Part 2: SeedLoop exits after maxAttempts when seeders always fail.
func TestSupervisorS4BoundedRetryReady(t *testing.T) {
	t.Parallel()

	id := domain.NewSandboxID()
	broker := cred.NewBroker()

	failSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
		return errors.New("seeder: always fails")
	})

	fakeCert := &x509.Certificate{} // non-nil cert ensures seeding body is entered
	cert := fakeCert

	const maxAttempts = 3
	start := time.Now()

	// nil svc is safe: cert != nil so GetPerimeterCACert is never called.
	done, guestEverResponded := supervisor.SeedLoop(context.Background(), id, &cert,
		failSeeder, failSeeder, broker, nil, maxAttempts, 0, nil, true, cred.ClaudeCodeProfile, nil, nil)
	elapsed := time.Since(start)

	if done {
		t.Fatal("SeedLoop returned true but seeders always fail — expected false")
	}
	if guestEverResponded {
		t.Fatal("SeedLoop reported guestEverResponded=true but CA seeder always fails — expected false")
	}
	t.Logf("PASS: SeedLoop exited after cap=%d in %v (returned false, guestEverResponded=false — caller must NOT write READY)",
		maxAttempts, elapsed)
}

// Proves Part 3: PidAlive and CheckAndReconcile.
func TestSupervisorS4OrphanReconcile(t *testing.T) {
	t.Parallel()

	// (a) zero PID → false
	if supervisor.PidAlive(0) {
		t.Error("PidAlive(0): expected false, got true")
	} else {
		t.Log("PASS (a): PidAlive(0) == false")
	}

	// (b) our own PID → true
	if !supervisor.PidAlive(os.Getpid()) {
		t.Errorf("PidAlive(%d): expected true, got false", os.Getpid())
	} else {
		t.Logf("PASS (b): PidAlive(%d) == true (self)", os.Getpid())
	}

	// (c) exited PID → false
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start /bin/true: %v", err)
	}
	deadPid := cmd.Process.Pid
	_ = cmd.Wait()
	if supervisor.PidAlive(deadPid) {
		t.Errorf("PidAlive(%d): expected false for exited process", deadPid)
	} else {
		t.Logf("PASS (c): PidAlive(%d) == false (exited)", deadPid)
	}

	// (d) CheckAndReconcile(dead PID) cleans stale files
	stateDir := t.TempDir()
	pidfile := supervisor.PidfilePath(stateDir)
	sockfile := supervisor.SockPath(stateDir)
	if err := os.WriteFile(pidfile, []byte("99999\n"), 0o644); err != nil {
		t.Fatalf("write fake pidfile: %v", err)
	}
	if err := os.WriteFile(sockfile, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write fake sockfile: %v", err)
	}

	alive, err := supervisor.CheckAndReconcile(deadPid, sockfile)
	if err != nil {
		t.Errorf("CheckAndReconcile(dead): unexpected error: %v", err)
	}
	if alive {
		t.Error("CheckAndReconcile(dead): expected alive=false, got true")
	}
	if _, statErr := os.Stat(pidfile); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("CheckAndReconcile(dead): pidfile still present after cleanup")
	}
	if _, statErr := os.Stat(sockfile); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("CheckAndReconcile(dead): sockfile still present after cleanup")
	}
	t.Logf("PASS (d): CheckAndReconcile(dead=%d) cleaned stale files", deadPid)

	// (e) CheckAndReconcile(live PID) reports alive
	alive2, err2 := supervisor.CheckAndReconcile(os.Getpid(), "")
	if err2 != nil {
		t.Errorf("CheckAndReconcile(live): unexpected error: %v", err2)
	}
	if !alive2 {
		t.Errorf("CheckAndReconcile(live=%d): expected alive=true", os.Getpid())
	} else {
		t.Logf("PASS (e): CheckAndReconcile(live=%d) == alive", os.Getpid())
	}
}

// Proves Part 1 + AC-7: agent placeholder in guest, no real credentials on disk.
func TestSupervisorS4PlaceholderInGuest(t *testing.T) {
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	storeRoot := t.TempDir()
	cacheRoot := filepath.Join(storeRoot, "images")
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
	t.Logf("base image ready: digest=%s", img.Digest)

	t.Log("building nexus3 binary …")
	nexus3Bin := buildNexus3Bin(t)
	t.Logf("nexus3 binary: %s", nexus3Bin)

	socketDir, err := os.MkdirTemp("/tmp", "sv-s4-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}
	stateDir, err := os.MkdirTemp("/tmp", "sv-s4-state-")
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("MkdirTemp stateDir: %v", err)
	}
	serialPath := filepath.Join(socketDir, "sv-s4-serial.log")

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
	var sandboxRef string

	t.Cleanup(func() {
		if supervisorPID != 0 {
			sock := supervisor.SockPath(stateDir)
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := supervisor.StopSupervisor(stopCtx, sock); err != nil {
				t.Logf("cleanup: StopSupervisor: %v", err)
			}
		}
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== serial ===\n%s", content)
		}
		if content, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== supervisor log ===\n%s", content)
		}
		os.RemoveAll(socketDir)
		os.RemoveAll(stateDir)
		if sandboxRef != "" {
			rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = svc.Remove(rmCtx, sandboxRef)
		}
	})

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

	t.Log("CreateAndBoot (initial brief boot) …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()

	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"sv-s4-test", fmt.Sprintf("s4ph-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxRef = sb.ID.String()
	t.Logf("sandbox provisioned: id=%s disk=%s", sb.ID, diskPath)

	t.Log("waiting for guest agent (initial boot) …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)

	stopCtx5, stopCancel5 := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopCancel5()
	if _, err := svc.Stop(stopCtx5, sb.ID.String()); err != nil {
		t.Fatalf("svc.Stop: %v", err)
	}
	if diskPath == "" {
		t.Fatal("diskPath not captured")
	}

	// SpawnDetached without CredsFile: placeholder only (D-PD-32 security narrowing)
	t.Log("spawning detached supervisor (S4 placeholder path, no creds) …")
	spawnCfg := supervisor.SpawnConfig{
		Config: supervisor.Config{
			SandboxRef: sb.ID.String(),
			StoreRoot:  storeRoot,
			StateDir:   stateDir,
			CHBin:      chBin,
			SocketDir:  socketDir,
			KernelPath: kernelPath,
			DiskPath:   diskPath,
			// CredsFile deliberately absent: proves zero-cred even in live mode.
		},
		Exe:          nexus3Bin,
		ReadyTimeout: 5 * time.Minute,
	}
	pid, _, err := supervisor.SpawnDetached(spawnCfg)
	if err != nil {
		t.Fatalf("supervisor.SpawnDetached: %v", err)
	}
	supervisorPID = pid
	t.Logf("supervisor ready: pid=%d", pid)

	shadowDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:    chBin,
		SocketDir:     socketDir,
		KernelPath:    kernelPath,
		DiskImagePath: diskPath,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (shadowDrv): %v", err)
	}

	t.Log("waiting for guest agent (supervisor-booted VM) …")
	waitForAgentSH(t, shadowDrv, sb.ID, 60*time.Second)

	agentC := agent.NewClient(shadowDrv, sb.ID)

	execGuest := func(cmd string) (string, int32) {
		t.Helper()
		var outBuf bytes.Buffer
		execCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		code, execErr := agentC.Exec(execCtx, agent.ExecOptions{
			Argv:   []string{"/bin/sh", "-c", cmd},
			Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			Stdout: &outBuf,
			Stderr: &outBuf,
		})
		if execErr != nil {
			t.Logf("exec %q: err=%v", cmd, execErr)
		}
		return outBuf.String(), code
	}

	// (a) GuestCACertPath must exist and contain a PEM cert.
	caOut, caCode := execGuest("cat " + service.GuestCACertPath)
	if caCode != 0 || !strings.Contains(caOut, "BEGIN CERTIFICATE") {
		t.Errorf("FAIL (a): GuestCACertPath missing or not a PEM cert (exit %d): %q",
			caCode, truncateS4(caOut, 120))
	} else {
		t.Logf("PASS (a): GuestCACertPath contains PEM certificate")
	}

	// (b) GuestCredEnvPath must be ABSENT for this sandbox.
	// This sandbox is created without UseAgentSeed, so sb.AgentName="". RunDetached
	// passes AgentName!="" as seedAgentCreds to SeedLoop (supervisor.go); with
	// seedAgentCreds=false SeedLoop writes only the CA cert, not the agent
	// placeholder env file. Asserting absence is the correct post-security-narrowing
	// expectation (deliberate design: do not hand credential env vars to a guest
	// that runs no agent). The old assertion that cred.env must be present was
	// written before that narrowing and was never updated.
	_, credCode := execGuest("test -e " + service.GuestCredEnvPath)
	if credCode == 0 {
		t.Errorf("FAIL (b): GuestCredEnvPath unexpectedly present for non-agent sandbox — " +
			"agent placeholder must not be seeded when AgentName is empty (D-PD-32 security narrowing)")
	} else {
		t.Logf("PASS (b): GuestCredEnvPath correctly absent for non-agent sandbox (seedAgentCreds=false)")
	}

	// (c) AC-7 zero-cred: grep guest disk for real token markers (sk-ant-, refresh_token, etc.)
	zeroOut, _ := execGuest(
		`grep -rI 'sk-ant-\|refresh_token\|access_token\|anthropic_api_key' /root /home 2>/dev/null || true`)
	if strings.TrimSpace(zeroOut) != "" {
		t.Errorf("AC-7 FAIL (c): real cred material found on guest disk:\n%s", zeroOut)
	} else {
		t.Logf("PASS (c): AC-7 zero-cred-in-guest: no real token material found")
	}

	// (d) D-M4 mutation guard: shell-profile drop-in must seed /etc/profile.d/nexus3-cred.sh
	profContent, profCode := execGuest("cat " + service.GuestShellProfilePath)
	if profCode != 0 {
		t.Errorf("D-M4 FAIL (d): shell-profile drop-in absent from guest at %s (exit %d)\n"+
			"Deleting probeAndSeedGuest call from RunDetached causes this failure.",
			service.GuestShellProfilePath, profCode)
	} else {
		if !strings.Contains(profContent, service.GuestCredEnvPath) {
			t.Errorf("D-M4 FAIL (d): drop-in at %s does not reference GuestCredEnvPath (%s)\ncontent: %q",
				service.GuestShellProfilePath, service.GuestCredEnvPath, profContent)
		} else {
			t.Logf("PASS (d): shell-profile drop-in present and references GuestCredEnvPath (D-M4 guard)")
		}
	}

	stopSvCtx, stopSvCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopSvCancel()
	if err := supervisor.StopSupervisor(stopSvCtx, supervisor.SockPath(stateDir)); err != nil {
		t.Logf("StopSupervisor: %v (may be gone)", err)
	} else {
		supervisorPID = 0
	}
}

// AC-5 live proof: SpawnDetached with live creds, MITM swaps placeholder → real bearer.
func TestSupervisorS4LiveEgress(t *testing.T) {
	storePath := service.DefaultDedicatedCredStorePath()
	if _, statErr := os.Stat(storePath); errors.Is(statErr, os.ErrNotExist) {
		t.Skipf(
			"SKIP TestSupervisorS4LiveEgress: dedicated cred store absent at %q\n"+
				"Operator steps to prove AC-5 live 200:\n"+
				"  1. In a DEDICATED terminal (NOT your main claude.ai login):\n"+
				"       nexus3 auth login --force\n"+
				"     (writes dedicated OAuth session — do NOT reuse main login)\n"+
				"  2. export NEXUS3_DEDICATED_CRED_STORE=%s\n"+
				"  3. TMPDIR=/tmp go test -tags integration -count=1 \\\n"+
				"         -run TestSupervisorS4LiveEgress \\\n"+
				"         ./internal/test/selfhost/ -v -timeout 30m",
			storePath, storePath)
	}
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	t.Logf("live cred store: %s — running AC-5 live proof", storePath)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	storeRoot := t.TempDir()
	cacheRoot := filepath.Join(storeRoot, "images")
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	t.Log("building agent base image …")
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer imgCancel()
	img, buildErr := BuildAgentBaseImage(imgCtx, cache)
	switch {
	case buildErr == nil:
	case errors.Is(buildErr, ErrDockerUnavailable):
		t.Skip("skipping: docker unavailable:", buildErr)
	case errors.Is(buildErr, builder.ErrMke2fsUnavailable):
		t.Skip("skipping: mke2fs unavailable:", buildErr)
	default:
		t.Fatalf("BuildAgentBaseImage: %v", buildErr)
	}
	t.Logf("base image: %s", img.Digest)

	nexus3Bin := buildNexus3Bin(t)
	t.Logf("nexus3 binary: %s", nexus3Bin)

	socketDir, err := os.MkdirTemp("/tmp", "sv-s4live-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}
	stateDir, err := os.MkdirTemp("/tmp", "sv-s4live-state-")
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("MkdirTemp stateDir: %v", err)
	}
	serialPath := filepath.Join(socketDir, "sv-s4live-serial.log")

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
	var sandboxRef string

	t.Cleanup(func() {
		if supervisorPID != 0 {
			sock := supervisor.SockPath(stateDir)
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if stopErr := supervisor.StopSupervisor(stopCtx, sock); stopErr != nil {
				t.Logf("cleanup StopSupervisor: %v", stopErr)
			}
		}
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== serial ===\n%s", content)
		}
		if content, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil && len(content) > 0 {
			t.Logf("=== supervisor log ===\n%s", content)
		}
		os.RemoveAll(socketDir)
		os.RemoveAll(stateDir)
		if sandboxRef != "" {
			rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = svc.Remove(rmCtx, sandboxRef)
		}
	})

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

	t.Log("CreateAndBoot (initial brief boot) …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()

	// AllowedHosts set before CreateAndBoot for supervisor perimeter allowlist
	liveHosts := append(service.AgentEgressHosts(cred.ClaudeCodeProfile), "github.com")
	liveOpts := service.CreateAndBootOptions{
		Image:               service.ImageSpec{Digest: string(img.Digest)},
		CacheRoot:           cacheRoot,
		ReachabilityTimeout: 60 * time.Second,
		AllowedHosts:        liveHosts,
	}
	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"sv-s4live", fmt.Sprintf("s4live-%d", time.Now().UnixNano()),
		liveOpts,
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxRef = sb.ID.String()
	t.Logf("initial boot done: sandbox=%s disk=%s", sb.ID, diskPath)

	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)

	stopCtx5, stopCancel5 := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopCancel5()
	if _, err := svc.Stop(stopCtx5, sb.ID.String()); err != nil {
		t.Fatalf("svc.Stop: %v", err)
	}
	if diskPath == "" {
		t.Fatal("diskPath not captured")
	}

	// SpawnDetached with CredsFile: Refresher mints real token, MITM swaps it into bearer
	t.Logf("spawning supervisor with live creds: %s", storePath)
	spawnCfg := supervisor.SpawnConfig{
		Config: supervisor.Config{
			SandboxRef: sb.ID.String(),
			StoreRoot:  storeRoot,
			StateDir:   stateDir,
			CHBin:      chBin,
			SocketDir:  socketDir,
			KernelPath: kernelPath,
			DiskPath:   diskPath,
			CredsFile:  storePath, // live OAuth creds; Refresher exchanges refresh_token
		},
		Exe:          nexus3Bin,
		ReadyTimeout: 5 * time.Minute,
	}
	pid, _, err := supervisor.SpawnDetached(spawnCfg)
	if err != nil {
		t.Fatalf("SpawnDetached (live creds): %v", err)
	}
	supervisorPID = pid
	t.Logf("supervisor READY (live creds): pid=%d", pid)

	shadowDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:    chBin,
		SocketDir:     socketDir,
		KernelPath:    kernelPath,
		DiskImagePath: diskPath,
	})
	if err != nil {
		t.Fatalf("shadow driver: %v", err)
	}
	t.Log("waiting for guest agent (supervisor-booted VM, live creds) …")
	waitForAgentSH(t, shadowDrv, sb.ID, 60*time.Second)
	agentC := agent.NewClient(shadowDrv, sb.ID)

	execGuest := func(name, cmd string, timeoutSec int) (out string, code int32) {
		t.Helper()
		var buf bytes.Buffer
		execCtx, cancel := context.WithTimeout(context.Background(),
			time.Duration(timeoutSec)*time.Second)
		defer cancel()
		code, execErr := agentC.Exec(execCtx, agent.ExecOptions{
			Argv: []string{"/bin/sh", "-c", cmd},
			Env: map[string]string{
				"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				"HOME": "/root",
			},
			Stdout: &buf,
			Stderr: &buf,
		})
		out = buf.String()
		if execErr != nil {
			t.Logf("%s exec err: %v", name, execErr)
		}
		t.Logf("%s exit=%d output=%q", name, code, truncateS4(out, 300))
		return out, code
	}

	// (A) AC-5 node HTTPS to api.anthropic.com: MITM intercepts, swaps placeholder→real bearer
	// Debug: verify cert file and cred.env before node probe
	debugOut, _ := execGuest("debug-cert",
		`ls -la /usr/local/share/ca-certificates/nexus3-mitm.crt /run/nexus3/cred.env 2>&1 && `+
			`head -1 /run/nexus3/cred.env && `+
			`openssl x509 -in /usr/local/share/ca-certificates/nexus3-mitm.crt -noout -subject 2>&1 | head -1`,
		15)
	t.Logf("debug cert/env: %s", truncateS4(debugOut, 400))

	t.Log("AC-5 assertion A: node HTTPS api.anthropic.com/v1/models → expect 200 …")
	// Source /run/nexus3/cred.env so NODE_EXTRA_CA_CERTS and placeholder are inherited by node
	nodeHTTPScript := `set -a; . /run/nexus3/cred.env; node -e "
const https = require('https');
const token = process.env.CLAUDE_CODE_OAUTH_TOKEN || process.env.ANTHROPIC_AUTH_TOKEN || '';
const opts = {
  hostname: 'api.anthropic.com',
  path: '/v1/models',
  method: 'GET',
  headers: {
    'anthropic-version': '2023-06-01',
    'Authorization': 'Bearer ' + token
  }
};
const req = https.request(opts, res => {
  process.stdout.write(String(res.statusCode));
  res.resume();
  res.on('end', () => process.exit(0));
});
req.on('error', e => { process.stderr.write(e.message); process.exit(1); });
req.end();
"`
	nodeOut, _ := execGuest("node-anthropic", nodeHTTPScript, 60)
	httpCode := strings.TrimSpace(nodeOut)
	if httpCode != "200" {
		t.Errorf("AC-5 FAIL (A): api.anthropic.com/v1/models returned HTTP %q (expected 200)\n"+
			"  If 401: the refresh_token in %s is stale.\n"+
			"  Fix: run `nexus3 auth login --force` in a fresh DEDICATED terminal,\n"+
			"       then re-export NEXUS3_DEDICATED_CRED_STORE and rerun the test.",
			httpCode, storePath)
	} else {
		t.Logf("PASS (A): api.anthropic.com/v1/models → HTTP %s", httpCode)
	}

	// (B) git clone: non-Node.js HTTPS (libssl) through MITM CA in system bundle
	t.Log("AC-5 assertion B: git clone public HTTPS repo via system trust store …")
	cloneOut, cloneCode := execGuest("git-clone",
		`git clone --depth=1 https://github.com/anthropics/anthropic-sdk-go.git /tmp/sdk-clone 2>&1 && \
		ls /tmp/sdk-clone | head -5`,
		120)
	if cloneCode != 0 {
		t.Errorf("AC-5 FAIL (B): git clone exit=%d output=%q", cloneCode, cloneOut)
	} else {
		t.Logf("PASS (B): git clone exit=0, files: %s", truncateS4(cloneOut, 200))
	}

	// (C) AC-7 zero-cred: real token must never reach guest disk even with live creds
	t.Log("AC-7 zero-cred assertion C: grep guest for real token markers …")
	zeroOut, _ := execGuest("zero-cred",
		`grep -rI 'sk-ant-\|refresh_token\|access_token\|anthropic_api_key' /root /home 2>/dev/null || true`,
		15)
	if strings.TrimSpace(zeroOut) != "" {
		t.Errorf("AC-7 FAIL (C): real cred material found on guest disk:\n%s", zeroOut)
	} else {
		t.Log("PASS (C): AC-7 zero-cred-in-guest: no real token material on disk")
	}

	t.Logf("stopping supervisor pid=%d …", supervisorPID)
	stopSvCtx, stopSvCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopSvCancel()
	if stopErr := supervisor.StopSupervisor(stopSvCtx, supervisor.SockPath(stateDir)); stopErr != nil {
		t.Logf("StopSupervisor: %v", stopErr)
	} else {
		t.Log("supervisor stop acknowledged")
		supervisorPID = 0
	}
	time.Sleep(2 * time.Second) // brief wait for supervisor exit before PID check
	if !supervisor.PidAlive(pid) {
		t.Logf("PASS (D): supervisor pid=%d no longer alive after stop", pid)
	} else {
		t.Logf("supervisor pid=%d still alive briefly after stop (may still be shutting down)", pid)
	}
}

// Caps s at n runes for safe error messages.
func truncateS4(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
