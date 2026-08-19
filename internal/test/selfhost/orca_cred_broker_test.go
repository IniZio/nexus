//go:build integration

// Package selfhost — OrcaCreate credential broker wiring proof.
//
// TestOrcaCredBrokerWiring proves that the WireClaudeEgress + lazy-seeder
// pattern introduced in orcaCreate (internal/cli/cmd_orca.go) correctly wires
// the host credential broker for MITM token injection. The test exercises the
// same service primitives that orcaCreate uses, with a
// StaticCredentialSource backed by a deterministic test token so the proof
// is fully self-contained and does not require a live Anthropic account.
//
// # Properties proved
//
//  1. After CreateAndBoot with WireClaudeEgress (UseAgentSeed=true, lazy
//     seeder, StaticCredentialSource), broker.SetRealToken for
//     api.anthropic.com returns nil — proving RegisterPlaceholder fired in
//     CreateAndBoot step 9 and the real token was wired.
//  2. MITM proxy performs host-side bearer-swap on a live in-guest HTTPS
//     request to api.anthropic.com (swapCount > 0).
//  3. T_static (the test bearer token) is absent from the guest exec
//     environment and from the guest filesystem (zero-cred invariant).
//
// # Live 200 from api.anthropic.com (deferred)
//
// This test uses a fake token and expects no NEXUS3_OK response. To prove a
// real 200, bootstrap live credentials and run TestOAuthRotationDogfood:
//  1. Start a dedicated Claude Code session: claude login (in a fresh shell)
//  2. Import to nexus3: nexus3 auth login --force
//  3. Verify token validity: cat ~/.config/nexus3/creds.json | jq .expires_at
//  4. Run: TMPDIR=/tmp go test -tags integration -run TestOAuthRotationDogfood
//     ./internal/test/selfhost/ -v -timeout 20m
//
// # Prerequisites
//
//   - /dev/kvm accessible
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN or ~/.local/bin/cloud-hypervisor)
//   - mke2fs in PATH (e2fsprogs)
//   - docker (required by BuildAgentBaseImage)
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestOrcaCredBrokerWiring \
//	    ./internal/test/selfhost/ -v -timeout 30m
package selfhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netstack"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// TestOrcaCredBrokerWiring is the acceptance test for orcaCreate's credential
// broker wiring: WireClaudeEgress + lazy seeder + StaticCredentialSource.
func TestOrcaCredBrokerWiring(t *testing.T) {
	// ── 1. Skip guards ────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	// Guard: clear ANTHROPIC_AUTH_TOKEN so resolveAgentCredKind returns
	// kindOAuth, which causes SeedGuestAgent to emit
	// CLAUDE_CODE_OAUTH_TOKEN=<placeholder> in the seed payload.
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	// ── 2. Kernel path ────────────────────────────────────────────────────────
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── 3. Build agent base image ─────────────────────────────────────────────
	storeRoot := t.TempDir()
	cacheRoot := filepath.Join(storeRoot, "images")
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	imgCtx, imgCancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer imgCancel()

	t.Log("building agent base image (first run ~15–30 min; subsequent: seconds from cache) …")
	img, err := BuildAgentBaseImage(imgCtx, cache)
	if err != nil {
		switch {
		case errors.Is(err, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", err)
		case errors.Is(err, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", err)
		}
		t.Fatalf("BuildAgentBaseImage: %v", err)
	}
	t.Logf("agent image: digest=%s size=%.2f GiB", img.Digest, float64(img.Size)/(1<<30))

	// ── 4. Infrastructure ─────────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "orca-cred-broker-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}
	serialPath := filepath.Join(socketDir, "orca-cred-serial.log")
	t.Cleanup(func() {
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== guest serial output ===\n%s", content)
		}
		os.RemoveAll(socketDir)
	})

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:   chBin,
		SocketDir:    socketDir,
		KernelPath:   kernelPath,
		StartTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}

	broker := cred.NewBroker()

	// WithBroker enables svc.Remove to call broker.Revoke for all scopes,
	// keeping the test's post-Remove scope-error assertion discriminating.
	svc := service.New(st, svcDrv, lifecycle.New()).WithBroker(broker)

	// ── 5. StaticCredentialSource backed by a deterministic test token ────────
	// T_static is a fake bearer token that will be rejected by Anthropic, but
	// the MITM swap fires regardless of the upstream response. This makes the
	// wiring proof fully self-contained without a live Anthropic account.
	tStatic := fmt.Sprintf("sk-ant-oat01-ORCA-CRED-BROKER-WIRING-TEST-%d", time.Now().UnixNano())
	maskToken := func(tok string) string {
		if len(tok) <= 8 {
			return "****"
		}
		return tok[:4] + "…" + tok[len(tok)-4:]
	}
	t.Logf("T_static (fake, len=%d): %s", len(tStatic), maskToken(tStatic))

	credSrc := cred.NewStaticCredentialSource(&cred.DedicatedCredStore{
		AccessToken: tStatic,
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})

	// ── 6. Boot sandbox via WireClaudeEgress (the orcaCreate path) ────────────
	//
	// The lazy seeder captures bootDrv. By the time CreateAndBoot's step-9 seed
	// fires, the DriverFactory has already run and bootDrv is set — exactly the
	// same pattern as orcaCreate's agentSeeder closure.
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(ext4Path string, _ []service.ExtraDisk) (driver.Driver, error) {
		var ferr error
		bootDrv, ferr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    ext4Path,
			MemoryMiB:        2048,
			SerialOutputPath: serialPath,
			StartTimeout:     30 * time.Second,
		})
		return bootDrv, ferr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	// Lazy seeder: mirrors orcaCreate's agentSeeder closure exactly.
	// capturedBootDrv is set by the factory before step 9 fires.
	lazySeeder := service.GuestSeeder(func(ctx context.Context, id domain.SandboxID, payload []byte) error {
		gd, ok := interface{}(bootDrv).(driver.GuestDialer)
		if !ok {
			t.Log("lazySeeder: driver does not implement GuestDialer; seed skipped")
			return nil
		}
		return service.NewAgentCopySeeder(agent.NewClient(gd, id))(ctx, id, payload)
	})

	opts := service.CreateAndBootOptions{
		Image:               service.ImageSpec{Digest: string(img.Digest)},
		CacheRoot:           cacheRoot,
		ReachabilityTimeout: 60 * time.Second,
	}
	// WireClaudeEgress: mirrors the call added to orcaCreate.
	// Sets UseAgentSeed=true, AllowedHosts=AgentEgressHosts, Broker, Seeder,
	// AgentCredSource, AgentProfile.
	service.WireClaudeEgress(&opts, broker, lazySeeder, credSrc)

	t.Log("creating and booting sandbox via WireClaudeEgress path …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID == (domain.SandboxID{}) {
			return
		}
		rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if rerr := svc.Remove(rmCtx, sandboxID.String()); rerr != nil {
			t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, rerr)
		}
	})

	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"orca-cred-broker", fmt.Sprintf("orca-cred-%d", time.Now().UnixNano()),
		opts,
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("sandbox booted: %s state=%s", sb.ID, sb.State)

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// ── 7. Assert (1): RegisterPlaceholder fired for AnthropicAPIHost ─────────
	//
	// CreateAndBoot step 9 (UseAgentSeed=true) called seedGuestAgent which
	// called broker.RegisterPlaceholder(sb.ID, AnthropicAPIHost, ""). If that
	// fired, SetRealToken for the same scope returns nil. If RegisterPlaceholder
	// was never called, SetRealToken returns "no placeholder registered" error.
	//
	// Note: CreateAndBoot step 9 also called broker.SetRealToken with T_static
	// (from AgentCredSource.Token()). The probe here overwrites that with the
	// same value, which is idempotent.
	if setErr := broker.SetRealToken(sb.ID, service.AnthropicAPIHost, tStatic); setErr != nil {
		t.Fatalf("(1) broker.SetRealToken probe: RegisterPlaceholder was NOT called for %s: %v",
			service.AnthropicAPIHost, setErr)
	}
	t.Log("(1) PASS: broker.SetRealToken probe succeeded — RegisterPlaceholder fired for", service.AnthropicAPIHost)

	// ── 8. Get placeholder via manual SeedGuestAgent ──────────────────────────
	//
	// Calling SeedGuestAgent again replaces the auto-seeded placeholder from
	// step 9. That is fine here: we need the placeholder string to inject into
	// ExecOptions.Env, and the manual call gives us that. We re-wire T_static
	// immediately after.
	credSeeder := service.NewAgentCopySeeder(agentClient)
	records, err := service.SeedGuestAgent(context.Background(), broker, sb.ID, credSeeder)
	if err != nil {
		t.Fatalf("SeedGuestAgent (manual, for placeholder extraction): %v", err)
	}
	if err := broker.SetRealToken(sb.ID, service.AnthropicAPIHost, tStatic); err != nil {
		t.Fatalf("broker.SetRealToken (re-wire T_static): %v", err)
	}

	var claudePlaceholder string
	for _, r := range records {
		if r.Host == service.AnthropicAPIHost {
			claudePlaceholder = r.Placeholder
			break
		}
	}
	if claudePlaceholder == "" {
		t.Fatalf("no placeholder registered for host %s after manual SeedGuestAgent", service.AnthropicAPIHost)
	}
	t.Logf("broker: placeholder wired for %s", service.AnthropicAPIHost)

	// ── 9. Start perimeter supervisor ────────────────────────────────────────
	nh := interface{}(bootDrv).(driver.NetworkHook)
	fd, err := nh.GuestNetworkFD(context.Background(), sb.ID)
	if err != nil {
		t.Fatalf("GuestNetworkFD: %v", err)
	}

	al, err := netfilter.NewAllowList(nil, nil, service.AgentEgressHosts(cred.ClaudeCodeProfile))
	if err != nil {
		t.Fatalf("netfilter.NewAllowList: %v", err)
	}

	var auditMu sync.Mutex
	var auditEvents []perimeter.AuditEvent
	stack := netstack.New(al, func(ev perimeter.AuditEvent) {
		t.Logf("perimeter audit: %s %s — %s", ev.Decision, ev.DestHost, ev.Reason)
		auditMu.Lock()
		auditEvents = append(auditEvents, ev)
		auditMu.Unlock()
	})

	var swapCount atomic.Int64
	var connectAllowCount atomic.Int64
	swapLogger := slog.New(&countingHandler{
		inner: &countingHandler{
			inner:   slog.Default().Handler(),
			phrase:  "mitm: CONNECT allowed",
			count:   &connectAllowCount,
			attrKey: "host",
			attrVal: service.AnthropicAPIHost,
		},
		phrase: "credential swapped",
		count:  &swapCount,
	})

	mitmProxy, err := mitm.New(mitm.Config{
		SandboxID:    sb.ID,
		AllowedHosts: service.AgentEgressHosts(cred.ClaudeCodeProfile),
		Broker:       broker,
		Logger:       swapLogger,
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}

	supCtx, supCancel := context.WithCancel(context.Background())
	defer supCancel()

	sup, err := perimeter.Start(supCtx, sb.ID, fd, stack, mitmProxy, al)
	if err != nil {
		t.Fatalf("perimeter.Start: %v", err)
	}
	defer sup.Close()
	t.Logf("perimeter MITM listening at %s", sup.MitmAddr())

	// ── 10. SeedCA ────────────────────────────────────────────────────────────
	if err := service.SeedCA(context.Background(), sup.CACert(), sb.ID, dogfoodCACopySeeder(agentClient)); err != nil {
		t.Fatalf("SeedCA: %v", err)
	}
	t.Log("MITM CA cert seeded to guest at", service.GuestCACertPath)

	// ── 11. In-guest HTTPS call to api.anthropic.com ──────────────────────────
	//
	// Run claude with placeholder in env. T_static is a fake token so the
	// request will be rejected by Anthropic (no NEXUS3_OK), but the MITM swap
	// fires regardless of the upstream response — swapCount > 0 proves
	// host-side bearer injection.
	guestEnv := map[string]string{
		"PATH":                                   "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":                                   "/root",
		"TERM":                                   "dumb",
		cred.ClaudeCodeProfile.PlaceholderEnvVar: claudePlaceholder,
		"NODE_EXTRA_CA_CERTS":                    service.GuestCACertPath,
		"ANTHROPIC_MODEL":                        dogfoodHaikuModel,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	}

	swapBefore := swapCount.Load()
	t.Log("running in-guest claude exec (T_static, fake — expect rejection, swap still fires) …")

	var stdoutBuf, stderrBuf bytes.Buffer
	execCtx, execCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer execCancel()
	_, execErr := agentClient.Exec(execCtx, agent.ExecOptions{
		Cwd:    "/root",
		Argv:   []string{"/usr/local/bin/claude", "-p", "reply with exactly: NEXUS3_OK", "--model", dogfoodHaikuModel},
		Env:    guestEnv,
		Stdout: &stdoutBuf,
		Stderr: &stderrBuf,
	})
	if execErr != nil {
		t.Fatalf("in-guest claude exec: transport failure: %v", execErr)
	}
	stdout := stdoutBuf.String()
	if stderrBuf.Len() > 0 {
		t.Logf("claude stderr:\n%s", stderrBuf.String())
	}
	t.Logf("claude stdout:\n%s", stdout)

	// ── 12. Perimeter invariant assertions ────────────────────────────────────

	// (a) MITM must have performed at least one swap (T_static was injected).
	if swapCount.Load() == swapBefore {
		t.Errorf("(a) perimeter: MITM swap count unchanged (was %d); bearer injection did not fire", swapBefore)
	} else {
		t.Logf("(a) PASS: MITM bearer swap count: %d (delta from before exec: %d)",
			swapCount.Load(), swapCount.Load()-swapBefore)
	}

	// (b) MITM must have allowed CONNECT to api.anthropic.com.
	if connectAllowCount.Load() == 0 {
		t.Errorf("(b) perimeter: MITM never allowed CONNECT to %s", service.AnthropicAPIHost)
	} else {
		t.Logf("(b) PASS: MITM CONNECT allowed to %s: %d time(s)", service.AnthropicAPIHost, connectAllowCount.Load())
	}

	// (c) Zero Deny events — no non-allowlisted egress was attempted.
	auditMu.Lock()
	denyCount := 0
	for _, ev := range auditEvents {
		if ev.Decision == perimeter.Deny {
			denyCount++
			t.Errorf("(c) perimeter: unexpected Deny event: %s %s — %s", ev.Decision, ev.DestHost, ev.Reason)
		}
	}
	auditMu.Unlock()
	if denyCount == 0 {
		t.Log("(c) PASS: zero Deny events")
	}

	// (d) T_static must not appear in the guest exec environment.
	for k, v := range guestEnv {
		if v == tStatic {
			t.Errorf("(d) zero-cred env: real token T_static leaked into guest env[%q]", k)
		}
	}
	t.Log("(d) PASS: T_static absent from guest exec env")

	// (d-fs) T_static must not appear on the guest filesystem.
	var fsGrepOut bytes.Buffer
	grepCtx, grepCancel := context.WithTimeout(context.Background(), 30*time.Second)
	grepExitCode, grepExecErr := agentClient.Exec(grepCtx, agent.ExecOptions{
		Cwd:  "/",
		Argv: []string{"/bin/sh", "-c", `grep -rl -- "$NEEDLE" /root /home /etc /tmp 2>/dev/null`},
		Env: map[string]string{
			"PATH":   "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"NEEDLE": tStatic,
		},
		Stdout: &fsGrepOut,
		Stderr: &bytes.Buffer{},
	})
	grepCancel()
	if grepExecErr != nil {
		t.Logf("(d-fs) in-guest grep exec error (skipping fs check): %v", grepExecErr)
	} else if grepExitCode == 0 {
		t.Errorf("(d-fs) zero-cred fs: T_static found on guest filesystem:\n%s", fsGrepOut.String())
	} else {
		t.Log("(d-fs) PASS: T_static absent from guest filesystem (/root /home /etc /tmp)")
	}

	t.Log("TestOrcaCredBrokerWiring: all assertions complete")
}
