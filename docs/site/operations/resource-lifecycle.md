---
title: "Resource Lifecycle"
description: "Intent journaling, reaper classification, and reclaiming disk and network resources"
---

# Resource Lifecycle

> nexus3 creates, journals, and reaps host resources deterministically — orphans left by a crash are recoverable without operator intervention.

Every disk and socket resource is journaled before it is materialized. The reaper classifies survivors after a crash and removes orphans. Kernel-owned network resources are reclaimed automatically.

```sh
nexus3 reap           # report orphaned resources (dry-run)
nexus3 reap --apply   # delete orphans
```

## Resource kinds

| Kind | Path pattern | Owner key | Reclamation |
|---|---|---|---|
| `disk_raw` | `<stateRoot>/disks/<ULID>-raw.ext4` | full ULID | reap |
| `disk_workspace` | `<stateRoot>/disks/<ULID>-workspace.ext4` | full ULID | reap |
| `disk_shadow` | `<stateRoot>/disks/<handle>-<ULID>.shadow.ext4` | sandbox handle | reap (handle correlation) |
| `create_intent` | `<stateRoot>/disks/<ULID>.create-intent.json` | full ULID | reap |
| `socket_api` | `<socketDir>/<ULID>.sock` | full ULID | reap |
| `socket_vsock` / `socket_iid` | `<socketDir>/…` | full ULID | reap |
| `builder_supervisor` | `<stateRoot>/builder/…` | full ULID | reap (`os.RemoveAll`) |
| TAP / bridge / netns | `nx3g-<prefix>`, `nx3h-<prefix>`, `nx3b-<prefix>` | 5-byte ULID prefix | **kernel** (auto) |

TAP interfaces, bridge, and the network namespace are in-kernel resources, named with the first 10 hex characters of the sandbox ULID. They are auto-reclaimed by the kernel when the Cloud Hypervisor process group dies — even under SIGKILL. The reaper does not target them; for correlation purposes, enumerate `ip link` entries matching `nx3[ghb]-<hex>` and strip the prefix.

### Mounts are host-owned; nexus3 does not reclaim them <Badge type="danger" text="not built" />

