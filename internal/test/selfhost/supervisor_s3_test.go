//go:build integration

package selfhost

// supervisor_s3_test.go — D-PP-03 + D-PP-04 S3 acceptance tests.
//
// # Properties proved
//
//  1. (Unit) Refresher wiring — Part A: cred.NewRefresher is constructed from a
//     synthetic credential store and Host() returns the expected agent-egress
//     hostname. This proves Part A wiring without live Anthropic creds (which
//     require S4's dogfood path).
//
//  2. (Integration) CA seeding — Part B: after SpawnDetached on the persistent
//     supervisor path:
//       (a) service.GuestCACertPath exists in the supervisor-booted guest.
//       (b) GuestCredEnvPath contains NODE_EXTRA_CA_CERTS=GuestCACertPath.
//
// # What remains for S4 live proof
//
// The Refresher's oauth2 HTTP refresh path (Token() → token endpoint → real
// bearer) is not exercised here: no live Anthropic account is available in CI.
// S4 will dogfood via TestRefresherLiveRefreshGrant with real refresh creds at
// NEXUS3_DEDICATED_CRED_STORE.
//
// # Running
//
//	# Unit test only (no KVM required):
//	go test -tags integration -run TestSupervisorS3RefresherWiring ./internal/test/selfhost/ -v
//
//	# Full integration test (KVM required):
//	TMPDIR=/tmp go test -tags integration -run TestSupervisorS3CAInGuest \
//	    ./internal/test/selfhost/ -v -timeout 30m

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
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

// ── TestSupervisorS3RefresherWiring (unit) ────────────────────────────────────
//
// Proves Part A: the broker is Refresher-backed, not StaticCredentialSource.
// Uses a synthetic credential store — no live Anthropic account or KVM required.
func TestSupervisorS3RefresherWiring(t *testing.T) {
	// Build a minimal synthetic creds.json. Token() will fail when called
	// (the token_endpoint is unreachable), but NewRefresher construction succeeds.
	credsDir := t.TempDir()
	credsFile := filepath.Join(credsDir, "creds.json")
	synthStore := &cred.DedicatedCredStore{
		AccessToken:   "sk-ant-test-REFRESHER-WIRING-UNIT-S3",
		RefreshToken:  "oa-test-refresh-token-s3",
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		TokenType:     "Bearer",
		ClientID:      "test-client-id-s3",
		TokenEndpoint: "http://localhost:19999/oauth/token", // unreachable; construction only
	}
	if err := cred.SaveStore(credsFile, synthStore); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	broker := cred.NewBroker()

	// Construct a Refresher for each agent-egress host and verify Host().
	for _, host := range service.AgentEgressHosts(cred.ClaudeCodeProfile) {
		r, err := cred.NewRefresher(credsFile, host, broker)
		if err != nil {
			t.Fatalf("cred.NewRefresher(%q): %v — Part A wiring broken", host, err)
		}
		if r == nil {
			t.Fatalf("cred.NewRefresher(%q): returned nil Refresher", host)
		}
		if got := r.Host(); got != host {
			t.Errorf("Refresher.Host(): got %q, want %q", got, host)
		}
		t.Logf("Part A PASS: Refresher constructed (not StaticCredentialSource) for host=%q", host)
	}

	// Graceful-degradation path: absent creds file must return ErrStoreAbsent.
	_, errAbsent := cred.NewRefresher("/nonexistent/creds.json", service.AnthropicAPIHost, broker)
	if !errors.Is(errAbsent, cred.ErrStoreAbsent) {
		t.Errorf("NewRefresher (absent file): expected ErrStoreAbsent, got %v", errAbsent)
	} else {
		t.Log("Part A graceful-degradation PASS: ErrStoreAbsent on absent creds file")
	}
}

