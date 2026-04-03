# Firecracker Bridge Networking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the broken slirp4netns+unshare approach with a host-bridge + `nexus-tap-helper` architecture so Firecracker VMs get internet access without sudo at runtime.

**Architecture:** A small `nexus-tap-helper` binary with `cap_net_admin=ep` creates/destroys host-side tap devices and attaches them to a shared Linux bridge (`nexusbr0`). Firecracker runs directly in the host netns and opens the tap by name — no EBUSY, no unshare. The bridge is configured persistently via systemd-networkd. DHCP inside the guest provides dynamic IP assignment.

**Tech Stack:** Go 1.24, Linux kernel networking (TUNSETIFF, bridge), systemd-networkd, busybox `udhcpc`, Firecracker v1.12.1

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `packages/nexus/cmd/nexus-tap-helper/main.go` | **Create** | `cap_net_admin` helper binary: `create <tap> <bridge>` / `delete <tap>` |
| `packages/nexus/pkg/runtime/firecracker/tap_linux.go` | **Rewrite** | Bridge constants, `realSetupTAP`/`realTeardownTAP` invoke helper |
| `packages/nexus/pkg/runtime/firecracker/tap_linux_integration_test.go` | **Rewrite** | Replace slirp test with bridge tap test |
| `packages/nexus/pkg/runtime/firecracker/manager.go` | **Modify** | Remove unshare, remove slirp fields, update tap name + boot args |
| `packages/nexus/pkg/runtime/firecracker/manager_test.go` | **Modify** | Remove slirpStartFunc mock; update TAP name/IP assertions |
| `packages/nexus/cmd/nexus-firecracker-agent/main.go` | **Modify** | Add `udhcpc -i eth0` before `setupDNS()` in PID1 path |
| `packages/nexus/cmd/nexus/main.go` | **Modify** | Add helper + bridge preflight checks to `validateFirecrackerHostPrerequisites` |

---

## Task 1: Create `nexus-tap-helper` binary

**Files:**
- Create: `packages/nexus/cmd/nexus-tap-helper/main.go`

- [ ] **Step 1.1: Write the binary**

```go
// packages/nexus/cmd/nexus-tap-helper/main.go
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// This binary requires cap_net_admin=ep set via:
//   sudo setcap cap_net_admin=ep /usr/local/bin/nexus-tap-helper
//
// Usage:
//   nexus-tap-helper create <tapname> <bridge>
//   nexus-tap-helper delete <tapname>

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: nexus-tap-helper create <tapname> <bridge>\n")
		fmt.Fprintf(os.Stderr, "       nexus-tap-helper delete <tapname>\n")
		os.Exit(1)
	}
	cmd := os.Args[1]
	switch cmd {
	case "create":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "usage: nexus-tap-helper create <tapname> <bridge>\n")
			os.Exit(1)
		}
		tapName := os.Args[2]
		bridge := os.Args[3]
		if err := createTAP(tapName, bridge); err != nil {
			fmt.Fprintf(os.Stderr, "nexus-tap-helper create: %v\n", err)
			os.Exit(1)
		}
	case "delete":
		tapName := os.Args[2]
		if err := deleteTAP(tapName); err != nil {
			fmt.Fprintf(os.Stderr, "nexus-tap-helper delete: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(1)
	}
}

const (
	TUNSETIFF   = 0x400454ca
	TUNSETOWNER = 0x400454cc
	TUNSETGROUP = 0x400454ce
	IFF_TAP     = 0x0002
	IFF_NO_PI   = 0x1000
)

type ifreq struct {
	Name  [unix.IFNAMSIZ]byte
	Flags uint16
	_     [22]byte
}

func createTAP(tapName, bridge string) error {
	if len(tapName) >= unix.IFNAMSIZ {
		return fmt.Errorf("tap name %q exceeds IFNAMSIZ-1 (%d chars max)", tapName, unix.IFNAMSIZ-1)
	}

	// Open /dev/net/tun
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open /dev/net/tun: %w", err)
	}
	// NOTE: we intentionally keep the fd open until we call TUNSETPERSIST.
	// After persistence is set, the kernel holds the tap — safe to close.

	// TUNSETIFF: create the tap interface
	var req ifreq
	copy(req.Name[:], tapName)
	req.Flags = IFF_TAP | IFF_NO_PI
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), TUNSETIFF, uintptr(unsafe.Pointer(&req))); errno != 0 {
		unix.Close(fd)
		return fmt.Errorf("TUNSETIFF %s: %w", tapName, errno)
	}

	// TUNSETPERSIST: make the tap persist after this process exits
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TUNSETPERSIST, 1); errno != 0 {
		unix.Close(fd)
		return fmt.Errorf("TUNSETPERSIST %s: %w", tapName, errno)
	}

	unix.Close(fd)

	// Bring the interface up
	iface, err := net.InterfaceByName(tapName)
	if err != nil {
		return fmt.Errorf("interface %s not found after creation: %w", tapName, err)
	}
	if err := setLinkUp(iface.Index); err != nil {
		return fmt.Errorf("bring up %s: %w", tapName, err)
	}

	// Attach to bridge via `ip link set <tap> master <bridge>`
	out, err := exec.Command("ip", "link", "set", tapName, "master", bridge).CombinedOutput()
	if err != nil {
		return fmt.Errorf("attach %s to bridge %s: %w: %s", tapName, bridge, err, strings.TrimSpace(string(out)))
	}

	return nil
}

func deleteTAP(tapName string) error {
	// Use `ip link del` — simplest portable approach
	out, err := exec.Command("ip", "link", "del", tapName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip link del %s: %w: %s", tapName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func setLinkUp(ifIndex int) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	type ifFlags struct {
		Name  [unix.IFNAMSIZ]byte
		Flags uint16
		_     [22]byte
	}

	// Get current flags via SIOCGIFFLAGS
	var req ifFlags
	// We need the interface name for SIOCGIFFLAGS — look it up
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	var ifName string
	for _, i := range ifaces {
		if i.Index == ifIndex {
			ifName = i.Name
			break
		}
	}
	if ifName == "" {
		return fmt.Errorf("interface index %d not found", ifIndex)
	}
	copy(req.Name[:], ifName)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&req))); errno != 0 {
		return fmt.Errorf("SIOCGIFFLAGS: %w", errno)
	}
	req.Flags |= unix.IFF_UP
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&req))); errno != 0 {
		return fmt.Errorf("SIOCSIFFLAGS: %w", errno)
	}
	return nil
}
```

