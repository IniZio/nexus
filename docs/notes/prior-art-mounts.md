# Prior Art: Live Host Directory Mounts in microVM Sandboxes

**Scope:** How comparable tools expose live host directory mounts to a guest VM.  
**Central question:** What happens when a running VM with a host mount is forked or snapshotted?  
**Status of nexus3 live mounts:** UNBUILT as of 2026-08-18. This document describes other systems only.

---

## 1. microsandbox (primary reference)

**Source:** https://docs.microsandbox.dev / https://github.com/superradcompany/microsandbox  
**Transport:** virtiofs via an in-process passthroughfs backend (`crates/filesystem/lib/backends/passthroughfs/`)

### 1.1 Mount syntax

microsandbox has four mount types. The one matching a live host directory is the **bind mount**:

```bash
# CLI — bind mount a host directory into the guest
msb create python --name dev \
  --mount-dir ./src:/app/src:ro,noexec \
  --mount-dir ./data:/app/data
```

Flag reference:

| Flag | Source | Key options |
|------|--------|-------------|
| `--mount-dir` | Host directory (virtiofs) | `ro`, `rw`, `noexec`, `nosuid`, `nodev`, `stat-virt=strict\|relaxed\|off`, `host-perms=private\|mirror` |
| `--mount-file` | Single host file (virtiofs) | same as above |
| `--mount-named` | Named volume (`kind=dir` = virtiofs; `kind=disk` = virtio-blk) | `ro`, `rw`, `kind=dir\|disk`, `quota=<size>` for dir, `size=<size>` for disk |
| `--mount-disk` | Host disk image (virtio-blk) | `ro`, `rw`, `format=raw\|qcow2\|vmdk`, `fstype=<type>` |

The named-volume primitive maps to two internal `VolumeKind` variants
(`packages/microsandbox-types/rust/lib/domain.rs:282-289`):

```rust
pub enum VolumeKind {
    /// Directory-backed named volume mounted through virtiofs.
    Directory,
    /// Raw ext4 disk-image named volume mounted through virtio-blk.
    Disk,
}
```

SDK equivalents (Rust shown; TypeScript/Python/Go follow the same shape):

```rust
let sb = Sandbox::builder("dev")
    .image("python")
    .volume("/app/src", |v| v.bind("./src").readonly().noexec())
    .volume("/app/data", |v| v.bind("./data"))
    .create().await?;
```

Named volume (idempotent create-and-mount):

```bash
msb create python --name dev --mount-named mydata:/data
# disk-backed variant:
msb create python --name dev --mount-named cache:/cache:kind=disk,size=10G
```

### 1.2 Read-only

CLI: `:ro` in the options block (`--mount-dir ./src:/app/src:ro`).  
SDK: `.readonly()` method; Python: `readonly=True` kwarg.  
The backend applies a defence-in-depth readonly check at the operation layer
(`create_ops.rs`, lines 59, 168, 229, 322, 437) because a privileged guest
could otherwise remount read-write from inside.

### 1.3 Fork / snapshot semantics

**Snapshots** are disk-only:

> "Snapshots are disk-only and require a sandbox that is not running.
> Stopped and crashed sandboxes can be snapshotted; running, draining,
> and paused sandboxes are rejected."

What a snapshot captures vs. omits:

| Captured | NOT captured |
|---|---|
| Writable filesystem changes | Memory contents |
| Pinned image identity | Running processes |
| Optional labels and integrity hash | Network state |

Host directory mounts appear in neither column — they are **not part of the snapshot artifact at all**. The snapshot captures only the guest rootfs layer. A sandbox booted from a snapshot must re-specify its `--mount-dir` / `--mount-named` flags independently; the snapshot does not carry them.

On the cloud variant, the docs note: "use a named volume for state that must survive sandbox replacement" — confirming that snapshot does not include volume state.

**Fork:** microsandbox has no fork primitive. There is no `msb fork` command or SDK equivalent in the public docs or CLI reference. The project disposes of sandboxes rather than branching them.

### 1.4 .git handling

No special-casing. No path blocklist. The passthroughfs backend applies generic
containment: `PassthroughConfig.no_symlink_root` refuses `..` traversal and symlink
escapes regardless of directory name. The test suite is
`tests/test_root_containment.rs`.

### 1.5 Multiple-VM sharing

| Volume kind | Concurrent RW | Concurrent RO |
|---|---|---|
| `dir` (virtiofs) | Unlimited | Unlimited |
| `disk` (virtio-blk) | 1 (exclusive) | Unlimited |

