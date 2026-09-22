//go:build integration

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

const (
	motiveE2EMotiveID  = "motive-e2e"
	motiveE2ESentinel  = "MOTIVE_E2E_OK"
	motiveE2EGuestPath = "/root/result.txt"
)

func TestMotiveDogfood(t *testing.T) {
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	realToken := os.Getenv("ANTHROPIC_AUTH_TOKEN")
	if realToken == "" {
		t.Skip("set ANTHROPIC_AUTH_TOKEN to run the live motive e2e dogfood")
	}

	t.Setenv("ANTHROPIC_AUTH_TOKEN", realToken)

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

	socketDir, err := os.MkdirTemp("/tmp", "motive-dogfood-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}
	serialPath := filepath.Join(socketDir, "motive-dogfood-serial.log")
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
	svc := service.New(st, svcDrv, lifecycle.New())
	broker := cred.NewBroker()

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

	t.Log("creating and booting motive-tagged sandbox …")
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
		"motive-dogfood", fmt.Sprintf("motive-dogfood-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			Labels:              map[string]string{"motive": motiveE2EMotiveID},
			AllowedHosts:        service.AgentEgressHosts(cred.ClaudeCodeProfile),
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("sandbox booted: %s motive=%s state=%s", sb.ID, motiveE2EMotiveID, sb.State)

	agentClient := agent.NewClient(bootDrv, sb.ID)

	credSeeder := service.NewAgentCopySeeder(agentClient)
	records, err := service.SeedGuestAgent(context.Background(), broker, sb.ID, credSeeder)
	if err != nil {
		t.Fatalf("SeedGuestAgent: %v", err)
	}
	if err := broker.SetRealToken(sb.ID, service.AnthropicAPIHost, realToken); err != nil {
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
	t.Logf("broker: ANTHROPIC_AUTH_TOKEN placeholder wired for %s", service.AnthropicAPIHost)

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

	if err := service.SeedCA(context.Background(), sup.CACert(), sb.ID, dogfoodCACopySeeder(agentClient)); err != nil {
		t.Fatalf("SeedCA: %v", err)
	}
	t.Log("MITM CA cert seeded to guest at", service.GuestCACertPath)

	guestEnv := map[string]string{
		"PATH":                                    "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":                                    "/root",
		"TERM":                                    "dumb",
		"ANTHROPIC_AUTH_TOKEN":                    claudePlaceholder,
		"NODE_EXTRA_CA_CERTS":                     service.GuestCACertPath,
		"ANTHROPIC_MODEL":                         dogfoodHaikuModel,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	}

	t.Logf("running claude -p in-guest (model=%s) …", dogfoodHaikuModel)
	var stdout, stderr bytes.Buffer
	execCtx, execCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer execCancel()

	claudeCmd := fmt.Sprintf(
		"/usr/local/bin/claude -p 'reply with exactly: %s' --model %s | tee %s",
		motiveE2ESentinel, dogfoodHaikuModel, motiveE2EGuestPath,
	)
	exitCode, execErr := agentClient.Exec(execCtx, agent.ExecOptions{
		Cwd:    "/root",
		Argv:   []string{"/bin/sh", "-c", claudeCmd},
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
		t.Fatalf("claude exited %d\noutput: %s\nstderr: %s", exitCode, output, errOutput)
	}
	if !strings.Contains(output, motiveE2ESentinel) {
		t.Errorf("expected output to contain %s; got: %q", motiveE2ESentinel, output)
		return
	}
	t.Logf("motive dogfood PASSED — model=%s response=%q", dogfoodHaikuModel, output)

	// ── 10. Perimeter + motive invariant assertions ───────────────────────────
	// (a) Sandbox must be retrievable by motive ID via service.GetByMotive.
	{
		motiveCtx, motiveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer motiveCancel()
		motSandboxes, merr := svc.GetByMotive(motiveCtx, motiveE2EMotiveID)
		if merr != nil {
			t.Errorf("(a) motive invariant: GetByMotive(%q): %v", motiveE2EMotiveID, merr)
		} else {
			var found bool
			for _, ms := range motSandboxes {
				if ms.ID == sb.ID {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("(a) motive invariant: sandbox %s not found among GetByMotive(%q) results (got %d)",
					sb.ID, motiveE2EMotiveID, len(motSandboxes))
			} else {
				t.Logf("(a) PASS: sandbox %s found via GetByMotive(%q)", sb.ID, motiveE2EMotiveID)
			}
		}
	}

	// (b) Egress to api.anthropic.com must have been observed and allowed by the MITM proxy.
	// The netstack AuditEvent.DestHost carries the resolved IP:port (e.g. "160.79.104.10:443"),
	// not the hostname, so a hostname match against auditEvents is a false-negative. Instead
	// we count "mitm: CONNECT allowed" log records where host==service.AnthropicAPIHost.
	if connectAllowCount.Load() == 0 {
		t.Errorf("(b) perimeter invariant: MITM never observed CONNECT allowed for %s",
			service.AnthropicAPIHost)
	} else {
		t.Logf("(b) PASS: MITM CONNECT allowed to %s observed %d time(s)",
			service.AnthropicAPIHost, connectAllowCount.Load())
	}

	// (c) Host-side credential swap must have fired at least once.
	if swapCount.Load() == 0 {
		t.Errorf("(c) perimeter invariant: no host-side bearer-swap observed (swapCount=0)")
	} else {
		t.Logf("(c) PASS: bearer-swap fired %d time(s)", swapCount.Load())
	}

	// (d) In-guest response contains the expected sentinel (real API reached).
	if !strings.Contains(output, motiveE2ESentinel) {
		t.Errorf("(d) sentinel %q absent from response: %q", motiveE2ESentinel, output)
	} else {
		t.Logf("(d) PASS: sentinel %q present in response", motiveE2ESentinel)
	}

	// (e) Real auth token must be absent from every value in the guest env.
	for k, v := range guestEnv {
		if v == realToken {
			t.Errorf("(e) perimeter invariant: real token leaked into guest env[%q]", k)
		}
	}
	t.Log("(e) PASS: real token absent from all guest env values")

	t.Log("(f) SKIP: HarvestMotive retired (D-2 surface removal)")
}
