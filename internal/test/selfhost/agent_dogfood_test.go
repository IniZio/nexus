//go:build integration

// Package selfhost — Milestone-A end-to-end dogfood:
// herdr boots an in-guest claude agent, routes HTTPS through the nexus3
// zero-cred MITM perimeter (bearer-swap + SNI shim), and asserts the
// Haiku model replies with "NEXUS3_OK".
//
// # Prerequisites
//
//   - /dev/kvm accessible
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN or ~/.local/bin/cloud-hypervisor)
//   - mke2fs in PATH (e2fsprogs)
//   - docker (required by BuildAgentBaseImage)
//   - NEXUS3_CLAUDE_OAUTH_TOKEN set (see ~/.config/nexus3/agent.env)
//
// # Running
//
//	set -a; source ~/.config/nexus3/agent.env 2>/dev/null; set +a
//	TMPDIR=/tmp go test -tags integration -run TestAgentDogfood \
//	    ./internal/test/selfhost/ -v -timeout 30m
//
// # Design notes
//
// Gap 1 (SeedCA): service.SeedCA delivers the MITM CA cert as a PEM file to
// GuestCACertPath. NODE_EXTRA_CA_CERTS points Node.js (claude's runtime) at
// that file directly — no update-ca-certificates needed.
//
// Gap 2 (HTTPS_PROXY): not injected.  The perimeter supervisor's buildDialer
// implements a transparent SNI shim: port-443 TCP from the guest is intercepted
// on the host by a goroutine that peeks the TLS ClientHello SNI via sni.ParseSNI
// and opens an HTTP CONNECT tunnel to the MITM proxy address host-side.
// 127.0.0.1:<mitmPort> is the host's loopback — not reachable from the guest —
// so injecting HTTPS_PROXY would actively break the working transparent path.
// (perimeter/supervisor.go buildDialer, lines 58–95.)
//
// Bug fixed after dogfood: NewAgentCopySeeder previously tar-wrapped its payload
// before calling agent.Copy with IsDirectory=false, causing pushFile to write raw
// tar bytes to GuestCredEnvPath instead of KEY=VALUE text.  The fix sends the raw
// payload bytes directly (bytes.NewReader(payload), no archive step).  The S4
// live-egress probe (TestSupervisorS4LiveEgress) verifies end-to-end by running
// "set -a; . /run/nexus3/cred.env" inside the guest and confirming the sourced
// vars reach the claude process.  See internal/core/service/seed_copy_test.go for
// the fast hermetic regression guard.
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
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
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

// dogfoodHaikuModel is the exact Haiku model ID used for all live calls.
// Must be Haiku; the test fails if this model is rejected — no fallback.
const dogfoodHaikuModel = "claude-haiku-4-5-20251001"

