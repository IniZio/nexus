//go:build integration

// Package selfhost — Milestone-A dogfood: in-guest claude via nexus zero-cred MITM perimeter.
// Gap 1 (SeedCA): MITM CA cert at GuestCACertPath, NODE_EXTRA_CA_CERTS → Node.js direct.
// Gap 2 (HTTPS_PROXY): not injected; transparent SNI shim via buildDialer intercepts port-443.
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

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/builder"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/perimeter"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus/internal/core/perimeter/netstack"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

const dogfoodHaikuModel = "claude-haiku-4-5-20251001" // Exact model; test fails if rejected

func TestAgentDogfood(t *testing.T) {
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	token := os.Getenv("NEXUS_CLAUDE_OAUTH_TOKEN")
	if token == "" {
		t.Skip("set NEXUS_CLAUDE_OAUTH_TOKEN (source ~/.config/nexus/agent.env) to run the live dogfood")
	}

	// Clear ANTHROPIC_AUTH_TOKEN so resolveAgentCredKind() returns kindOAuth
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

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
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== guest serial output ===\n%s", content) // dump on failure for guest kernel messages
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

	// bootDrv must be same instance for GuestNetworkFD and agent.NewClient (both index d.nets[id])
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

	// SeedGuestAgent: registers placeholders, delivers cred.env as raw KEY=VALUE bytes
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

	nh := interface{}(bootDrv).(driver.NetworkHook)
	fd, err := nh.GuestNetworkFD(context.Background(), sb.ID)
	if err != nil {
		t.Fatalf("GuestNetworkFD: %v", err)
	}

	al, err := netfilter.NewAllowList(nil, nil, service.AgentEgressHosts(cred.ClaudeCodeProfile))
	if err != nil {
		t.Fatalf("netfilter.NewAllowList: %v", err)
	}
	// auditEvents: collected for assertion (a) — Allow decision to api.anthropic.com
	var auditMu sync.Mutex
	var auditEvents []perimeter.AuditEvent
	stack := netstack.New(al, func(ev perimeter.AuditEvent) {
		t.Logf("perimeter audit: %s %s — %s", ev.Decision, ev.DestHost, ev.Reason)
		auditMu.Lock()
		auditEvents = append(auditEvents, ev)
		auditMu.Unlock()
	})

	// swapCount: assertion (b) — bearer-swap fired ≥ once
	// connectAllowCount: assertion (a) — CONNECT allowed for api.anthropic.com (hostname-bearing signal)
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

	// Gap 1 (SeedCA): MITM CA cert as raw PEM to GuestCACertPath, NODE_EXTRA_CA_CERTS → Node.js
	if err := service.SeedCA(context.Background(), sup.CACert(), sb.ID, dogfoodCACopySeeder(agentClient)); err != nil {
		t.Fatalf("SeedCA: %v", err)
	}
	t.Log("MITM CA cert seeded to guest at", service.GuestCACertPath)

	// Pre-flight: diagnose guest network and CA cert state
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

	// Run claude in-guest: test fails if model rejected (HTTPS via transparent SNI shim + MITM)
	t.Logf("running claude -p in-guest (model=%s) …", dogfoodHaikuModel)

	var stdout, stderr bytes.Buffer
	execCtx, execCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer execCancel()

	// guestEnv is inspected by post-run assertions (c) and (d)
	guestEnv := map[string]string{
		"PATH":                    "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":                    "/root",
		"TERM":                    "dumb",
		"CLAUDE_CODE_OAUTH_TOKEN": claudePlaceholder, // MITM proxy swaps for real token
		"NODE_EXTRA_CA_CERTS":     service.GuestCACertPath,
		"ANTHROPIC_MODEL":         dogfoodHaikuModel,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	}
	exitCode, execErr := agentClient.Exec(execCtx, agent.ExecOptions{
		Cwd: "/root",
		Argv: []string{
			"/usr/local/bin/claude",
			"-p", "reply with exactly: NEXUS_OK",
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
	if !strings.Contains(output, "NEXUS_OK") {
		t.Errorf("expected output to contain NEXUS_OK; got: %q", output)
		return
	}
	t.Logf("dogfood PASSED — model=%s response=%q", dogfoodHaikuModel, output)

	// (a) MITM must observe CONNECT allowed to api.anthropic.com (hostname-bearing signal)
	if connectAllowCount.Load() == 0 {
		t.Errorf("(a) perimeter invariant: MITM never observed CONNECT allowed for %s",
			service.AnthropicAPIHost)
	} else {
		t.Logf("(a) PASS: MITM CONNECT allowed to %s observed %d time(s)",
			service.AnthropicAPIHost, connectAllowCount.Load())
	}

	// (b) Host-side credential swap must have fired ≥ once
	if swapCount.Load() == 0 {
		t.Errorf("(b) perimeter invariant: no host-side bearer-swap observed (swapCount=0)")
	}

	// (c) Model pinned to exact Haiku version in both constant and guest env
	const wantHaikuModel = "claude-haiku-4-5-20251001"
	if dogfoodHaikuModel != wantHaikuModel {
		t.Errorf("(c) perimeter invariant: dogfoodHaikuModel constant drifted: got %q, want %q",
			dogfoodHaikuModel, wantHaikuModel)
	}
	if guestEnv["ANTHROPIC_MODEL"] != wantHaikuModel {
		t.Errorf("(c) perimeter invariant: ANTHROPIC_MODEL in guest env = %q, want %q",
			guestEnv["ANTHROPIC_MODEL"], wantHaikuModel)
	}

	// (d) Real token must be absent from every value in guest env
	for k, v := range guestEnv {
		if v == token {
			t.Errorf("(d) perimeter invariant: real token leaked into guest env[%q]", k)
		}
	}
}

// Writes payload bytes directly to GuestCACertPath via agent.Copy (IsDirectory=false).
func dogfoodCACopySeeder(c *agent.Client) service.GuestSeeder {
	return func(ctx context.Context, _ domain.SandboxID, payload []byte) error {
		return c.Copy(ctx, agent.CopyOptions{
			Direction: agentpb.CopyDirection_COPY_DIRECTION_PUSH,
			GuestPath: service.GuestCACertPath,
			Src:       bytes.NewReader(payload),
		})
	}
}

// Increments count when log Message contains phrase; optional attrKey/attrVal match.
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