A second read-write attach of a `kind=disk` volume returns an error. No guard
exists for `kind=dir`; concurrent writers conflict at the filesystem layer and
microsandbox does not arbitrate.

---

## 2. gondolin (earendil-works/gondolin)

**Source:** https://github.com/earendil-works/gondolin /
https://earendil-works.github.io/gondolin/  
**Transport:** FUSE-backed virtual filesystem ("VFS providers") via QEMU; an experimental
libkrun backend also exists but has parity gaps.

### 2.1 Mount syntax

Gondolin exposes "programmable VFS providers" configured in the SDK (TypeScript/JS).
The `RealFSProvider` mounts a local host directory into the guest at a nominated
guest path. In the pi-gondolin extension (the canonical usage reference,
https://github.com/pasky/pi-gondolin), this is described as:

> "The `RealFSProvider` mounts your local working directory into the VM via a
> FUSE-backed virtual filesystem, so reads and writes are bidirectional."

The working directory is mounted at `/workspace` inside the VM. There is no
standalone CLI flag for directory mounts — mount configuration happens through
the SDK's `VfsProviders` option at VM creation time. There is no documented
`--mount-dir` equivalent in the CLI (`npx @earendil-works/gondolin bash` boots
with defaults).

### 2.2 Read-only

The pi-gondolin usage mounts read-write by default. The SDK VFS provider API
supports custom implementations, so read-only could be enforced by a custom
provider. No built-in `readonly` option was found in the public documentation
or README as of this writing.

### 2.3 Fork / snapshot semantics

**Snapshots (disk checkpoints):** Gondolin supports disk-only checkpoints
(`vm.checkpoint(<absolute qcow2 path>)` / `checkpoint.resume()`):

> "Gondolin does not provide full VM save/restore (capturing in-VM process
> state + RAM) today."

The disk checkpoint is a qcow2 backing image. Important limitation from the
official Limitations page:

> "Some guest paths are tmpfs-backed by design (e.g. /root, /tmp, /var/tmp,
> /var/cache, /var/log). Writes under those paths are not part of disk
> checkpoints."

VFS mounts (FUSE-backed) are **not captured in a checkpoint** — they are
session-level configuration provided at VM creation time, not stored in the
disk image. Resuming from a checkpoint does not reproduce any VFS mount; the
caller must re-specify it.

**Fork:** Gondolin has no fork primitive. Tracking issue #8 covers full
VM save/restore (memory snapshots), but that is separate from cloning a running
VM's state to a new instance.

### 2.4 .git handling

No special-casing documented. The FUSE backend operates at the filesystem layer
without path awareness.

### 2.5 Multiple-VM sharing

Not documented. Because the transport is FUSE (userspace on the host), multiple
VMs can share the same host directory if the SDK is invoked that way; there is
no built-in guard. Concurrent writers would conflict at the host filesystem
level.

---

## 3. crabbox — NOT FOUND; Firecracker + virtiofs substituted

**Finding:** "crabbox" as a standalone microVM or sandbox product is not
findable. The one GitHub search result (`zozo123/odysseus-crabbox-demo`) is a
demo that runs another tool (islo.dev) and uses "crabbox" as a proper noun
in its name, not as the tool being documented. No project page, documentation,
or source repository named "crabbox" was located. This section documents the
raw **Firecracker + virtiofs** combination, which is the underlying platform
that tools like microsandbox build on. It is labeled as a substitution.

**Source:** https://github.com/firecracker-microvm/firecracker /
https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md

### 3.1 Mount syntax

Firecracker does not ship a virtiofs device natively. virtiofs in Firecracker
requires running a separate `virtiofsd` (or equivalent) host-side daemon and
wiring it via the Firecracker API as a virtio-fs device. The configuration is
machine-config JSON; there is no built-in CLI flag. At the API level a
virtio-fs tag and socket path are specified; the guest mounts with:

```bash
mount -t virtiofs <tag> /guest/path
```

There is no built-in read-only enforcement at the Firecracker API layer;
the guest-side `mount -o ro` controls it, or `virtiofsd` can be started
with read-only export options.

### 3.2 Read-only

Enforced via `virtiofsd` startup options (`--shared-dir <path>:ro`) or
by the guest-side mount options. Firecracker itself has no opinion on
directory mount permissions.

### 3.3 Fork / snapshot semantics

