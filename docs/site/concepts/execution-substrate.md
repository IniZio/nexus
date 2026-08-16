# Execution substrate

nexus3 runs workloads in Cloud Hypervisor (CH) microVMs on Linux. The substrate is hidden behind the `driver` seam, which provides a substrate-agnostic interface to the rest of the core.

**nexus3 owns zero VMM code.** It drives Cloud Hypervisor over its REST-on-unix-socket API.

## The `driver` seam

`internal/core/driver` is the single abstraction point between the homegrown core and the VMM. All substrate-specific code lives below this seam; nothing above it knows which VMM is in use.

The driver exposes:

| Method | Description |
|--------|-------------|
| `Start` | Boot a new VM from an image and configuration. |
| `Stop` | Graceful shutdown (VMShutdown + VMMShutdown + SIGKILL fallback). |
| `Pause` | Suspend VM execution; memory stays in host RAM. |
| `Resume` | Resume a paused VM. |
| `TakeSnapshot` | Write VM memory state to disk (VM must be paused). |
| `ForkFrom` | Restore a snapshot N times into N independent running VMs. |
| `DialGuest` | Transport primitive — open a vsock-backed net.Conn to the guest agent. |
| `RunState` | Report actual VMM run state (running vs. paused), not just liveness. |
| Network hook | Intercept and route guest egress. |

The `RunState` obligation is load-bearing: crash recovery must distinguish a running parent from a paused one to decide whether to apply `TriggerSubstrateLost`.

### macOS

A macOS driver (`nexus3-vzd`, Swift, using Apple's Virtualization.framework) is **backlogged**. The seam exists to keep it re-addable without touching the core. All capabilities described here are Linux/CH. See [Snapshots and fork](snapshots-and-fork.md) for platform cost differences.

The macOS design has been empirically validated on a Darwin arm64 test host (macOS 26.5.1,
Swift 6.3.1). Key validated findings, preserved here as implementation guidance for when the
backlog item is scheduled:

- **Entitlement:** exactly one key — `com.apple.security.virtualization`. Ad-hoc signing
  (`codesign -s - --entitlements`, no keychain) is sufficient. No other entitlement is needed.
- **VZ fork tier:** Virtualization.framework save/restore works but costs `provisioned-size`, not
  working-set. This is non-portable — the same capability costs working-set on CH. The bar
  (seconds, 2–3 branches) holds on macOS but the cost is higher.
- **Data path:** SCM_RIGHTS fd-passing from `nexus3-vzd` to the host process is preferred over
  unix socket relays; dup-after-receive is required.
- **Guest kernel:** PCIe-virtio (not MMIO) required for Virtualization.framework.
- **Egress:** tun device in the VZ guest (same policy contract as Linux; the perimeter hook
  differs by substrate but the allowlist semantics are identical).
- **Listener survival:** a guest vsock `LISTEN` socket survives a cross-process VZ save/restore
  — the pre-save host connection is closed by VZ across the cycle, but the listen socket
  survives. The agent must **not** rebuild its guest listeners on reattach; this rule applies on
  Linux too (the vsock server stays up across CLI exit and reattach).
- **Supervision:** one `nexus3-vzd` process per sandbox VM (retained pipe, VZ lifecycle tied to
  the daemon process lifetime).

## Cloud Hypervisor (Linux)

### VM configuration

Each sandbox VM is created with:

- A root disk: a CoW sparse ext4 image forked from the base image.
- Extra disks (`/dev/vdb`, etc.) for workspace mounts and shadow disks.
- A TAP-based network interface in its own network namespace.
- A vsock device (CID allocated per sandbox) for host↔guest communication.
- Optional `/dev/kvm` passthrough for nested virtualization (opt-in, off by default; CH nested KVM defaults to enabled-on-omit, which is a security trap — nexus3 passes explicit `false` unless the sandbox requests it).

### Snapshot mechanics

CH snapshots a **paused** VM by writing memory to disk. The key facts:

- **CH copies snapshot memory into per-VM anonymous memory** (`fill_saved_regions()`). The UFFD `ondemand` path only defers that copy via `UFFDIO_COPY`.
- **There is no page-sharing between sibling VMs.** nexus3 fork creates N independent copies, each consuming its own host RAM. The cost model is host-RAM-bounded.
- **Restore uses daemon-mode restore**, not the `--restore` CLI flag.
- **Concurrent restores are supported** by CH.

### Snapshot integrity

**CH has no integrity check.** A truncated `memory-ranges` file is indistinguishable from a sparse file (`lseek(SEEK_HOLE)` reports EOF as a hole), so CH restore silently zeroes missing RAM and returns success.

nexus3 owns integrity: a commit marker is written after the snapshot is complete, and a length assertion is checked before restore. See [Snapshots and fork](snapshots-and-fork.md).

## Guest kernel

The guest kernel is a custom Linux build shipped per-arch alongside the nexus3 binary. Key enabled features include:

- `CONFIG_BRIDGE` + netfilter — required for Docker networking inside the guest.
- `CONFIG_VIRTIO_*` — virtio-blk (disks), virtio-net (network), virtio-balloon (memory resize).
- Balloon free-page reporting — lets the host reclaim guest idle pages.

The kernel source and pinned config are in `scripts/kernel/`. The build is reproducible from source.

## `DialGuest` — the transport primitive

`DialGuest(sandboxID)` returns a `net.Conn` over vsock to the guest agent. It is the only transport primitive exposed by the driver. All host→guest communication — gRPC control-plane calls, data-plane multiplexed connections — is routed through vsock connections opened via `DialGuest`.

### vsock port assignment

| Port | Use |
|------|-----|
| Fixed control port | gRPC AgentService (control plane) |
| Dynamic data ports | One multiplexed clawk-framed connection per session (data plane) |
| Governor telemetry port | Resize telemetry stream (pressure stall, disk usage, memory stats) |

The host always dials the guest; the guest never dials back.

## Network

Each sandbox runs in its own Linux network namespace. The guest gets a static IP (`192.168.127.2/24`); no DHCP client is required in the guest.

Outbound traffic is intercepted by a per-sandbox **egress perimeter** running on the host: a lightweight proxy that enforces the `AllowedHosts` policy (from `Envelope.AllowedHosts`) and provides TLS MITM for credential injection. The perimeter is implemented by a detached **supervisor process** that survives CLI exit.

### Network hook primitive

The driver exposes a **network hook** that intercepts guest egress at the TAP level. The supervisor uses this hook to implement the egress perimeter without requiring root privileges inside the guest.

## Resource limits

Resource limits (RAM, vCPU, disk) are set at creation time via `vmcfg.Resolve` and adjusted at runtime by the [resource governor](lifecycle-states.md#resource-governor). No nexus3 command today exposes a raw `--memory` or `--vcpu` flag directly to the user; the governor owns those decisions.
