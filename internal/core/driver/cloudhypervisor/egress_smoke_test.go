//go:build integration

package cloudhypervisor

// egress_smoke_test.go — Hermetic boot + network-interface smoke test for the
// committed Linux 6.12.76 guest kernel (images/kernel/vmlinux-x86_64).
//
// # Regression this test catches
//
// The committed kernel enables CONFIG_DUMMY=y, which creates a dummy0 virtual
// interface at boot. Before the fix in cmd/nexus3-agent/network.go
// (firstNonLoIfaceAt), the agent selected the first non-loopback interface
// alphabetically. Because "dummy" < "eth", dummy0 was chosen ahead of eth0
// (the virtio-net device), assigning 192.168.127.2/24 to a black-hole device
// and silently killing all guest DNS and egress.
//
// The fix: firstNonLoIfaceAt now prefers interfaces that have a
// /sys/class/net/<name>/device symlink (hardware-backed, e.g. eth0). Virtual
// interfaces (dummy0, veth*, bridge) lack this symlink and are skipped.
//
// # What this test asserts
//
//  1. REQUIRED: The guest serial log contains a line
//     "nexus3-agent: network: configuring eth0: …" — the agent picked a
//     hardware-backed interface. If the regression returns and dummy0 is
//     chosen, the line reads "configuring dummy0: …" and the test FAILS.
//
//  2. TODO (egress-probe upgrade): stand up a host-side TCP listener on the
//     perimeter (192.168.127.1) and have the agent exec a connect attempt,
//     asserting the connection succeeds. The driver already sets up a full
//     TAP + perimeter netstack; the blocker is that ip(8) and a static netcat
//     are absent from the minimal smoke rootfs. Add them to enable this leg.
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestBootEgressSmoke \
//	    ./internal/core/driver/cloudhypervisor/ -v -timeout 120s
//
// # Guard conditions (test skips, never fails, when absent)
//
//   - /dev/kvm accessible
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN or ~/.local/bin/cloud-hypervisor)
//   - mke2fs (e2fsprogs package)
//   - images/kernel/vmlinux-x86_64 (run scripts/fetch-boot-artifacts.sh)

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// buildEgressSmokeRootfs assembles the minimal rootfs directory needed for the
// egress smoke test. It contains only what nexus3-agent requires to boot as
// PID 1 and log its network-interface selection:
//
//	/sbin/nexus3-agent — PID-1 init binary (must be static; see buildNexus3Agent)
//	/etc/              — agent writes /etc/resolv.conf here
//	/dev/ /proc/ /sys/ /tmp/ — empty mount-point directories
//
// No ip(8) binary is included. The agent still logs the "configuring <iface>"
// line before it searches for ip(8), so the serial assertion works even with
// ip(8) absent (network config beyond the log line is skipped, which is fine).
func buildEgressSmokeRootfs(t *testing.T, agentBin string) string {
	t.Helper()
	rootfs := t.TempDir()

	for _, d := range []string{"sbin", "etc", "dev", "proc", "sys", "tmp"} {
		if err := os.MkdirAll(filepath.Join(rootfs, d), 0o755); err != nil {
			t.Fatalf("mkdir rootfs/%s: %v", d, err)
		}
	}

	data, err := os.ReadFile(agentBin)
	if err != nil {
		t.Fatalf("read agent binary: %v", err)
	}
	dst := filepath.Join(rootfs, "sbin", "nexus3-agent")
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("write /sbin/nexus3-agent: %v", err)
	}
	return rootfs
}