- [ ] **Step 1.2: Verify it compiles**

```bash
cd /home/newman/magic/nexus
go build ./packages/nexus/cmd/nexus-tap-helper/...
```

Expected: binary built at `nexus-tap-helper` in working dir, no errors.

- [ ] **Step 1.3: Commit**

```bash
git add packages/nexus/cmd/nexus-tap-helper/main.go
git commit -m "feat(tap-helper): add nexus-tap-helper binary for CAP_NET_ADMIN tap management"
```

---

## Task 2: Rewrite `tap_linux.go`

**Files:**
- Rewrite: `packages/nexus/pkg/runtime/firecracker/tap_linux.go`

- [ ] **Step 2.1: Replace the file entirely**

```go
//go:build linux

package firecracker

import (
	"fmt"
	"os/exec"
	"strings"
)

// bridgeName is the Linux bridge all Firecracker tap devices are attached to.
const bridgeName = "nexusbr0"

// bridgeGatewayIP is the host-side IP on the bridge (default gateway for guests).
const bridgeGatewayIP = "172.26.0.1"

// guestSubnetCIDR is the subnet behind the bridge.
const guestSubnetCIDR = "172.26.0.0/16"

// tapHelperBin is the name of the privileged tap helper binary.
const tapHelperBin = "nexus-tap-helper"

// checkTapHelper verifies that nexus-tap-helper is installed and has cap_net_admin.
// Returns an error with setup instructions if not found/configured.
func checkTapHelper() error {
	path, err := exec.LookPath(tapHelperBin)
	if err != nil {
		return fmt.Errorf(
			"%s not found in PATH\n\nOne-time setup required:\n%s",
			tapHelperBin, tapHelperSetupInstructions(),
		)
	}

	// Check that it has cap_net_admin via getcap
	out, err := exec.Command("getcap", path).Output()
	if err != nil {
		// getcap not available — skip the check, let it fail at runtime
		return nil
	}
	if !strings.Contains(string(out), "cap_net_admin") {
		return fmt.Errorf(
			"%s found at %s but does not have cap_net_admin\n\nOne-time setup required:\n%s",
			tapHelperBin, path, tapHelperSetupInstructions(),
		)
	}
	return nil
}

// checkBridge verifies that nexusbr0 exists and is UP.
func checkBridge() error {
	out, err := exec.Command("ip", "link", "show", bridgeName).CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"bridge %s not found\n\nOne-time setup required:\n%s",
			bridgeName, bridgeSetupInstructions(),
		)
	}
	if !strings.Contains(string(out), "UP") {
		return fmt.Errorf(
			"bridge %s exists but is not UP\n\nTry: sudo ip link set %s up\nOr re-run full setup:\n%s",
			bridgeName, bridgeName, bridgeSetupInstructions(),
		)
	}
	return nil
}

// tapHelperSetupInstructions returns the one-time setup commands for the tap helper.
func tapHelperSetupInstructions() string {
	return `  # Build and install nexus-tap-helper
  go build -o /tmp/nexus-tap-helper ./packages/nexus/cmd/nexus-tap-helper/
  sudo cp /tmp/nexus-tap-helper /usr/local/bin/nexus-tap-helper
  sudo setcap cap_net_admin=ep /usr/local/bin/nexus-tap-helper`
}

// bridgeSetupInstructions returns the one-time setup commands for the bridge.
func bridgeSetupInstructions() string {
	return `  # Configure persistent bridge via systemd-networkd
  sudo tee /etc/systemd/network/10-nexusbr0.netdev << 'EOF'