Firecracker does support full VM snapshots (memory + disk state). The snapshot
captures the guest VM state including the list of virtio-fs devices by
configuration — but **not** the contents of the host directory behind the
device. The Limitations section of the Firecracker snapshot docs does not
enumerate virtiofs explicitly; however:

- After restore, the guest VM re-opens the virtiofs socket and the daemon
  must be running with the same host path.
- If the host directory changed between snapshot and restore, the guest's
  VFS page cache is stale relative to the real host contents.
- For a **clone** (restoring the same snapshot into two simultaneously running
  VMs), both VMs write to the same host directory concurrently through their
  respective virtiofsd instances. Firecracker has no guard for this; the
  application layer must coordinate (or accept conflicts).

Network connectivity is explicitly noted as "not guaranteed to be preserved
after resume" — reconnection must be handled by the caller.

### 3.4 .git handling

No special-casing at the Firecracker or virtiofsd layer.

### 3.5 Multiple-VM sharing

No built-in guard. Multiple running VMs can each attach a virtiofsd pointing
to the same host directory. Concurrent writes conflict at the host filesystem
level. This is equivalent to two Docker containers mounting the same host path
read-write.

---

## 4. What nexus3 should copy, what it should not, and why

### Copy

**4.1 Separate bind-mount and named-volume primitives (microsandbox pattern).**  
`--mount-dir` for live source trees and `--mount-named` for managed volumes is
the cleaner split: the intent is legible at the call site. Conflating them into
one flag forces callers to reason about lifetime semantics.

**4.2 Read-only as a first-class option (microsandbox).**  
`:ro` in the options block is the established convention (Docker, microsandbox,
Lima). nexus3 should accept `ro` in the mount option string and enforce it at
the virtiofs layer, not only trust the guest to honour it.

**4.3 Disk checkpoint refusal on running VMs (microsandbox, gondolin).**  
Both tools refuse to snapshot a running sandbox. nexus3's ratified refusal to
snapshot or fork a sandbox that has live mounts (D-PD-53) is consistent with
this prior art and goes one step further: rather than snapshotting without the
mount (which leaves the snapshot ambiguously useful), nexus3 refuses the entire
operation. That is the correct position.

**4.4 Mounts are re-specified at boot, not stored in the snapshot (microsandbox,
gondolin).**  
Neither system serialises mount configuration into the snapshot artifact.
nexus3 should not attempt to do so either. A snapshot captures the guest disk;
the caller re-mounts at boot time with the same or a different source.

### Do not copy

**4.5 Unlimited concurrent RW on directory volumes (microsandbox `kind=dir` policy).**  
microsandbox allows unlimited concurrent rw mounts of the same named dir volume
and documents that conflicts are the caller's problem. For nexus3's use case —
agent workflows where multiple agents could fork-and-mount the same working tree
— silent concurrent-write conflicts are a correctness hazard (`.git/index.lock`
deadlocks, mid-write file observations). nexus3's fork refusal on mounts
(D-PD-53) is the right divergence.

**4.6 FUSE-backed VFS providers (gondolin pattern).**  
gondolin's programmable JavaScript VFS layer is flexible but introduces a
userspace IPC hop on every filesystem operation. nexus3 has already measured
virtiofs (in-kernel passthroughfs, same as microsandbox uses) and ruled it the
transport (D-PD-100). The FUSE intermediary adds latency for no benefit in
nexus3's workload.

**4.7 No snapshot support for mounts at all (gondolin).**  
Gondolin simply has no story for snapshotting a VM that has a VFS mount active.
nexus3 should be explicit about the refusal rather than silent about it — a
documented refusal (already ratified) is better than an undocumented gap.

---

## Sources

- microsandbox volumes docs: https://docs.microsandbox.dev/sandboxes/volumes.md
- microsandbox snapshots docs: https://docs.microsandbox.dev/sandboxes/snapshots.md
- microsandbox CLI volume commands: https://docs.microsandbox.dev/cli/volume-commands.md
- microsandbox Go SDK volumes: https://docs.microsandbox.dev/sdk/go/volumes.md
- microsandbox security filesystem: https://docs.microsandbox.dev/security/filesystem.md
- microsandbox GitHub: https://github.com/superradcompany/microsandbox
- gondolin GitHub: https://github.com/earendil-works/gondolin
- gondolin limitations: https://earendil-works.github.io/gondolin/limitations/
- pi-gondolin (gondolin + pi integration): https://github.com/pasky/pi-gondolin
- Firecracker snapshot support: https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md
