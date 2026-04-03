# Firecracker Bridge Networking Design

**Date:** 2026-04-03  
**Branch:** `feat/firecracker-guest-networking`  
**Status:** Approved

---

## Problem

The current implementation wraps Firecracker in `unshare --user --net` and uses slirp4netns for NAT. This fails with `EBUSY` because:

1. slirp4netns creates `tap0` inside the private network namespace and holds the fd open.
2. Firecracker then tries to open the same `tap0` via its own `TUNSETIFF` call.
3. Linux tap devices don't allow two simultaneous TUNSETIFF openers without `IFF_MULTI_QUEUE`, which Firecracker doesn't use.

Bridge networking with a host-side tap fixes this: Firecracker opens the tap _itself_ as the first and only opener — no conflict.

---

## Approach

Remove the `unshare --net` wrapper. Run Firecracker directly in the host network namespace. Use a small `setcap`-privileged helper binary (`nexus-tap-helper`) to create and destroy host-side tap devices. Attach each tap to a shared Linux bridge (`nexusbr0`). The bridge is configured persistently via **systemd-networkd**. DHCP inside the guest is handled by the kernel's built-in `busybox udhcpd` or the existing guest agent's DNS setup.

---

## Architecture

```
Host netns
  nexusbr0  (bridge, 172.26.0.1/16, managed by systemd-networkd)
  ├── nexus-<id0>  (tap, VM 0 — created by nexus-tap-helper at spawn)
  ├── nexus-<id1>  (tap, VM 1)
  └── ...

  iptables MASQUERADE: 172.26.0.0/16 → default route interface
```

Each Firecracker VM:
- Gets a dedicated tap on the host attached to `nexusbr0`.
- Runs a DHCP client inside the guest; the bridge acts as gateway.
- Has internet access via masquerade NAT on the host.

---

## Components

### 1. `nexus-tap-helper` binary

**Location:** `packages/nexus/cmd/nexus-tap-helper/main.go`  
**Installed to:** `/usr/local/bin/nexus-tap-helper`  
**Capability:** `cap_net_admin=ep` (set once at install time via `sudo setcap`)

A minimal, no-daemon binary with two subcommands:

```
nexus-tap-helper create <tapname> <bridge>
nexus-tap-helper delete <tapname>
```

`create`:
1. Opens `/dev/net/tun` and calls `TUNSETIFF` with `IFF_TAP | IFF_NO_PI` to create the tap.
2. Sets tap ownership to the calling user's UID/GID (read from env or passed as flags).
3. Brings the tap up (`SIOCSIFFLAGS`).
4. Adds the tap to the bridge (`SIOCBRADDIF` or `ip link set <tap> master <bridge>`).
5. Exits 0.

`delete`:
1. Brings the tap down.
2. Calls `TUNSETIFF` to remove it (or `ip link del <tapname>`).
3. Exits 0.

The binary deliberately has no other functionality. It is narrow, auditable, and safe to `setcap`.

### 2. `tap_linux.go` (rewrite)

Replace all slirp4netns logic with:

- `realSetupTAP(tapName, bridge string) error` — invokes `nexus-tap-helper create <tapName> nexusbr0`
- `realTeardownTAP(tapName string)` — invokes `nexus-tap-helper delete <tapName>`
- TAP name per VM: `nexus-` + first 9 chars of `workspaceID` (max 15 chars total, Linux `IFNAMSIZ` limit)
- Remove all slirp4netns constants, `startSlirp4netns`, `checkSlirp4netns`
- Remove `slirpStartFunc` variable
- Keep `tapSetupFunc` / `tapTeardownFunc` var pattern for test mocking

**Network constants:**

| Constant | Value |
|----------|-------|
| `bridgeName` | `nexusbr0` |
| `bridgeGatewayIP` | `172.26.0.1` |
| `guestSubnetCIDR` | `172.26.0.0/16` |

Guest IPs are assigned dynamically by DHCP — no static allocation in nexus code.

### 3. `manager.go` (simplify)