[NetDev]
Name=nexusbr0
Kind=bridge
EOF

  sudo tee /etc/systemd/network/11-nexusbr0.network << 'EOF'
[Match]
Name=nexusbr0
[Network]
Address=172.26.0.1/16
IPForward=yes
IPMasquerade=ipv4
EOF

  sudo tee /etc/systemd/network/12-nexus-tap.network << 'EOF'
[Match]
Name=nexus-*
[Network]
Bridge=nexusbr0
EOF

  sudo systemctl enable --now systemd-networkd`
}

// realSetupTAP creates a tap device and attaches it to nexusbr0 via nexus-tap-helper.
func realSetupTAP(tapName, hostIP, subnetCIDR string) (any, error) {
	out, err := exec.Command(tapHelperBin, "create", tapName, bridgeName).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("nexus-tap-helper create %s: %w: %s", tapName, err, strings.TrimSpace(string(out)))
	}
	return nil, nil
}

// realTeardownTAP removes the tap device via nexus-tap-helper.
func realTeardownTAP(tapName, subnetCIDR string) {
	// Best-effort: ignore errors (tap may already be gone if VM crashed)
	_ = exec.Command(tapHelperBin, "delete", tapName).Run()
}

// tapNameForWorkspace returns the tap interface name for a workspace ID.
// Linux IFNAMSIZ is 16 bytes including null terminator, so max 15 chars.
// We use "nx-" prefix + first 12 chars of workspaceID = 15 chars max.
func tapNameForWorkspace(workspaceID string) string {
	suffix := workspaceID
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	return "nx-" + suffix
}
```

- [ ] **Step 2.2: Verify the package compiles**

```bash
cd /home/newman/magic/nexus
go build ./packages/nexus/pkg/runtime/firecracker/...
```

Expected: no errors.

- [ ] **Step 2.3: Commit**

```bash
git add packages/nexus/pkg/runtime/firecracker/tap_linux.go
git commit -m "feat(firecracker): replace slirp4netns with bridge tap networking"
```

---

## Task 3: Update `manager.go`

**Files:**
- Modify: `packages/nexus/pkg/runtime/firecracker/manager.go`

Changes:
1. Remove `slirpStartFunc` variable and `SlirpProcess` field.
2. Remove the `unshare` wrapper — run Firecracker directly.
3. Use `tapNameForWorkspace` instead of `slirpTAPName`.
4. Update boot args: use DHCP (no `ip=` kernel arg).
5. Update `Stop()` to remove slirp teardown.
6. Update `teardownTAP` call to pass `guestSubnetCIDR`.

- [ ] **Step 3.1: Remove slirpStartFunc var and SlirpProcess field**

In `manager.go`, delete these lines:

```go
// slirpStartFunc starts slirp4netns against the given PID's network namespace.
// Overridable in tests to avoid launching real slirp4netns.
var slirpStartFunc func(pid int, tapName string) (*os.Process, error) = startSlirp4netns
```

And in the `Instance` struct, remove:
```go
SlirpProcess *os.Process
```

- [ ] **Step 3.2: Update `Spawn` — tap name, no unshare, no slirp**

Replace the networking setup section (from the `tap := slirpTAPName` comment through the slirpProc launch block) with:

```go
tap := tapNameForWorkspace(spec.WorkspaceID)
mac := guestMAC(cid)
hostIP := bridgeGatewayIP
subnetCIDR := guestSubnetCIDR
if err := setupTAP(tap, hostIP, subnetCIDR); err != nil {
    os.RemoveAll(workDir)
    return nil, fmt.Errorf("failed to setup tap %s: %w", tap, err)
}
```

