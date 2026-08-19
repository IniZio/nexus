---
title: "Execution substrate"
description: "Cloud Hypervisor, the vsock transport, networking, and resource limits"
---

# Execution substrate

> nexus3 owns zero VMM code — it drives Cloud Hypervisor over a REST-on-unix-socket API.

Workloads run in Cloud Hypervisor (CH) microVMs on Linux. All substrate-specific code lives behind the `driver` seam; nothing above it knows which VMM is in use.

```sh
nexus3 doctor   # report substrate availability and capability check results
```

## The `driver` seam

`internal/core/driver` is the single abstraction point between the nexus3 core and the VMM.

| Method | Description |
|--------|-------------|
| `Start` | Boot a new VM from an image and configuration. |
| `Stop` | Graceful shutdown (VMShutdown + VMMShutdown + SIGKILL fallback). |
| `Pause` | Suspend VM execution; memory stays in host RAM. |
| `Resume` | Resume a paused VM. |
| `TakeSnapshot` | Write VM memory state to disk (VM must be paused). |
| `ForkFrom` | Restore a snapshot into one new independent running VM. Call once per child. |
| `DialGuest` | Transport primitive — open a vsock-backed `net.Conn` to the guest agent. |
| `RunState` | Report actual VMM run state (running vs. paused), not just liveness. |
| Network hook | Intercept and route guest egress. |

`RunState` is load-bearing: crash recovery must distinguish a running parent from a paused one to decide whether to apply `TriggerSubstrateLost`.

### macOS

A macOS driver (`nexus3-vzd`, Swift, using Apple's Virtualization.framework) <Badge type="info" text="backlogged" />. The seam exists to keep it re-addable without touching the core. All capabilities described here are Linux/CH. See [Snapshots and fork](snapshots-and-fork.md) for platform cost differences.

Validated findings for when this backlog item is scheduled:

- **Entitlement:** exactly one key — `com.apple.security.virtualization`. Ad-hoc signing is sufficient.
- **VZ fork tier:** Virtualization.framework save/restore costs provisioned-size, not working-set. The bar (seconds, 2–3 branches) holds on macOS but the cost is higher.
- **Data path:** SCM_RIGHTS fd-passing from `nexus3-vzd` to the host process; dup-after-receive is required.
- **Guest kernel:** PCIe-virtio (not MMIO) required for Virtualization.framework.
- **Egress:** tun device in the VZ guest; same policy contract as Linux, different hook implementation.
- **Listener survival:** a guest vsock `LISTEN` socket survives a cross-process VZ save/restore. The agent must not rebuild its guest listeners on reattach — this rule applies on Linux too.
- **Supervision:** one `nexus3-vzd` process per sandbox VM, tied to the daemon process lifetime.

## Cloud Hypervisor (Linux)

### VM configuration

Each sandbox VM is created with:

- A root disk: a CoW sparse ext4 image forked from the base image.
- **Source mounts**: one virtiofs device per declared `--mount <host-path>:<guest-path>`, providing a live bidirectional view of the host directory.
- **Shadow disks** <Badge type="danger" text="not built" />: virtio-blk ext4 images that shadow write-heavy directories (`node_modules`, `.next`, `target`, `dist`) inside the mount, keeping virtiofs metadata cost off the hot build paths.
- A TAP-based network interface in its own network namespace.
- A vsock device (CID allocated per sandbox) for host↔guest communication.
- Optional `/dev/kvm` passthrough for nested virtualisation (opt-in via `--nested`, off by default).

### Snapshot mechanics

CH snapshots a **paused** VM by writing memory to disk:

- **CH copies snapshot memory into per-VM anonymous memory.** There is no page-sharing between sibling VMs. Each `nexus3 fork` call creates one independent copy, consuming its own host RAM.
- **Restore uses daemon-mode restore**, not the `--restore` CLI flag.
- **Concurrent restores are supported** by CH.

### Snapshot integrity

CH has no integrity check. A truncated `memory-ranges` file is indistinguishable from a sparse file, so CH restore silently zeroes missing RAM and returns success.

nexus3 owns integrity: a commit marker is written after the snapshot is complete, and a length assertion is checked before restore. See [Snapshots and fork](snapshots-and-fork.md).

## Guest kernel

The guest kernel is a custom Linux build shipped per-arch alongside the nexus3 binary. Key enabled features:

- `CONFIG_BRIDGE` + netfilter — required for Docker networking inside the guest.
- `CONFIG_VIRTIO_*` — virtio-blk (disks), virtio-net (network), virtio-balloon (memory resize).
- `CONFIG_VIRTIO_FS` (virtiofs) — required for live source mounts.
- Balloon free-page reporting — lets the host reclaim guest idle pages.

The kernel source and pinned config are in `scripts/kernel/`. The build is reproducible from source.

## `DialGuest` — the transport primitive

`DialGuest(sandboxID)` returns a `net.Conn` over vsock to the guest agent. It is the only transport primitive exposed by the driver. All host→guest communication — gRPC control-plane calls and data-plane multiplexed connections — routes through vsock connections opened via `DialGuest`.

### vsock port assignment

| Port | Use |
|------|-----|
| Fixed control port | gRPC AgentService (control plane) |
| Dynamic data ports | One multiplexed clawk-framed connection per session (data plane) |
| Governor telemetry port | Resize telemetry stream (pressure stall, disk usage, memory stats) |

The host always dials the guest; the guest never dials back.

## Network

Each sandbox runs in its own Linux network namespace. The guest gets a static IP (`192.168.127.2/24`); no DHCP client is required in the guest.

Outbound traffic is intercepted by a per-sandbox **egress perimeter** running on the host: a lightweight proxy that enforces the `AllowedHosts` policy and provides TLS MITM for credential injection. The perimeter is implemented by a detached **supervisor process** that survives CLI exit.

### Network hook primitive

The driver exposes a **network hook** that intercepts guest egress at the TAP level. The supervisor uses this hook to implement the egress perimeter without requiring root privileges inside the guest.

## Resource limits

Resource limits (RAM, vCPU, disk) are set at creation time via `--memory`, `--vcpus`, `--memory-max`, `--vcpus-max`, and `--disk-max` on `nexus3 create`, and adjusted at runtime by the [resource governor](lifecycle-states.md#resource-governor). <Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.
