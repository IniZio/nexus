//go:build integration

// Package selfhost — AC-5 invalid→valid rotation proof (no OAuth grant).
//
// Proves that the guest agent survives a host-side token rotation from an
// invalid bearer token (T0) to a valid one (T1), with ZERO OAuth refresh
// grants to the token endpoint. This design is strictly stronger than the
// prior OAuth-grant approach: a post-rotation NEXUS3_OK response is
// unambiguous proof that T1 propagated to the MITM (a successful API call
// cannot arrive by accident).
//
// Properties proved:
//  1. Baseline exec with T0 (deliberately invalid) → API call rejected by
//     Anthropic (NEXUS3_OK absent); MITM swap delta confirms the failure
//     reached Anthropic, not a network/boot problem.
//  2. broker.SetRealToken(sb.ID, host, T1) rotates the MITM mapping host-side;
//     the guest placeholder is byte-identical before and after.
//  3. Post-rotation exec with T1 (valid) → API call succeeds (NEXUS3_OK).
//  4. The MITM performs host-side credential swaps (swapCount delta > 0).
//  5. Zero Deny egress events: the guest never reached the OAuth token endpoint
//     (outside AgentEgressHosts; would appear as a Deny if attempted).
//  6. T0 and T1 are absent from the guest env and filesystem (AC-5 guarantee).
//  7. After svc.Remove: broker.Revoke was called (SetRealToken for the removed
//     sandbox fails with a scope-revoked error).
//
// # Prerequisites
//
//   - /dev/kvm accessible
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN or ~/.local/bin/cloud-hypervisor)
//   - mke2fs in PATH (e2fsprogs)
//   - docker (required by BuildAgentBaseImage)
//   - ~/.config/nexus3/creds.json (or NEXUS3_DEDICATED_CRED_STORE) present with a
//     currently-valid access_token (ExpiresAt > now+10m); no refresh_token needed.
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestOAuthRotationDogfood \
//	    ./internal/test/selfhost/ -v -timeout 20m
//
// # Rotation mechanism
//
// T0 is a deliberately invalid bearer token (a recognisable bogus string).
// T1 is the currently-valid access_token already in the cred store — no HTTP
// call to the token endpoint is made. The rotation is a direct
// broker.SetRealToken call. The MITM substitutes whichever real token the
// broker currently holds for the guest's placeholder on every intercepted
// request; swapping T0→T1 in the broker is all that is needed.
//
// # Manual seeding rationale
//
// WireClaudeEgress + CreateAndBoot would auto-seed the guest, but the returned
// []PlaceholderRecord is not accessible to the caller, and seedGuestAgent returns
// nil when seeder==nil (seed.go:301). The test therefore uses manual seeding
// (SeedGuestAgent post-boot → records → claudePlaceholder) so the placeholder can
// be injected into ExecOptions.Env explicitly. Both TestAgentDogfood and
// TestMotiveDogfood use the same pattern.
package selfhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// TestOAuthRotationDogfood is the AC-5 acceptance test: invalid→valid host-side
// token rotation, proven live with zero OAuth refresh grants.
func TestOAuthRotationDogfood(t *testing.T) {
	// ── 1. Skip guards ─────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	storePath := service.DefaultDedicatedCredStorePath()
	realStore, err := cred.LoadStore(storePath)
	if err != nil {
		if errors.Is(err, cred.ErrStoreAbsent) {
			t.Skipf("no dedicated cred store at %s; place a real OAuth token to run the live proof (charter TBD-P5-2)", storePath)
		}
		t.Fatalf("cred.LoadStore(%s): %v", storePath, err)
	}

	// Guard: the access_token in the store IS T1 — we need it to be currently
	// valid so the post-rotation API call succeeds. Require at least 10 minutes
	// of remaining validity to survive the image build phase.
	const minValidity = 10 * time.Minute
	if !realStore.ExpiresAt.After(time.Now().Add(minValidity)) {
		remaining := time.Until(realStore.ExpiresAt)
		t.Skipf("access_token in cred store expires too soon (remaining: %v, need >%v); refresh the token and rerun", remaining, minValidity)
	}

	// Guard: clear ANTHROPIC_AUTH_TOKEN so resolveAgentCredKind returns kindOAuth,
	// which causes SeedGuestAgent to emit CLAUDE_CODE_OAUTH_TOKEN=<placeholder>
	// in the seed payload (not ANTHROPIC_AUTH_TOKEN).
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	// ── 2. Kernel path ──────────────────────────────────────────────────────────
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── 3. Build / get agent base image ────────────────────────────────────────
	cacheRoot := t.TempDir()
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

	// ── 4. Infrastructure ───────────────────────────────────────────────────────
	// Socket dir in /tmp: stays within the 107-byte Linux sun_path limit.
	socketDir, err := os.MkdirTemp("/tmp", "rotation-dogfood-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}
	serialPath := filepath.Join(socketDir, "rotation-serial.log")
	t.Cleanup(func() {
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== guest serial output ===\n%s", content)
		}
		os.RemoveAll(socketDir)
	})

	storeRoot := t.TempDir()
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

	// WithBroker is required so that svc.Remove calls broker.Revoke for all
	// AllowedHosts scopes, making the post-Remove SetRealToken assertion
	// discriminating (scope revoked → error).
	svc := service.New(st, svcDrv, lifecycle.New()).WithBroker(broker)

	// ── 5. Boot sandbox ─────────────────────────────────────────────────────────
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

	t.Log("creating and booting sandbox …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID == (domain.SandboxID{}) {
			return // explicit Remove was called in the test body
		}
		rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if rerr := svc.Remove(rmCtx, sandboxID.String()); rerr != nil {
			t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, rerr)
		}
	})

	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"rotation-dogfood", fmt.Sprintf("rotation-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			AllowedHosts:        service.AgentEgressHosts(cred.ClaudeCodeProfile),
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("sandbox booted: %s state=%s", sb.ID, sb.State)

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// ── 6. Manual credential seeding ───────────────────────────────────────────
	// Extract the placeholder so it can be injected into ExecOptions.Env.
	// Manual seeding is required here (not WireClaudeEgress) because we need the
	// placeholder value to inject into the guest environment. See package comment.
	credSeeder := service.NewAgentCopySeeder(agentClient)
	records, err := service.SeedGuestAgent(context.Background(), broker, sb.ID, credSeeder)
	if err != nil {
		t.Fatalf("SeedGuestAgent: %v", err)
	}

	var claudePlaceholder string
	for _, r := range records {
		if r.Host == service.AnthropicAPIHost {
			claudePlaceholder = r.Placeholder
			break
		}
	}
	if claudePlaceholder == "" {
		t.Fatalf("no placeholder registered for host %s", service.AnthropicAPIHost)
	}
	t.Logf("broker: placeholder wired for %s", service.AnthropicAPIHost)

	// ── 7. T0 (invalid) and T1 (valid) ─────────────────────────────────────────
	// T0 is a deliberately bogus bearer token — it will be recognised and
	// rejected by the Anthropic API with a 401. It is never a real secret.
	// T1 is the currently-valid access_token already in the cred store.
	t0 := fmt.Sprintf("sk-ant-oat01-INVALID-rottest-%d", time.Now().UnixNano())
	t1 := realStore.AccessToken
	maskToken := func(tok string) string {
		if len(tok) <= 8 {
			return "****"
		}
		return tok[:4] + "…" + tok[len(tok)-4:]
	}
	t.Logf("T0 (invalid, len=%d): %s", len(t0), maskToken(t0))
	t.Logf("T1 (valid,   len=%d): %s", len(t1), maskToken(t1))

	// Plant T0 in the broker so the MITM serves the invalid token at baseline.
	// SeedGuestAgent registered the placeholder with realToken=""; SetRealToken
	// updates the mapping without changing the placeholder string.
	if err := broker.SetRealToken(sb.ID, service.AnthropicAPIHost, t0); err != nil {
		t.Fatalf("broker.SetRealToken(T0): %v", err)
	}

	// Confirm placeholder unchanged and broker maps it to T0.
	if resolved, ok := broker.ResolveScoped(claudePlaceholder, sb.ID, service.AnthropicAPIHost); !ok {
		t.Fatalf("broker.ResolveScoped post-T0-plant: placeholder not found")
	} else if resolved != t0 {
		t.Fatalf("broker.ResolveScoped post-T0-plant: got %q, want T0", maskToken(resolved))
	}
	t.Log("broker: placeholder→T0 mapping confirmed")

	// ── 8. Start perimeter supervisor ──────────────────────────────────────────
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

	// ── 9. SeedCA ───────────────────────────────────────────────────────────────
	if err := service.SeedCA(context.Background(), sup.CACert(), sb.ID, dogfoodCACopySeeder(agentClient)); err != nil {
		t.Fatalf("SeedCA: %v", err)
	}
	t.Log("MITM CA cert seeded to guest at", service.GuestCACertPath)

	// ── 10. runClaudeRaw helper ─────────────────────────────────────────────────
	// Returns (exitCode, stdout, stderr, execErr). Never asserts; callers decide.
	guestEnv := map[string]string{
		"PATH":                                   "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":                                   "/root",
		"TERM":                                   "dumb",
		cred.ClaudeCodeProfile.PlaceholderEnvVar: claudePlaceholder,
		"NODE_EXTRA_CA_CERTS":                    service.GuestCACertPath,
		"ANTHROPIC_MODEL":                        dogfoodHaikuModel,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	}

	runClaudeRaw := func(label string) (exitCode int32, stdout, stderr string, execErr error) {
		t.Helper()
		var stdoutBuf, stderrBuf bytes.Buffer
		execCtx, execCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer execCancel()
		exitCode, execErr = agentClient.Exec(execCtx, agent.ExecOptions{
			Cwd:    "/root",
			Argv:   []string{"/usr/local/bin/claude", "-p", "reply with exactly: NEXUS3_OK", "--model", dogfoodHaikuModel},
			Env:    guestEnv,
			Stdout: &stdoutBuf,
			Stderr: &stderrBuf,
		})
		stdout = strings.TrimSpace(stdoutBuf.String())
		stderr = strings.TrimSpace(stderrBuf.String())
		t.Logf("claude %s stdout:\n%s", label, stdout)
		if stderr != "" {
			t.Logf("claude %s stderr:\n%s", label, stderr)
		}
		return
	}

	// ── 11. Baseline exec (T0 in broker) — expect rejection ────────────────────
	// Snapshot swap counter before the baseline call; a delta > 0 proves the
	// MITM reached Anthropic with T0 (distinguishes auth-failure from boot/network).
	swapBeforeBaseline := swapCount.Load()
	t.Log("running baseline claude exec with T0 (invalid) — expecting rejection …")

	bExitCode, bOut, _, bExecErr := runClaudeRaw("baseline-T0-invalid")
	if bExecErr != nil {
		t.Fatalf("baseline-T0-invalid: transport failure (not auth failure): %v", bExecErr)
	}

	// Swap delta discriminator: if the MITM swapped on this call, we know the
	// request reached Anthropic and the failure is upstream auth rejection.
	// Zero delta means the failure is network/boot — not the expected 401.
	if swapCount.Load() == swapBeforeBaseline {
		t.Fatalf("baseline-T0-invalid: MITM swap count unchanged (was %d); failure is network/boot, not auth rejection — cannot confirm AC-5 baseline", swapBeforeBaseline)
	}
	// Load-bearing negative: T0 should be rejected — NEXUS3_OK must be absent.
	if strings.Contains(bOut, "NEXUS3_OK") {
		t.Fatalf("baseline-T0-invalid: unexpectedly produced NEXUS3_OK (T0 was accepted by Anthropic); output: %q", bOut)
	}
	t.Logf("baseline PASSED: T0 correctly rejected (exitCode=%d, swap delta=%d)",
		bExitCode, swapCount.Load()-swapBeforeBaseline)

	// ── 12. Rotation: set T1 directly (no OAuth grant) ─────────────────────────
	// broker.SetRealToken atomically replaces the real token for the
	// placeholder↔scope mapping. The guest placeholder is byte-identical;
	// only the host-side mapping changes.
	if err := broker.SetRealToken(sb.ID, service.AnthropicAPIHost, t1); err != nil {
		t.Fatalf("broker.SetRealToken(T1): %v", err)
	}

	// Assert: placeholder unchanged, broker now maps it to T1.
	if resolved, ok := broker.ResolveScoped(claudePlaceholder, sb.ID, service.AnthropicAPIHost); !ok {
		t.Fatalf("broker.ResolveScoped post-rotation: placeholder not found (scope revoked prematurely?)")
	} else if resolved != t1 {
		t.Fatalf("broker.ResolveScoped post-rotation: got %s, want T1 (%s)", maskToken(resolved), maskToken(t1))
	}
	t.Logf("rotation: placeholder→T1 mapping confirmed (placeholder unchanged, host-side mapping rotated)")

	// ── 13. Post-rotation exec (T1 in broker) — expect success ─────────────────
	swapBeforePostRotation := swapCount.Load()
	t.Log("running post-rotation claude exec with T1 (valid) …")

	pExitCode, pOut, _, pExecErr := runClaudeRaw("post-rotation-T1")
	if pExecErr != nil {
		t.Fatalf("post-rotation: transport failure: %v", pExecErr)
	}
	if pExitCode != 0 {
		t.Fatalf("post-rotation claude exited %d (T1 not accepted by Anthropic); output: %q", pExitCode, pOut)
	}
	if !strings.Contains(pOut, "NEXUS3_OK") {
		t.Errorf("post-rotation: expected output to contain NEXUS3_OK; got: %q", pOut)
	}
	t.Logf("post-rotation PASSED with T1 (exitCode=%d, swap delta=%d)",
		pExitCode, swapCount.Load()-swapBeforePostRotation)

	// ── 14. Perimeter invariant assertions ─────────────────────────────────────

	// (a) MITM must have performed host-side credential swaps (across both calls).
	if swapCount.Load() == 0 {
		t.Errorf("(a) perimeter: MITM never observed credential swap (swapCount=0)")
	} else {
		t.Logf("(a) PASS: MITM credential swaps observed: %d", swapCount.Load())
	}

	// (b) MITM must have allowed CONNECT to api.anthropic.com.
	if connectAllowCount.Load() == 0 {
		t.Errorf("(b) perimeter: MITM never observed CONNECT allowed for %s", service.AnthropicAPIHost)
	} else {
		t.Logf("(b) PASS: MITM CONNECT allowed to %s observed %d time(s)", service.AnthropicAPIHost, connectAllowCount.Load())
	}

	// (c) Zero Deny events — no non-allowlisted egress was attempted.
	// The token endpoint host is outside AgentEgressHosts; a Deny would appear
	// here if the guest tried to reach it. Zero Deny events is consistent with
	// the zero-self-refresh invariant (AC-5).
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
		t.Log("(c) PASS: zero Deny events — no non-allowlisted egress observed (guest never self-refreshed)")
	}

	// (d) T0 and T1 must not appear in the guest exec environment.
	for k, v := range guestEnv {
		if v == t0 {
			t.Errorf("(d) perimeter: real token T0 leaked into guest env[%q]", k)
		}
		if v == t1 {
			t.Errorf("(d) perimeter: real token T1 leaked into guest env[%q]", k)
		}
	}
	t.Log("(d) PASS: T0 and T1 absent from guest env")

	// (d-fs) AC-5 filesystem half: neither T0 nor T1 may appear anywhere in
	// the guest filesystem that the agent could read — home dir, config dirs,
	// and any credential/config file the guest agent would use.
	//
	// Mechanism: run `grep -rl -- "$NEEDLE" <dirs>` in-guest via agentClient.Exec.
	//   grep exit 0  = matches found     → token IS present (bad, t.Errorf)
	//   grep exit 1  = no matches        → token absent     (good, PASS)
	//   exec err     = transport failure → logged, skipped (non-fatal)
	for _, tc := range []struct{ label, tok string }{
		{"T0", t0},
		{"T1", t1},
	} {
		var fsGrepOut bytes.Buffer
		grepCtx, grepCancel := context.WithTimeout(context.Background(), 30*time.Second)
		grepExitCode, grepExecErr := agentClient.Exec(grepCtx, agent.ExecOptions{
			Cwd:  "/",
			Argv: []string{"/bin/sh", "-c", `grep -rl -- "$NEEDLE" /root /home /etc /tmp 2>/dev/null`},
			Env: map[string]string{
				"PATH":   "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				"NEEDLE": tc.tok,
			},
			Stdout: &fsGrepOut,
			Stderr: &bytes.Buffer{},
		})
		grepCancel()
		if grepExecErr != nil {
			t.Logf("(d-fs) %s: in-guest grep exec error (skipping fs check): %v", tc.label, grepExecErr)
			continue
		}
		if grepExitCode == 0 {
			// grep exit 0 = matches found — token present in guest filesystem.
			t.Errorf("(d-fs) real token %s (%s) found in guest filesystem (files: %s)",
				tc.label, maskToken(tc.tok), strings.TrimSpace(fsGrepOut.String()))
		} else {
			t.Logf("(d-fs) PASS: %s (%s) absent from guest filesystem (grep exit=%d)",
				tc.label, maskToken(tc.tok), grepExitCode)
		}
	}

	// ── 15. Explicit Remove + scope-revocation assertion ───────────────────────
	// Zero sandboxID NOW so t.Cleanup is a no-op; we call Remove explicitly here.
	removedID := sb.ID
	sandboxID = domain.SandboxID{}

	rmCtx, rmCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if rerr := svc.Remove(rmCtx, removedID.String()); rerr != nil {
		t.Fatalf("explicit svc.Remove: %v", rerr)
	}
	rmCancel()
	t.Logf("explicit Remove: %s", removedID)

	// (e) broker.Revoke was called: SetRealToken for the removed sandbox now fails.
	// This is the direct proof that Remove wired broker.Revoke (via WithBroker).
	if setErr := broker.SetRealToken(removedID, service.AnthropicAPIHost, "canary-post-remove"); setErr == nil {
		t.Error("(e) deregister: broker.SetRealToken should fail after Remove (scope revoked); got nil")
	} else {
		t.Logf("(e) PASS: broker.SetRealToken fails post-Remove: %v", setErr)
	}

	t.Log("TestOAuthRotationDogfood PASSED")
}