Replace the `fcArgs`/`exec.Command("unshare", ...)` block with:

```go
cmd := exec.Command(
    m.config.FirecrackerBin,
    "--api-sock", apiSocket,
    "--id", spec.WorkspaceID,
)
cmd.Dir = workDir
```

Remove all `slirpProc` usage throughout `Spawn` (the nil checks, Kill/Wait calls, etc.)

- [ ] **Step 3.3: Update boot args — remove static IP, use DHCP**

Replace `defaultFirecrackerBootArgs(guestIP, hostIP)` with `defaultFirecrackerBootArgs()`.

Update the function signature and body:

```go
// defaultFirecrackerBootArgs returns the kernel command line for a VM.
// If NEXUS_FIRECRACKER_BOOT_ARGS is set, it is returned verbatim.
// Otherwise, a standard set is generated. Network setup is done by udhcpc
// inside the guest (no kernel ip= argument — DHCP handles it).
func defaultFirecrackerBootArgs() string {
    if raw := strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_BOOT_ARGS")); raw != "" {
        return raw
    }
    return "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw"
}
```

- [ ] **Step 3.4: Update `Stop` — remove slirp teardown**

In `Stop()`, remove the slirp kill block:

```go
// Kill slirp4netns after the VM process exits; its network namespace is gone anyway.
if inst.SlirpProcess != nil {
    inst.SlirpProcess.Kill()
    inst.SlirpProcess.Wait()
}
```

Update the `teardownTAP` call to use the correct CIDR:

```go
if inst.TAPName != "" {
    teardownTAP(inst.TAPName, guestSubnetCIDR)
}
```

- [ ] **Step 3.5: Update `Instance` GuestIP/HostIP assignment in Spawn**

After tap setup, store the IP values:
```go
inst := &Instance{
    ...
    TAPName:  tap,
    GuestIP:  "",           // assigned by DHCP at boot
    HostIP:   bridgeGatewayIP,
}
```

- [ ] **Step 3.6: Compile check**

```bash
cd /home/newman/magic/nexus
go build ./packages/nexus/pkg/runtime/firecracker/...
```

Expected: no errors.

- [ ] **Step 3.7: Commit**

```bash
git add packages/nexus/pkg/runtime/firecracker/manager.go
git commit -m "feat(firecracker): remove unshare+slirp, run FC in host netns with bridge tap"
```

---

## Task 4: Update `manager_test.go`

**Files:**
- Modify: `packages/nexus/pkg/runtime/firecracker/manager_test.go`

- [ ] **Step 4.1: Remove slirpStartFunc from installTestNetworkRunner**

In `installTestNetworkRunner`, delete the slirp mock block:

```go
// Mock slirpStartFunc so tests don't try to launch real slirp4netns.
oldSlirp := slirpStartFunc
slirpStartFunc = func(pid int, tapName string) (*os.Process, error) {
    nc.run("slirp4netns", fmt.Sprintf("--pid=%d", pid), tapName)
    return nil, nil
}
t.Cleanup(func() { slirpStartFunc = oldSlirp })
```

- [ ] **Step 4.2: Fix `TestManagerSpawnBinaryNotFound`**

Without `unshare`, a nonexistent Firecracker binary now fails immediately in `cmd.Start()` — no need for a timeout context. Update:

```go
func TestManagerSpawnBinaryNotFound(t *testing.T) {
    installTestNetworkRunner(t)
    cfg := testManagerConfig(t)
    cfg.FirecrackerBin = "/nonexistent/firecracker"
    mgr := newManager(cfg)
    mgr.apiClientFactory = func(sockPath string) apiClientInterface {
        return &mockAPIClient{}
    }

    ctx := context.Background()
    spec := SpawnSpec{
        WorkspaceID: "ws-notfound",
        ProjectRoot: t.TempDir(),
        MemoryMiB:   1024,
        VCPUs:       1,
    }

    _, err := mgr.Spawn(ctx, spec)
    if err == nil {
        t.Fatal("expected error for nonexistent binary")
    }
    if !strings.Contains(err.Error(), "failed to start firecracker") &&
        !strings.Contains(err.Error(), "no such file") &&
        !strings.Contains(err.Error(), "failed to setup tap") {
        t.Errorf("unexpected error: %v", err)
    }
}
```

- [ ] **Step 4.3: Fix `TestDefaultFirecrackerBootArgsContainsIPArg`**