When a sandbox is created with a `-v` argument, nexus3 binds a host directory into the guest via virtiofs. That directory is **not a nexus3-managed resource** — it is owned by whoever created it (typically the orchestrator, which creates a `git worktree` per sandbox). `nexus3 rm` removes the sandbox record, shadow disks, sockets, and the raw OS disk. <Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox remove`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping. It does **not** delete the mounted host directory. The orchestrator is responsible for cleaning up the worktree it created (e.g. `git worktree remove`).

Shadow disks are per-sandbox virtio-blk images that cover write-heavy paths inside the mount (`node_modules`, `.next`, `target`, `dist`). These are nexus3-managed and **are** removed by `nexus3 rm`.

## Creation: intent-before-materialize

Every disk creation (raw OS disk, shadow disks) is journaled before the disk is materialized. Mounted worktrees are not disk artifacts — no intent record is written for them. <Badge type="danger" text="not built" /> The write sequence in `writeCreateIntent` (`internal/core/service/intent.go`):

```
os.OpenFile(intentPath, O_WRONLY|O_CREATE|O_TRUNC, 0o600)
f.Write(data)
f.Sync()      // data survives ext4 journal replay
f.Close()
dir.Sync()    // directory entry survives journal replay
```

Both syncs are load-bearing:

- **File sync** flushes data to the storage medium. Without it, a power loss before writeback drops the bytes.
- **Directory sync** flushes the directory entry. On Linux, `fsync` on a file does not flush its parent directory. Without `dir.Sync()`, after power loss the file's inode can survive while the directory entry is absent, making the intent invisible to the reaper's `os.ReadDir` scan.

The intent must be durable **before** the `.raw` disk is created. `create.go` enforces this ordering: `writeCreateIntent` → `cowExt4`.

### Unverified durability residuals

The following properties are **not exercised by the test suite** and require additional infrastructure to verify:

| Residual | What is needed |
|---|---|
| True power-loss durability | `dm-flakey`, fault-injecting filesystem, or hardware power-cycle |
| Directory-entry survival after crash-replay | Write intent, trigger kernel panic, replay journal, assert entry visible |
| Atomicity of the directory update | Same as above |

The process-kill test (`R3`) confirms the code path executes but is **not a power-loss proxy** — it does not exercise the storage driver's fsync barrier.

## Reap

`nexus3 reap` reclaims orphaned host resources. It is **always a dry-run by default**; nothing is deleted without `--apply`.

```sh
nexus3 reap           # report only
nexus3 reap --apply   # delete orphans
```

### How reap works

`Reap` (`internal/core/service/reap.go`) enumerates resources by calling `ResourceIndex.List()`, which **scans the filesystem directly and never reads the record store**. It then loads all store records and classifies each filesystem resource independently.

### Liveness gate (three-way)

For each ULID-keyed resource that has no store record, the reaper runs three checks. **Ambiguity always resolves to KEEP.**

```
1. /proc/*/cmdline scan
   - ULID found       → LIVE (keep)
   - scan incomplete  → AMBIGUOUS → KEEP
   - not found        → continue

2. API socket probe (derived from socketDir/<ULID>.sock)
   - socket responsive → LIVE (keep)
   - ambiguous         → KEEP
   - unresponsive      → continue

3. All checks definitive-dead → ORPHAN (reclaimable)
```

Truncated `/proc/<pid>/cmdline` reads (beyond 512 KiB) are treated as ambiguous because the ULID might appear past the cutoff. EACCES and ENOENT on a cmdline file are ambiguous because the process may have been our target and exited mid-check.

### Classification summary

| Status | Condition | Action |
|---|---|---|
| `owned` | Store record exists | Skip (stale-record cleanup is `recover`'s job) |
| `live` | No record, but liveness check found the process or responsive socket | Skip |
| `orphan` | No record, all liveness checks definitive-dead | Reclaimable; deleted when `--apply` |

### Shadow disk classification

Shadow disks (`KindDiskShadow`) use handle-based correlation rather than the ULID liveness gate:

- **Legacy format** (`.shadow.ext4`, no embedded handle): unconditionally orphan.
- **B1 format**: owned when the handle matches a live sandbox record; orphan otherwise.

### What reap does not touch

- TAP / bridge / netns (kernel-owned; auto-reclaimed)
- The record store itself (that is `recover`'s domain)
- Any resource not matched by `ResourceIndex.List()`'s filename patterns

## Recovery

`recover` (`internal/core/service/`) uses the record store as its **sole universe**. It finds records that have no corresponding running process and attempts substrate-level recovery.

**Scoped exception**: netns, TAP, and bridge are in-kernel and auto-reclaimed when the Cloud Hypervisor process group dies even under SIGKILL. For those three, the kernel is the handle — `recover` does not need to track them explicitly.

## Disk preflight

All `nexus3 create` entry points run a disk-space preflight before any expensive work (`CheckDiskSpace` in `internal/core/service/preflight.go`). The preflight:

- Measures existing sandbox disks (raw and shadow) using `stat(2).Blocks * 512` (allocated bytes, not apparent size — sparse ext4 images have inflated apparent sizes).
- Projects `count × per-sandbox-estimate` against the host's available bytes (`Bavail × Bsize` from `statfs(2)`).
- Returns `ErrInsufficientDisk` if the projection exceeds available space.

The preflight covers nexus3-managed disks only. Mounted host directories are outside the estimate — the orchestrator is responsible for ensuring the host worktree fits the available disk budget. <Badge type="danger" text="not built" />

The per-sandbox estimate defaults to ~4.57 GiB (measured from a real pilot sandbox) when no existing sandbox disks are present to sample.

## Kernel preflight

All sandbox-creation entry points (`nexus3 create`, `nexus3 run`, `nexus3 orca`) call `resolveKernelPath()` before any store or VM setup. If `NEXUS3_KERNEL_PATH` is unset or the file does not exist, creation fails immediately with a legible error. Without this preflight, Cloud Hypervisor's own error is the opaque `"Cannot open kernel file"`.