// pollSerialForNetworkIface polls serialPath until the agent emits its
// "network: configuring <iface>: ip=… gw=…" line or the deadline elapses.
// Returns the interface name extracted from the line, or "" on timeout.
//
// The agent logs this line immediately after firstNonLoIfaceAt() returns, so
// it appears well before the agent starts listening on vsock.
func pollSerialForNetworkIface(serialPath string, deadline time.Time) string {
	const marker = "nexus3-agent: network: configuring "
	for time.Now().Before(deadline) {
		f, err := os.Open(serialPath)
		if err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := sc.Text()
				if idx := strings.Index(line, marker); idx >= 0 {
					// Format: "nexus3-agent: network: configuring <iface>: ip=… gw=…"
					// Extract interface name: text after marker up to the first ":".
					rest := line[idx+len(marker):]
					if colon := strings.IndexByte(rest, ':'); colon > 0 {
						f.Close()
						return rest[:colon]
					}
				}
			}
			f.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// TestBootEgressSmoke boots a real microVM on the committed guest kernel
// (Linux 6.12.76) with the current nexus3-agent as PID 1 and asserts that the
// agent configures a hardware-backed network interface (eth0), NOT the virtual
// dummy0 interface that CONFIG_DUMMY=y creates.
//
// HOW THE REGRESSION BITES: if firstNonLoIfaceAt reverts to alphabetical
// first-non-lo selection (the old behaviour), the kernel's dummy0 interface
// ("dummy" < "eth" alphabetically) is selected before eth0. The agent assigns
// 192.168.127.2/24 to dummy0 (a black-hole TX device), and all DNS and egress
// from the guest silently fail. The serial log then shows:
//
//	nexus3-agent: network: configuring dummy0: ip=192.168.127.2/24 gw=…
//
// This test catches that: pollSerialForNetworkIface extracts "dummy0", the
// gotIface == "dummy0" check fires, and the test FAILS.
func TestBootEgressSmoke(t *testing.T) {
	// guards
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")
	skipUnlessMke2fs(t)

	// build static agent binary
	// CRITICAL: CGO_ENABLED=0 (static) is mandatory for a PID-1 binary.
	// A dynamically-linked binary panics the kernel (ENOENT on the dynamic
	// loader, which is absent from the minimal rootfs). buildNexus3Agent sets
	// CGO_ENABLED=0 internally (see agent_integration_test.go).
	agentBin := buildNexus3Agent(t)

	// assemble rootfs + ext4
	rootfsDir := buildEgressSmokeRootfs(t, agentBin)
	ext4Path := buildExt4Image(t, rootfsDir)

	// socket dir (sun_path safe)
	socketDir, err := os.MkdirTemp("/tmp", "ch-egress-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	serialPath := filepath.Join(socketDir, "serial.log")

	// driver
	drv, err := New(Config{
		BinaryPath:       chBin,
		SocketDir:        socketDir,
		KernelPath:       kernelPath,
		DiskImagePath:    ext4Path,   // disk-boot: root=/dev/vda, init=/sbin/nexus3-agent
		SerialOutputPath: serialPath, // capture kernel + agent output to file
		VCPUs:            1,
		MemoryMiB:        256,
		StartTimeout:     45 * time.Second,
	})
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("New CHDriver: %v", err)
	}

	id := domain.NewSandboxID()
	var vmmPID int

	t.Cleanup(func() {
		// Print serial log before stopping — visible on failure and in -v mode.
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 {
			t.Logf("guest serial output:\n%s", content)
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = drv.Stop(stopCtx, id)
		if vmmPID != 0 {
			// SIGKILL the VMM process group by negative PID (pgid = pid).
			// Scoped to the unique vmmPID — does not match unrelated processes.
			_ = syscall.Kill(-vmmPID, syscall.SIGKILL)
		}
		drv.clearState(id)
		os.RemoveAll(socketDir)
	})

	// boot
	startCtx, startCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer startCancel()

	t.Log("Starting microVM on committed kernel (Linux 6.12.76, CONFIG_DUMMY=y)...")
	bootStart := time.Now()
	if _, err := drv.Start(startCtx, driver.StartRequest{SandboxID: id}); err != nil {
		t.Fatalf("drv.Start: %v\n(check serial log at %s)", err, serialPath)
	}
	t.Logf("VM started in %v; waiting for guest agent vsock...", time.Since(bootStart))

	// Record VMM PID for scoped cleanup.
	drv.mu.Lock()
	if proc := drv.procs[id]; proc != nil {
		vmmPID = proc.pid
	}
	drv.mu.Unlock()

	// wait for agent
	// Poll vsock port 1024 (AgentControlPort) until nexus3-agent accepts.
	// By the time vsock is up, the agent has already logged its network
	// selection — so the serial assertion below will find the line immediately.
	waitForAgentReady(t, drv, id, 30*time.Second)
	t.Log("agent vsock reachable — asserting hardware network interface in serial log")

	// assert hardware interface (primary gate)
	//
	// The agent logs immediately after firstNonLoIfaceAt() returns:
	//   "nexus3-agent: network: configuring <iface>: ip=192.168.127.2/24 gw=…"
	//
	// REGRESSION: old firstNonLoIfaceAt (alphabetical-first-non-lo) would
	// return "dummy0" on this kernel because "dummy" < "eth". The line would
	// read "configuring dummy0:…" — gotIface would be "dummy0" — and the check
	// below fires, FAILING the test.
	gotIface := pollSerialForNetworkIface(serialPath, time.Now().Add(10*time.Second))

	if gotIface == "" {
		// Line not found at all — diagnose via serial tail.
		var tail string
		if data, err := os.ReadFile(serialPath); err == nil {
			tail = string(data)
			if len(tail) > 4096 {
				tail = "...(truncated)...\n" + tail[len(tail)-4096:]
			}
		}
		t.Fatalf("serial log did not contain \"network: configuring\" within 10s after agent ready\n"+
			"serial log:\n%s", tail)
	}

	// REGRESSION GUARD: dummy0 means the old alphabetical selection is back.
	if gotIface == "dummy0" {
		t.Errorf("REGRESSION: agent configured dummy0 (virtual, black-hole TX) instead of eth0 (virtio-net).\n"+
			"Cause: firstNonLoIfaceAt in cmd/nexus3-agent/network.go reverted to alphabetical\n"+
			"       first-non-lo selection. Restore the /sys/class/net/<name>/device-symlink\n"+
			"       preference so hardware interfaces are preferred over virtual ones.")
	}

	// Require eth-prefixed name: Cloud Hypervisor's virtio-net always appears
	// as "eth0" on kernels without predictable-name udev (which is the case
	// here — nexus3-agent is PID 1 and udev is absent).
	if !strings.HasPrefix(gotIface, "eth") {
		t.Errorf("unexpected interface %q: expected eth0 (virtio-net on Cloud Hypervisor).\n"+
			"If the kernel renamed the interface (e.g. ens3), update this assertion.", gotIface)
	}

	t.Logf("PASS: agent selected hardware interface %q — dummy0 regression is NOT present", gotIface)

	// TODO: egress-probe upgrade
	//
	// The driver sets up a full perimeter netstack (gvproxy/netstack at
	// 192.168.127.1). The next hardening step:
	//  1. Stand up a host-side TCP listener on 192.168.127.1:<port> before booting.
	//  2. Wait for the agent to be ready (vsock up = network configured).
	//  3. Use agent.Exec to run: nc/socat/wget 192.168.127.1 <port> (or a
	//     static probe binary baked into the rootfs).
	//  4. Assert the host-side listener receives the connection.
	// Blocker: ip(8) is absent from the minimal rootfs so the interface is
	// logged but not brought up (agent logs "ip(8) not found; skipping").
	// Add a static ip(8) or a minimal ioctl-based bringup binary to the
	// rootfs to remove this blocker.
}