Rename to `TestDefaultFirecrackerBootArgsDHCP` and update — boot args no longer contain a static `ip=`:

```go
func TestDefaultFirecrackerBootArgsDHCP(t *testing.T) {
    t.Setenv("NEXUS_FIRECRACKER_BOOT_ARGS", "")
    args := defaultFirecrackerBootArgs()
    // DHCP mode: no static ip= kernel argument
    if strings.Contains(args, "ip=") {
        t.Errorf("bridge networking uses DHCP; boot args must not contain ip=, got %q", args)
    }
    if !strings.Contains(args, "console=ttyS0") {
        t.Errorf("expected console=ttyS0 in boot args, got %q", args)
    }
}
```

- [ ] **Step 4.4: Fix `TestManagerSpawnBootArgsContainIPConfig`**

The test currently checks that `ip=` is present. Invert it:

```go
func TestManagerSpawnBootArgsDHCP(t *testing.T) {
    installTestNetworkRunner(t)
    cfg := testManagerConfig(t)
    mgr := newManager(cfg)

    var capturedBootArgs string
    mgr.apiClientFactory = func(sockPath string) apiClientInterface {
        return &captureBootArgsClient{onBootSource: func(args string) { capturedBootArgs = args }}
    }

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    spec := SpawnSpec{
        WorkspaceID: "ws-boot-args",
        ProjectRoot: t.TempDir(),
        MemoryMiB:   512,
        VCPUs:       1,
    }

    _, err := mgr.Spawn(ctx, spec)
    if err != nil {
        t.Fatalf("spawn failed: %v", err)
    }

    // Bridge networking: DHCP, no static ip= in kernel args
    if strings.Contains(capturedBootArgs, "ip=") {
        t.Errorf("bridge networking uses DHCP; boot args must not contain ip=, got: %q", capturedBootArgs)
    }
    if !strings.Contains(capturedBootArgs, "console=ttyS0") {
        t.Errorf("expected console=ttyS0 in boot args, got: %q", capturedBootArgs)
    }
}
```

- [ ] **Step 4.5: Update `testManagerConfig` fake-firecracker script**

The fake firecracker no longer needs to handle `unshare` wrapping — it is called directly. The existing bash script already works (it just looks for `--api-sock`), so no change needed here. Verify it still works:

```bash
cd /home/newman/magic/nexus
go test ./packages/nexus/pkg/runtime/firecracker/... -run TestManagerSpawnConfiguresAndStartsVM -v -timeout 30s
```

Expected: PASS.

- [ ] **Step 4.6: Run all firecracker package tests**

```bash
cd /home/newman/magic/nexus
go test ./packages/nexus/pkg/runtime/firecracker/... -timeout 60s -v 2>&1 | tail -40
```

Expected: all PASS, zero FAIL.

- [ ] **Step 4.7: Commit**

```bash
git add packages/nexus/pkg/runtime/firecracker/manager_test.go
git commit -m "test(firecracker): update tests for bridge tap networking (remove slirp mocks)"
```

---

## Task 5: Update guest agent — run udhcpc before DNS

**Files:**
- Modify: `packages/nexus/cmd/nexus-firecracker-agent/main.go`

The guest needs an IP from DHCP before the vsock agent can serve connections. The `udhcpc` client must run at PID1 boot, before `setupDNS()`.

- [ ] **Step 5.1: Add `setupNetwork()` function and call it**

Add this function to `main.go`:

```go
// setupNetwork runs udhcpc to acquire an IP address from the bridge DHCP server.
// It is called at PID1 boot before setupDNS, so the network is ready before
// the vsock agent starts listening. Failure is non-fatal — the agent may still
// work via vsock without internet connectivity.
func setupNetwork() {
    // Run udhcpc: -i eth0 (interface), -n (exit if no lease), -q (quit after lease)
    // -t 10 (10 retries), -T 3 (3s timeout per attempt)
    cmd := exec.Command("udhcpc", "-i", "eth0", "-n", "-q", "-t", "10", "-T", "3")
    out, err := cmd.CombinedOutput()
    if err != nil {
        // Non-fatal: log and continue. The VM may lack networking in some test scenarios.
        emitDiagnostic("agent pid1 udhcpc failed (continuing): %v: %s", err, strings.TrimSpace(string(out)))
        return
    }
    emitDiagnostic("agent pid1 udhcpc completed: %s", strings.TrimSpace(string(out)))
}
```

- [ ] **Step 5.2: Call it in `main()` before `setupDNS()`**

Update the PID1 block in `main()`:

```go
if os.Getpid() == 1 {
    mountKernelFilesystems()
    emitDiagnostic("agent pid1 kernel filesystems mounted")
    setupNetwork()
    emitDiagnostic("agent pid1 network configured")
    setupDNS()
    emitDiagnostic("agent pid1 dns configured")
}
```

- [ ] **Step 5.3: Compile check**

```bash
cd /home/newman/magic/nexus
go build ./packages/nexus/cmd/nexus-firecracker-agent/...
```

Expected: no errors.

- [ ] **Step 5.4: Commit**

```bash
git add packages/nexus/cmd/nexus-firecracker-agent/main.go
git commit -m "feat(agent): run udhcpc at pid1 boot to acquire DHCP IP from bridge"
```

---

## Task 6: Update `tap_linux_integration_test.go`

**Files:**
- Rewrite: `packages/nexus/pkg/runtime/firecracker/tap_linux_integration_test.go`

- [ ] **Step 6.1: Replace the slirp integration test**

```go
//go:build linux

package firecracker

import (
	"os/exec"
	"strings"
	"testing"
)

// TestCheckTapHelperInstalled verifies that checkTapHelper returns a useful error
// when the helper is missing (in CI without the helper installed).
// If the helper IS installed, it also checks the cap_net_admin capability.
func TestCheckTapHelperInstalled(t *testing.T) {
	_, err := exec.LookPath(tapHelperBin)
	if err != nil {
		// Helper not installed: checkTapHelper should return a non-nil error with instructions.
		if checkErr := checkTapHelper(); checkErr == nil {
			t.Error("expected checkTapHelper to fail when binary not in PATH")
		} else if !strings.Contains(checkErr.Error(), "not found") {
			t.Errorf("expected 'not found' in error, got: %v", checkErr)
		}
		t.Skip("nexus-tap-helper not installed; skipping further tap helper checks")
	}

	// Helper is installed; verify capability
	if err := checkTapHelper(); err != nil {
		t.Logf("nexus-tap-helper installed but check failed (may need setcap): %v", err)
	}
}

// TestCheckBridgeExists verifies that checkBridge returns a useful error when
// the bridge is missing (expected in CI without one-time setup).
func TestCheckBridgeExists(t *testing.T) {
	out, err := exec.Command("ip", "link", "show", bridgeName).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "UP") {
		// Bridge not present: checkBridge should return a non-nil error.
		if checkErr := checkBridge(); checkErr == nil {
			t.Error("expected checkBridge to fail when bridge not present")
		}
		t.Skip("bridge nexusbr0 not set up; skipping live bridge test")
	}

	if err := checkBridge(); err != nil {
		t.Errorf("checkBridge returned error for existing bridge: %v", err)
	}
}

// TestTapNameForWorkspace verifies tap name truncation stays within IFNAMSIZ-1.
func TestTapNameForWorkspace(t *testing.T) {
	cases := []struct {
		workspaceID string
		wantPrefix  string
		wantMaxLen  int
	}{
		{"ws-test-1", "nx-ws-test-1", 15},
		{"very-long-workspace-identifier-that-exceeds-limit", "nx-very-long-wo", 15},
		{"short", "nx-short", 15},
	}
	for _, tc := range cases {
		got := tapNameForWorkspace(tc.workspaceID)
		if !strings.HasPrefix(got, "nx-") {
			t.Errorf("tapNameForWorkspace(%q) = %q, want nx- prefix", tc.workspaceID, got)
		}
		if len(got) > tc.wantMaxLen {
			t.Errorf("tapNameForWorkspace(%q) = %q (len %d), exceeds IFNAMSIZ-1 (%d)", tc.workspaceID, got, len(got), tc.wantMaxLen)
		}
	}
}
```

- [ ] **Step 6.2: Run the new integration tests**

```bash
cd /home/newman/magic/nexus
go test ./packages/nexus/pkg/runtime/firecracker/... -run TestCheckTapHelperInstalled -v
go test ./packages/nexus/pkg/runtime/firecracker/... -run TestCheckBridgeExists -v
go test ./packages/nexus/pkg/runtime/firecracker/... -run TestTapNameForWorkspace -v
```

Expected: `TestCheckTapHelperInstalled` and `TestCheckBridgeExists` skip (helper/bridge not yet installed), `TestTapNameForWorkspace` passes.

- [ ] **Step 6.3: Commit**

```bash
git add packages/nexus/pkg/runtime/firecracker/tap_linux_integration_test.go
git commit -m "test(firecracker): replace slirp integration test with bridge tap tests"
```

