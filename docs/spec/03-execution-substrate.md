# 03 — Execution Substrate

*Purpose: the `driver` seam and the Linux execution tier — Cloud Hypervisor, the guest kernel, `DialGuest`, and the network-hook primitive. nexus3 owns zero VMM code.*

## The `driver` seam

`internal/core/driver` is the single seam between the homegrown core and the VMM. It presents a substrate-agnostic interface — boot, snapshot, fork, restore, pause, resume, stop, run-state interrogation, plus two transport primitives (`DialGuest`, the network hook) — and hides which VMM implements it. On Linux the implementation is **Cloud Hypervisor**. A macOS implementation (`nexus3-vzd`) is backlogged (doc 12); the seam exists to keep it re-addable without touching the core.

nexus3 **owns zero VMM code** (tickets 07, 09). Cloud Hypervisor is a vetted Apache-2.0 upstream; the driver drives it over its REST-on-unix-socket API and its restore mechanism.

## Cloud Hypervisor (Linux tier)

- **Snapshot / restore / fan-out.** CH snapshots a *paused* VM (memory written out) and restores it N times. Concurrent restores are supported. **CH copies snapshot memory into per-VM anonymous memory** (`fill_saved_regions()`); the UFFD `ondemand` path only *defers* that copy via `UFFDIO_COPY`. **There is no page-sharing between sibling VMs** — that is Firecracker's design, not CH's. The cost model is therefore host-RAM-bounded (doc 05).
- **Restore mode:** daemon-mode restore, **not** the `--restore` CLI flag (ticket 37 operational carry-forward).
- **Run state, not just liveness.** The driver must expose the VM's **actual run state** (running vs paused), not merely "is the process alive." Distinguishing a running parent from a paused one is exactly what crash-recovery's `snapshotting` case turns on (ticket 19 ruling 6 obligation; ticket 18 parallel obligation for macOS).
- **Snapshot integrity is nexus3's job, not CH's.** CH has **no integrity check at all**: a truncated `memory-ranges` file is indistinguishable from a sparse file (`lseek(SEEK_HOLE)` reports EOF as a hole), so CH restore silently zeroes missing RAM and returns success. nexus3 owns a commit marker + a length assertion (doc 05, ticket 40).

## Guest kernel

nexus3 **ships the guest kernel per-arch** (ticket 14):

- Pinned config, **no `CONFIG_MODULES`** (everything built in; no loadable modules in the guest).
- **PVH-header** kernel on the CH/Linux path (ticket 37 carry-forward).
- On the macOS/VZ path the kernel must instead be **PCIe-virtio** (`virtio_pci` / `virtio_fs` / `vsock` built in) because VZ drives virtio over PCIe and silently fails an MMIO-only kernel — a macOS-only constraint recorded in doc 12 (ticket 33). It does not affect the Linux kernel build.

## `DialGuest` — the transport primitive

`driver` owns **`DialGuest`**: the one vsock primitive that is **host-dials-guest**, uniform across substrates (CH's `CONNECT <port>` and, later, VZ's `connect(toPort:)`). It returns a generic `net.Conn`. `driver` owns no protocol on top of it — `agent` (doc 04) and `perimeter` (doc 08) supply the protocol.

Host-dials-guest was locked by ticket 16 (ratified 2026-08-04), reversing ticket 08's original guest-dials-host: it is the primitive uniform across both VMMs and removes a bootstrap chicken-and-egg. Consequences that ride on it:

- vsock ports are **per-VM, opaque, guest-allocated**; sibling port collisions are the expected correct state, so nexus3 ships **zero allocator code** (`bind(VMADDR_PORT_ANY)` + `getsockname()`, host dials a **fixed** control port).
- **Guest listeners survive restore** (verified on CH by reading the muxer; confirmed on VZ by ticket 33). The agent must **not** rebuild them on reattach. There is **no `TRANSPORT_RESET`** event; an established connection breaks with **`EPIPE`** (not `ECONNRESET`) at resume while the listen socket stays bound (ticket 37).
- The real collision is the **host UDS path**, not the guest port: siblings restored from one snapshot share the identical guest path, separated by a **per-clone mount namespace** giving distinct UDS inodes — verified achievable **unprivileged** (`unshare --user --map-root-user --mount`, ticket 37). Per-clone vsock path must be **≤108 bytes**.

## The network-hook primitive

`driver` also owns a **network-hook primitive**: it hands `perimeter` the raw substrate network attachment (a tap-fd on Linux; `VZFileHandleNetworkDeviceAttachment` on macOS) analogous to `DialGuest`. Above the fd, `perimeter` is platform-uniform (doc 08). `driver` absorbs the substrate asymmetry and stays ignorant of policy; it must not import `perimeter`.

## Capability-to-mechanism cost framing

The fork capability (snapshot-a-running-VM, restore N times) is **held on both platforms with identical semantics**; the difference is **cost, declared as a cost table not a feature matrix** (doc 05). On Linux a branch costs its working set, shrunk by CH `free_page_reporting`. The bar is **seconds, 2–3 branches** — deliberately not sub-second or 10+, which keeps the two-substrate substrate decision (ticket 07) closed and fits CH's copy-per-VM model.

---

*Sources: tickets 07 (substrate choice, own no VMM), 09 (Go core / driver seam), 16 (host-dials-guest, port allocation), 14 (guest kernel per-arch, no CONFIG_MODULES), 37 (daemon-mode restore, PVH, EPIPE, ≤108-byte path, unprivileged mount ns), 40 (CH has no integrity check), 19/18 (run-state interrogation), 13 (cost framing). Map Correction 127: CH copies, no CoW page-sharing.*