- Remove `unshare --net` wrapper — Firecracker spawned with `exec.Command(m.config.FirecrackerBin, args...)` directly.
- Remove `SlirpProcess` field from `Instance` struct.
- Remove `slirpStartFunc` variable and all slirp startup/teardown logic.
- TAP setup before spawn, teardown in `Stop()`.
- Firecracker network interface API call: `PUT /network-interfaces/eth0` with `{ "host_dev_name": "<tapName>" }`.

**Boot args** change:
- Remove `ip=...` kernel cmdline parameter (static IP removed; DHCP handles it).
- Add `dhcp` or rely on the init script to run `udhcpc -i eth0`.

### 4. Guest DHCP

A DHCP server runs on the bridge host IP. Options:

- **dnsmasq on host** (preferred): `dnsmasq --interface=nexusbr0 --dhcp-range=172.26.1.0,172.26.254.255,1h --no-daemon`. Started as a systemd service or launched by nexus on first VM spawn.
- **Fallback**: The Firecracker agent's `setupDNS()` already runs at PID1 boot; extend it to also run `udhcpc` before DNS setup.

The guest needs an IP before the agent starts — so `udhcpc` must run early in the guest init, before the vsock agent binds.

### 5. systemd-networkd bridge config (one-time setup)

**`/etc/systemd/network/10-nexusbr0.netdev`:**
```ini
[NetDev]
Name=nexusbr0
Kind=bridge
```

**`/etc/systemd/network/11-nexusbr0.network`:**
```ini
[Match]
Name=nexusbr0

[Network]
Address=172.26.0.1/16
IPForward=yes
IPMasquerade=ipv4
```

**`/etc/systemd/network/12-nexus-tap.network`** (auto-attach new taps):
```ini
[Match]
Name=nexus-*

[Network]
Bridge=nexusbr0
```

After writing these files: `sudo systemctl enable --now systemd-networkd`.

This replaces all manual `ip link`, `ip addr`, `iptables`, and `sysctl` commands with a persistent declarative config. The bridge survives reboots. New taps matching `nexus-*` are auto-enslaved.

### 6. Doctor preflight

`validateFirecrackerHostPrerequisites` gains two new checks:

1. **Helper check**: `getcap /usr/local/bin/nexus-tap-helper` must include `cap_net_admin`.
2. **Bridge check**: `ip link show nexusbr0` must exist and be UP.

On failure, print exact setup commands and abort with a helpful error message:

```
nexus doctor: firecracker bridge networking not set up.
Run the following once to set it up:

  # Install tap helper
  sudo cp /path/to/nexus-tap-helper /usr/local/bin/nexus-tap-helper
  sudo setcap cap_net_admin=ep /usr/local/bin/nexus-tap-helper

  # Configure bridge (persistent via systemd-networkd)
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
```

No auto-setup. The user runs these commands once. This is the same model as Docker's initial setup.

---

## Guest init change

The Firecracker guest agent (`packages/nexus/cmd/nexus-firecracker-agent/main.go`) must run `udhcpc -i eth0 -n -q` **before** `setupDNS()`. This acquires an IP from the bridge DHCP server, enabling the network stack before the vsock agent listens.

---

## Test changes

- `tap_linux_integration_test.go`: remove `TestStartSlirp4netnsReady`, add `TestSetupTAP` (calls helper binary if present, skips if not)
- `manager_test.go`: replace `slirpStartFunc` mock with `tapSetupFunc` / `tapTeardownFunc` mocks (already the existing interface); remove `slirpGuestIP`/`slirpGatewayIP` boot arg checks
- `driver_test.go`: no changes expected

---

## Out of scope

- macOS support (bridge networking is Linux-only; macOS path will use a different mechanism, TBD)
- Dynamic dnsmasq lifecycle (v1: user starts dnsmasq manually or via systemd; nexus does not manage it)
- IPv6

---

## Success criteria

1. `go build ./...` — zero errors
2. `go test ./...` — all tests pass
3. `nexus doctor` — runs without `sudo`, bootstrap VM gets internet access, readiness probe passes
4. `nexus-tap-helper` has no CAP_NET_ADMIN at the binary level *before* `setcap`; has it *after*
5. Bridge survives reboot (systemd-networkd)