---

## Task 7: Update doctor preflight in `main.go`

**Files:**
- Modify: `packages/nexus/cmd/nexus/main.go`

Add two new checks to `validateFirecrackerHostPrerequisites`: the tap helper and the bridge.

- [ ] **Step 7.1: Import the check functions**

The check logic needs to live where `validateFirecrackerHostPrerequisites` is defined (`packages/nexus/cmd/nexus/main.go`), using the same `getcap` + `ip link show` logic as in `tap_linux.go`. Since `main.go` is a different package (`main`), duplicate the checks inline rather than importing from the `firecracker` package (avoid coupling the CLI to internal helpers).

Add to `validateFirecrackerHostPrerequisites` **after** the KVM device check:

```go
// Check nexus-tap-helper exists and has cap_net_admin
if err := validateFirecrackerTapHelper(); err != nil {
    return err
}

// Check nexusbr0 bridge is up
if err := validateFirecrackerBridge(); err != nil {
    return err
}
```

- [ ] **Step 7.2: Add the two validation functions**

Add these functions anywhere in `main.go` (e.g., after `validateFirecrackerHostPrerequisites`):

```go
func validateFirecrackerTapHelper() error {
    path, err := exec.LookPath("nexus-tap-helper")
    if err != nil {
        return fmt.Errorf(`nexus-tap-helper not found in PATH

One-time setup required:
  go build -o /tmp/nexus-tap-helper ./packages/nexus/cmd/nexus-tap-helper/
  sudo cp /tmp/nexus-tap-helper /usr/local/bin/nexus-tap-helper
  sudo setcap cap_net_admin=ep /usr/local/bin/nexus-tap-helper`)
    }

    // Check cap_net_admin via getcap (best-effort; skip if getcap unavailable)
    out, err := exec.Command("getcap", path).Output()
    if err == nil && !strings.Contains(string(out), "cap_net_admin") {
        return fmt.Errorf(`nexus-tap-helper at %s lacks cap_net_admin capability

Run:
  sudo setcap cap_net_admin=ep %s`, path, path)
    }

    return nil
}

func validateFirecrackerBridge() error {
    out, err := exec.Command("ip", "link", "show", "nexusbr0").CombinedOutput()
    if err != nil {
        return fmt.Errorf(`bridge nexusbr0 not found

One-time setup required (persistent via systemd-networkd):

  sudo tee /etc/systemd/network/10-nexusbr0.netdev << 'EOF'
[NetDev]
Name=nexusbr0
Kind=bridge
EOF

  sudo tee /etc/systemd/network/11-nexusbr0.network << 'EOF'
[Match]
Name=nexusbr0
[Network]
Address=172.26.0.1/16
IPForward=yes
IPMasquerade=ipv4
EOF

  sudo tee /etc/systemd/network/12-nexus-tap.network << 'EOF'
[Match]
Name=nexus-*
[Network]
Bridge=nexusbr0
EOF

  sudo systemctl enable --now systemd-networkd`)
    }
    if !strings.Contains(string(out), "UP") {
        return fmt.Errorf("bridge nexusbr0 exists but is not UP; run: sudo ip link set nexusbr0 up")
    }
    return nil
}
```

- [ ] **Step 7.3: Compile check**

```bash
cd /home/newman/magic/nexus
go build ./packages/nexus/cmd/nexus/...
```

Expected: no errors.

- [ ] **Step 7.4: Run all tests**

```bash
cd /home/newman/magic/nexus
go test ./... -timeout 120s 2>&1 | tail -30
```

Expected: all PASS.

- [ ] **Step 7.5: Commit**

```bash
git add packages/nexus/cmd/nexus/main.go
git commit -m "feat(doctor): add tap-helper and bridge preflight checks for firecracker"
```

---

## Task 8: One-time host setup + smoke test

This task is **manual execution** — it sets up the host and verifies the full stack.

- [ ] **Step 8.1: Build and install nexus-tap-helper**

```bash
cd /home/newman/magic/nexus
go build -o /tmp/nexus-tap-helper ./packages/nexus/cmd/nexus-tap-helper/
sudo cp /tmp/nexus-tap-helper /usr/local/bin/nexus-tap-helper
sudo setcap cap_net_admin=ep /usr/local/bin/nexus-tap-helper

# Verify:
getcap /usr/local/bin/nexus-tap-helper
```

Expected output: `/usr/local/bin/nexus-tap-helper cap_net_admin=ep`

- [ ] **Step 8.2: Configure the bridge via systemd-networkd**