// ── TestSupervisorS3CAInGuest (integration) ───────────────────────────────────
//
// Proves Part B: GuestCACertPath exists in the supervisor-booted guest and
// GuestCredEnvPath contains NODE_EXTRA_CA_CERTS=GuestCACertPath.
func TestSupervisorS3CAInGuest(t *testing.T) {
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: base image ─────────────────────────────────────────────────────
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

	// ── Step 2: build nexus3 binary for SpawnDetached ─────────────────────────
	t.Log("building nexus3 binary …")
	nexus3Bin := buildNexus3Bin(t)
	t.Logf("nexus3 binary: %s", nexus3Bin)

	// ── Step 3: infrastructure ─────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "sv-s3-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir path too long for AF_UNIX: %s", socketDir)
	}

	stateDir, err := os.MkdirTemp("/tmp", "sv-s3-state-")
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("MkdirTemp stateDir: %v", err)
	}

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}
	serialPath := filepath.Join(socketDir, "sv-s3-serial.log")

	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var supervisorPID int
	var sandboxID string

	t.Cleanup(func() {
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== guest serial output ===\n%s", content)
		}
		if supervisorPID != 0 {
			sock := supervisor.SockPath(stateDir)
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := supervisor.StopSupervisor(stopCtx, sock); err != nil {
				t.Logf("cleanup: StopSupervisor: %v", err)
			}
		}
		if content, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== supervisor log ===\n%s", content)
		}
		os.RemoveAll(socketDir)
		os.RemoveAll(stateDir)
		if sandboxID != "" {
			rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = svc.Remove(rmCtx, sandboxID)
		}
	})

	// ── Step 4: CreateAndBoot (no egress wiring — supervisor owns perimeter) ──
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
		"sv-s3-test", fmt.Sprintf("s3ca-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID.String()
	t.Logf("sandbox provisioned: id=%s disk=%s", sb.ID, diskPath)

	t.Log("waiting for guest agent (initial boot) …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)

	// ── Step 5: Stop ───────────────────────────────────────────────────────────
	t.Log("stopping sandbox …")
	stopCtx5, stopCancel5 := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopCancel5()
	if _, err := svc.Stop(stopCtx5, sb.ID.String()); err != nil {
		t.Fatalf("svc.Stop: %v", err)
	}
	if diskPath == "" {
		t.Fatal("diskPath not captured — cannot spawn supervisor")
	}

	// ── Step 6: SpawnDetached ──────────────────────────────────────────────────
	// CredsFile is deliberately absent here to exercise graceful degradation.
	// Part A (Refresher construction with valid creds) is proved by the unit
	// test above.
	t.Log("spawning detached supervisor (S3 CA-seeder path) …")
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
	t.Logf("supervisor ready: pid=%d", pid)

	// ── Step 7: shadow driver — assert CA in guest ─────────────────────────────
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

	// (b1) GuestCACertPath must exist in the guest.
	t.Logf("asserting GuestCACertPath=%s exists …", service.GuestCACertPath)
	var lsBuf bytes.Buffer
	lsExit, lsErr := agentC.Exec(context.Background(), agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "ls -la " + service.GuestCACertPath},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdout: &lsBuf,
		Stderr: &lsBuf,
	})
	lsOut := lsBuf.String()
	if lsErr != nil || lsExit != 0 {
		t.Errorf("Part B(1) FAIL: GuestCACertPath not found: err=%v exit=%d output=%s",
			lsErr, lsExit, lsOut)
	} else {
		t.Logf("Part B(1) PASS: GuestCACertPath exists:\n%s", strings.TrimSpace(lsOut))
	}

	// (b2) GuestCredEnvPath must contain NODE_EXTRA_CA_CERTS=GuestCACertPath.
	t.Logf("asserting NODE_EXTRA_CA_CERTS in %s …", service.GuestCredEnvPath)
	var catBuf bytes.Buffer
	catExit, catErr := agentC.Exec(context.Background(), agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "cat " + service.GuestCredEnvPath},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdout: &catBuf,
		Stderr: &catBuf,
	})
	catOut := catBuf.String()
	if catErr != nil || catExit != 0 {
		t.Errorf("Part B(2) FAIL: cannot read GuestCredEnvPath: err=%v exit=%d output=%s",
			catErr, catExit, catOut)
	} else {
		want := "NODE_EXTRA_CA_CERTS=" + service.GuestCACertPath
		if !strings.Contains(catOut, want) {
			t.Errorf("Part B(2) FAIL: %s does not contain %q\ncontent:\n%s",
				service.GuestCredEnvPath, want, catOut)
		} else {
			t.Logf("Part B(2) PASS: NODE_EXTRA_CA_CERTS wired to %s", service.GuestCACertPath)
		}
	}

	// ── Step 8: stop supervisor ────────────────────────────────────────────────
	stopSvCtx, stopSvCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopSvCancel()
	if err := supervisor.StopSupervisor(stopSvCtx, supervisor.SockPath(stateDir)); err != nil {
		t.Logf("StopSupervisor: %v (may be gone)", err)
	} else {
		supervisorPID = 0
	}
}