// TestAgentDogfood is the Milestone-A acceptance test.
func TestAgentDogfood(t *testing.T) {
	// ── 1. Skip guards ────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	token := os.Getenv("NEXUS3_CLAUDE_OAUTH_TOKEN")
	if token == "" {
		t.Skip("set NEXUS3_CLAUDE_OAUTH_TOKEN (source ~/.config/nexus3/agent.env) to run the live dogfood")
	}

	// Guard: clear ANTHROPIC_AUTH_TOKEN so resolveAgentCredKind() returns
	// kindOAuth regardless of what is set in the ambient host environment.
	// Without this, a host with ANTHROPIC_AUTH_TOKEN set would flip the seeder
	// to auth-token mode, breaking the OAuth placeholder the test asserts on.
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	// ── 2. Kernel path ────────────────────────────────────────────────────────
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── 3. Build / get agent base image ──────────────────────────────────────
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

	// ── 4. Infrastructure ─────────────────────────────────────────────────────
	// Socket dir in /tmp: stays within the 107-byte Linux sun_path limit.
	socketDir, err := os.MkdirTemp("/tmp", "dogfood-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}
	serialPath := filepath.Join(socketDir, "dogfood-serial.log")
	t.Cleanup(func() {
		// Dump serial output on failure so we can see guest kernel messages.
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
	svc := service.New(st, svcDrv, lifecycle.New())
	broker := cred.NewBroker()

	// ── 5. Boot sandbox ──────────────────────────────────────────────────────
	// bootDrv owns the guest vsock/network state.  It must be the same instance
	// passed to GuestNetworkFD and agent.NewClient (both index into d.nets[id]).
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(ext4Path string, _ []service.ExtraDisk) (driver.Driver, error) {
		var ferr error
		bootDrv, ferr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    ext4Path,
			MemoryMiB:        2048, // Node.js + claude (1 GiB not enough)
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
		"dogfood", fmt.Sprintf("dogfood-%d", time.Now().UnixNano()),
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

	// ── 6. Wire egress credentials ────────────────────────────────────────────
	// SeedGuestAgent registers placeholders in broker and delivers the cred.env
	// file to the guest as raw KEY=VALUE bytes (tar-wrap bug fixed).  The S4
	// live-egress path sources /run/nexus3/cred.env via "set -a; . cred.env".
	// We also extract the placeholder value for injection via ExecOptions.Env.
	credSeeder := service.NewAgentCopySeeder(agentClient)
	records, err := service.SeedGuestAgent(context.Background(), broker, sb.ID, credSeeder)
	if err != nil {
		t.Fatalf("SeedGuestAgent: %v", err)
	}
	if err := broker.SetRealToken(sb.ID, service.AnthropicAPIHost, token); err != nil {
		t.Fatalf("broker.SetRealToken: %v", err)
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

	// ── 7. Start perimeter supervisor ─────────────────────────────────────────
	nh := interface{}(bootDrv).(driver.NetworkHook)
	fd, err := nh.GuestNetworkFD(context.Background(), sb.ID)
	if err != nil {
		t.Fatalf("GuestNetworkFD: %v", err)
	}

	al, err := netfilter.NewAllowList(nil, nil, service.AgentEgressHosts(cred.ClaudeCodeProfile))
	if err != nil {
		t.Fatalf("netfilter.NewAllowList: %v", err)
	}
	// auditEvents accumulates perimeter AuditEvents for post-run assertion (a):
	// we assert that an Allow decision for api.anthropic.com was observed.
	var auditMu sync.Mutex
	var auditEvents []perimeter.AuditEvent
	stack := netstack.New(al, func(ev perimeter.AuditEvent) {
		t.Logf("perimeter audit: %s %s — %s", ev.Decision, ev.DestHost, ev.Reason)
		auditMu.Lock()
		auditEvents = append(auditEvents, ev)
		auditMu.Unlock()
	})

	// swapCount counts "credential swapped" slog events from the MITM proxy for
	// post-run assertion (b): we assert the host-side bearer-swap fired ≥ once.
	//
	// connectAllowCount counts "mitm: CONNECT allowed" slog events where the
	// "host" attr equals service.AnthropicAPIHost. This is the hostname-bearing
	// signal used for assertion (a); the netstack AuditEvent.DestHost carries
	// the resolved IP:port and cannot be matched by hostname.
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

	// ── 8. SeedCA (S-EGRESS Gap 1) ────────────────────────────────────────────
	// Deliver the MITM CA cert as raw PEM to GuestCACertPath.
	// dogfoodCACopySeeder sends raw bytes without a tar wrapper (unlike
	// NewAgentCopySeeder) so that the file on disk is valid PEM.
	// NODE_EXTRA_CA_CERTS in ExecOptions.Env points Node.js at this file.
	if err := service.SeedCA(context.Background(), sup.CACert(), sb.ID, dogfoodCACopySeeder(agentClient)); err != nil {
		t.Fatalf("SeedCA: %v", err)
	}
	t.Log("MITM CA cert seeded to guest at", service.GuestCACertPath)

	// ── 9. Pre-flight: diagnose guest network and CA cert state ───────────────
	// This exec runs before any live API call and logs the guest's network
	// interface list, ip availability, resolv.conf, and CA cert first bytes.
	// Empty output or EOF here means the exec path itself is broken.
	{
		const preflightScript = `
echo "=== PREFLIGHT ==="
echo "--- /sys/class/net:"
ls /sys/class/net 2>&1
echo "--- ip:"
command -v ip 2>&1 || echo NO_IP
echo "--- resolv.conf:"
cat /etc/resolv.conf 2>&1 || echo NO_RESOLV
echo "--- ca-cert:"
ls -l ` + service.GuestCACertPath + ` 2>&1
head -c 40 ` + service.GuestCACertPath + ` 2>&1
echo
echo "=== PREFLIGHT_DONE ==="
`
		var pfOut, pfErrBuf bytes.Buffer
		pfCtx, pfCancel := context.WithTimeout(context.Background(), 30*time.Second)
		pfCode, pfExecErr := agentClient.Exec(pfCtx, agent.ExecOptions{
			Argv:   []string{"/bin/sh", "-c", preflightScript},
			Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			Stdout: &pfOut,
			Stderr: &pfErrBuf,
		})
		pfCancel()
		t.Logf("preflight exit=%d err=%v\nstdout:\n%s\nstderr:\n%s",
			pfCode, pfExecErr, pfOut.String(), pfErrBuf.String())
	}

	// ── 10. Run claude in-guest (Haiku only) ─────────────────────────────────
	// Model: ANTHROPIC_MODEL env var + --model flag (belt and suspenders).
	// If the model ID is rejected, the test fails — do not fall back.
	// Note: --dangerously-skip-permissions is rejected when running as root.
	//
	// HTTPS routing: transparent SNI shim (buildDialer in perimeter/supervisor.go)
	// routes port-443 TCP from the guest through the MITM proxy on the host.
	// The MITM swaps Authorization: Bearer <placeholder> → <real_token>.
	// Node.js trusts the MITM CA via NODE_EXTRA_CA_CERTS.
	t.Logf("running claude -p in-guest (model=%s) …", dogfoodHaikuModel)

	var stdout, stderr bytes.Buffer
	execCtx, execCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer execCancel()

	// guestEnv is pulled out so post-run assertions (c) and (d) can inspect it.
	guestEnv := map[string]string{
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME": "/root",
		"TERM": "dumb",
		// Bearer-swap: MITM proxy swaps this placeholder for the real token.
		"CLAUDE_CODE_OAUTH_TOKEN": claudePlaceholder,
		// TLS: Node.js (claude's runtime) trusts the per-sandbox MITM CA.
		"NODE_EXTRA_CA_CERTS": service.GuestCACertPath,
		// Belt-and-suspenders Haiku pin (also --model flag above).
		"ANTHROPIC_MODEL": dogfoodHaikuModel,
		// Suppress telemetry, auto-update, and non-allowlisted egress.
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	}
	exitCode, execErr := agentClient.Exec(execCtx, agent.ExecOptions{
		Cwd: "/root",
		Argv: []string{
			"/usr/local/bin/claude",
			"-p", "reply with exactly: NEXUS3_OK",
			"--model", dogfoodHaikuModel,
		},
		Env:    guestEnv,
		Stdout: &stdout,
		Stderr: &stderr,
	})

	output := strings.TrimSpace(stdout.String())
	errOutput := strings.TrimSpace(stderr.String())
	t.Logf("claude stdout:\n%s", output)
	if errOutput != "" {
		t.Logf("claude stderr:\n%s", errOutput)
	}

	if execErr != nil {
		t.Fatalf("agentClient.Exec (claude -p): %v", execErr)
	}
	if exitCode != 0 {
		t.Fatalf("claude exited %d\noutput: %s", exitCode, output)
	}
	if !strings.Contains(output, "NEXUS3_OK") {
		t.Errorf("expected output to contain NEXUS3_OK; got: %q", output)
		return
	}
	t.Logf("dogfood PASSED — model=%s response=%q", dogfoodHaikuModel, output)

	// ── 11. Perimeter invariant assertions ───────────────────────────────────
	// These four checks harden the security properties of the completed run.
	// None weakens the existing pass condition (NEXUS3_OK) checked above.

	// (a) Egress to api.anthropic.com must have been observed and allowed by the MITM proxy.
	// The netstack AuditEvent.DestHost carries the resolved IP:port (e.g. "160.79.104.10:443"),
	// not the hostname, so a hostname match against auditEvents is a false-negative. Instead
	// we count "mitm: CONNECT allowed" log records where host==service.AnthropicAPIHost.
	if connectAllowCount.Load() == 0 {
		t.Errorf("(a) perimeter invariant: MITM never observed CONNECT allowed for %s",
			service.AnthropicAPIHost)
	} else {
		t.Logf("(a) PASS: MITM CONNECT allowed to %s observed %d time(s)",
			service.AnthropicAPIHost, connectAllowCount.Load())
	}

	// (b) Host-side credential swap must have fired at least once.
	if swapCount.Load() == 0 {
		t.Errorf("(b) perimeter invariant: no host-side bearer-swap observed (swapCount=0)")
	}

	// (c) Model must be pinned to the exact Haiku version — both the constant
	// and the value delivered to the guest environment.
	const wantHaikuModel = "claude-haiku-4-5-20251001"
	if dogfoodHaikuModel != wantHaikuModel {
		t.Errorf("(c) perimeter invariant: dogfoodHaikuModel constant drifted: got %q, want %q",
			dogfoodHaikuModel, wantHaikuModel)
	}
	if guestEnv["ANTHROPIC_MODEL"] != wantHaikuModel {
		t.Errorf("(c) perimeter invariant: ANTHROPIC_MODEL in guest env = %q, want %q",
			guestEnv["ANTHROPIC_MODEL"], wantHaikuModel)
	}

	// (d) Real Anthropic token must be absent from every value in the guest env.
	for k, v := range guestEnv {
		if v == token {
			t.Errorf("(d) perimeter invariant: real token leaked into guest env[%q]", k)
		}
	}
}

// dogfoodCACopySeeder returns a GuestSeeder that writes payload bytes directly
// to GuestCACertPath via agent.Copy with IsDirectory=false.  pushFile on the
// guest side writes the raw bytes verbatim — the correct behaviour for a PEM
// certificate file that Node.js reads via NODE_EXTRA_CA_CERTS.
//
// NOTE: service.NewAgentCACopySeeder now does the same thing (raw bytes, no tar
// wrapping) after the tar-wrap bug was fixed.  This local helper remains because
// it was written before the fix landed; it can be replaced with
// service.NewAgentCACopySeeder in a follow-up cleanup.
func dogfoodCACopySeeder(c *agent.Client) service.GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		return c.Copy(ctx, agent.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath: service.GuestCACertPath,
			Src:       bytes.NewReader(payload),
		})
	}
}

// countingHandler is a slog.Handler that increments count each time a log
// record whose Message contains phrase is handled. All records are forwarded
// to inner unchanged. The count pointer is shared across WithAttrs/WithGroup
// copies so all derived handlers increment the same counter.
//
// When both attrKey and attrVal are non-empty, the record must also carry a
// slog attr with the given key and a value that matches attrVal
// (case-insensitive) for count to increment. This lets a single handler match
// on both message text and a structured field (e.g. "host" == "api.anthropic.com").
type countingHandler struct {
	inner   slog.Handler
	phrase  string
	count   *atomic.Int64
	attrKey string // optional: slog attr key that must also match
	attrVal string // optional: expected attr value (case-insensitive)
}

func (h *countingHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.inner.Enabled(ctx, lvl)
}

func (h *countingHandler) Handle(ctx context.Context, r slog.Record) error {
	if strings.Contains(r.Message, h.phrase) {
		match := h.attrKey == ""
		if !match {
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == h.attrKey && strings.EqualFold(a.Value.String(), h.attrVal) {
					match = true
					return false // stop iteration
				}
				return true
			})
		}
		if match {
			h.count.Add(1)
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *countingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &countingHandler{inner: h.inner.WithAttrs(attrs), phrase: h.phrase, count: h.count, attrKey: h.attrKey, attrVal: h.attrVal}
}

func (h *countingHandler) WithGroup(name string) slog.Handler {
	return &countingHandler{inner: h.inner.WithGroup(name), phrase: h.phrase, count: h.count, attrKey: h.attrKey, attrVal: h.attrVal}
}