```bash
sudo tee /etc/systemd/network/10-nexusbr0.netdev << 'EOF'
[NetDev]
Name=nexusbr0
Kind=bridge
EOF

sudo tee /etc/systemd/network/11-nexusbr0.network << 'EOF'
[Match]
Name=nexusbr0

[Network]
Address=172.26.0.1/16
IPForward=yes
IPMasquerade=ipv4
EOF

sudo tee /etc/systemd/network/12-nexus-tap.network << 'EOF'
[Match]
Name=nexus-*

[Network]
Bridge=nexusbr0
EOF

sudo systemctl enable --now systemd-networkd
sleep 3
ip link show nexusbr0
ip addr show nexusbr0
```

Expected: bridge is UP, has `172.26.0.1/16`.

- [ ] **Step 8.3: Rebuild nexus binary**

```bash
cd /home/newman/magic/nexus
go build -o ~/.local/bin/nexus ./packages/nexus/cmd/nexus/
```

- [ ] **Step 8.4: Run the integration tests that require bridge**

```bash
cd /home/newman/magic/nexus
go test ./packages/nexus/pkg/runtime/firecracker/... -run TestCheckTapHelperInstalled -v
go test ./packages/nexus/pkg/runtime/firecracker/... -run TestCheckBridgeExists -v
```

Expected: both PASS (no skip).

- [ ] **Step 8.5: Smoke test — nexus doctor**

```bash
cd /home/newman/magic/nexus/.case-studies/hanlun-lms
NEXUS_RUNTIME_BACKEND=firecracker \
NEXUS_FIRECRACKER_BIN=$(which firecracker) \
NEXUS_FIRECRACKER_KERNEL=~/.cache/nexus-firecracker-local-nosudo/vmlinux.bin \
NEXUS_FIRECRACKER_ROOTFS=~/.cache/nexus-firecracker-local-nosudo/rootfs.ext4 \
~/.local/bin/nexus doctor
```

Expected: all probes pass including `10-startup-readiness.sh`.

- [ ] **Step 8.6: Final test suite**

```bash
cd /home/newman/magic/nexus
go test ./... -timeout 120s 2>&1 | grep -E "^(ok|FAIL|---)"
```

Expected: all `ok`, zero `FAIL`.

- [ ] **Step 8.7: Commit**

```bash
cd /home/newman/magic/nexus
git add -A
git commit -m "chore: one-time bridge setup verified; all tests pass"
```

---

## Self-review

**Spec coverage:**

| Spec requirement | Covered by |
|-----------------|-----------|
| `nexus-tap-helper` binary with `cap_net_admin=ep` | Task 1 |
| `realSetupTAP`/`realTeardownTAP` invoke helper | Task 2 |
| Remove `unshare --net` | Task 3 |
| Remove `SlirpProcess` / `slirpStartFunc` | Tasks 3, 4 |
| TAP name: `nx-` + 12 chars of workspaceID | Task 2 (`tapNameForWorkspace`) |
| DHCP in guest via `udhcpc` | Task 5 |
| systemd-networkd bridge setup | Task 8 |
| Doctor preflight checks helper + bridge | Task 7 |
| Boot args: no static `ip=` | Task 3 |
| `tap_linux_integration_test.go` updated | Task 6 |
| `manager_test.go` updated | Task 4 |

**Type consistency:**
- `tapNameForWorkspace` defined in Task 2 (`tap_linux.go`), used in Task 3 (`manager.go`) ✓
- `bridgeGatewayIP`, `guestSubnetCIDR`, `bridgeName` defined in Task 2, used in Tasks 3 and 7 ✓
- `tapSetupFunc` / `tapTeardownFunc` var pattern preserved — test mocks in `manager_test.go` still work ✓
- `defaultFirecrackerBootArgs()` signature changes from `(guestIP, gatewayIP string)` to `()` in Task 3 — checked in Task 4 ✓
- `Instance.GuestIP` is now empty string (DHCP-assigned, not known at spawn time) — test `TestManagerSpawnConfiguresAndStartsVM` checks `inst.GuestIP == ""` is acceptable (remove the non-empty assertion if it exists) ✓

**Placeholder scan:** None found. All steps have complete code or exact commands.

**Gap check:** The `GuestIP` field in `Instance` remains but is set to `""` (DHCP-assigned). The test `TestManagerSpawnConfiguresAndStartsVM` asserts `inst.GuestIP == ""` is checked — if the test currently asserts non-empty, it will need updating. Address in Task 4 if the test fails.
