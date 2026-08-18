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
| `disk_raw` | `<stateRoot>/disks/<ULID>.raw` | full ULID | reap |
| `disk_workspace` | `<stateRoot>/disks/<ULID>-workspace.ext4` | full ULID | reap |
| `disk_shadow` | `<stateRoot>/disks/<safeHandle>.shadow.<name>.ext4` | sandbox handle | reap (handle correlation) |
| `create_intent` | `<stateRoot>/disks/<ULID>.create-intent.json` | full ULID | reap |
| `socket_api` | `<socketDir>/<ULID>.sock` | full ULID | reap |
| `socket_vsock` / `socket_iid` | `<socketDir>/…` | full ULID | reap |
| `builder_supervisor` | `<stateRoot>/builder/…` | full ULID | reap (`os.RemoveAll`) |
| TAP / bridge / netns | `nx3g-<prefix>`, `nx3h-<prefix>`, `nx3b-<prefix>` | 5-byte ULID prefix | **kernel** (auto) |
| `volume_disk` | `<stateRoot>/volumes/<name>/disk.ext4` | volume name | **user** — never reap |
| `volume_dir` | `<stateRoot>/volumes/<name>/data/` | volume name | **user** — never reap |

TAP interfaces, bridge, and the network namespace are in-kernel resources, named with the first 10 hex characters of the sandbox ULID. They are auto-reclaimed by the kernel when the Cloud Hypervisor process group dies — even under SIGKILL. The reaper does not target them; for correlation purposes, enumerate `ip link` entries matching `nx3[ghb]-<hex>` and strip the prefix.

### Named volumes

Named volumes (`volume_disk` and `volume_dir`) are user-owned resources that live in `<stateRoot>/volumes/<name>/`. They are **structurally excluded** from the reaper: `ResourceIndex.List()` scans only `<stateRoot>/disks/` and `<stateRoot>/sockets/`; it has no visibility into `<stateRoot>/volumes/`. A guard test (`TestReap_NeverTouchesVolumes`) fails if that scan boundary ever widens.

`nexus3 rm` detaches a sandbox's named volumes but **never** deletes their backing files (D-PD-87). The volume persists until the user explicitly removes it with `nexus3 volume rm <name>` or reclaims detached volumes with `nexus3 volume prune`.

See [Volume commands](/cli/volume-commands) for the full volume lifecycle.

## Creation: intent-before-materialize

Every disk creation (raw OS disk, shadow disks) is journaled before the disk is materialized. Named volume backing files are not subject to the intent system — they are created inside the volume store under exclusive per-volume locks, not via the disks/ intent path. The write sequence in `writeCreateIntent` (`internal/core/service/intent.go`):

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

### In-flight creates (intent lease)

A create materializes its disk **before** it commits the store record — a multi-second copy for a multi-GiB image. During that window the disk has no record, and no process carries the ULID in its command line (the creator is the `nexus3` CLI itself; the VMM is not launched until afterwards), so the `/proc` gate below cannot see it. The reaper would classify a live create's disk as an orphan and delete it.

The create-intent file therefore carries an exclusive `flock(2)` **lease**, taken before the intent becomes discoverable and released only after the record is committed:

- **lease held** → a create is in flight → keep.
- **lease free** → the creator is gone (crashed, killed, or the host rebooted) → classify normally.
- **intent unreadable** → cannot rule out a live creator → keep, and say so distinctly in the report. This is the one keep that does **not** expire on its own and needs an operator to inspect the file.

The kernel releases `flock` when the holding process dies — including on `SIGKILL` — and a reboot releases everything, so a dead creator can never block reclamation.

The reaper probes leases **before** it snapshots the store records. The two observations are not atomic, and this is the order that makes them safe together: a lease is released only after the record is committed, so a create that releases between the two observations is guaranteed to appear in the record snapshot taken afterwards. Listing records first inverts that guarantee and reopens the window.

Both creation paths take a lease: `nexus3 create`, and `nexus3 fork` for each child's `<childID>.raw` and for any shadow disk copies created during fork. Fork children's workspace disks are named `<childID>-workspace.ext4` (ULID-keyed) and handled by the standard liveness gate. Named volumes of any kind are excluded from fork children: `nexus3 fork` refuses when the parent has any attached named volume (D-PD-96, TBR-PD-15 pending design). <Badge type="warning" text="partial" />

Two residuals remain on both create and fork. `ResourceIndex.List()` is a `readdir`, not an atomic directory snapshot, so a scan can in principle observe a `.raw` dirent while having already passed the slot where its `.create-intent.json` landed, yielding a disk with no lease to probe. And an intent file that cannot be read at all (for example one left mode-0600 by another uid) keeps its sandbox's disks indefinitely, as described above.

### Liveness gate

For each ULID-keyed resource that has no store record and no held lease, the reaper runs two further checks before reaching a verdict. **Ambiguity always resolves to KEEP.**

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
| `live` | No record, but a held intent lease, a matching process, or a responsive socket was found | Skip |
| `orphan` | No record, all liveness checks definitive-dead | Reclaimable; deleted when `--apply` |

### Shadow disk classification

Shadow disks (`KindDiskShadow`) use handle-based correlation rather than the ULID liveness gate:

- **Legacy format** (`.shadow.ext4`, no embedded handle): unconditionally orphan.
- **B1 format**: owned when the handle matches a live sandbox record; orphan otherwise.

Two shadow-disk hazards follow from handle correlation, and they have been addressed:

- **On create — windowed.** A shadow disk is materialized before the sandbox record that will own it. A flock lease on the create-intent artifact now prevents the reaper from acting during this window (D-PD-73): the lease is held until the record commits; the reaper probes leases before loading records so a create that releases between the two observations is guaranteed to be visible in the record snapshot. The window is closed.

- **On fork.** Forked children's workspace disks are named `<childID>-workspace.ext4` (D-PD-80(b)) and are ULID-keyed, so they are owned by the standard liveness gate and never misclassified. Shadow disk copies created during fork are protected by the same flock lease scheme during the create window (D-PD-74). After the window closes, the fork correlation hazard remains for shadow disks named `<childID>-<parentSafeHandle>.shadow.<name>.ext4`: `ShadowDiskSafeHandle` yields `<childID>-<parentSafeHandle>`, which cannot match any live sandbox handle. Such copies are classified orphan permanently once the fork create window closes.

The recommended parallel-dev approach avoids this residual entirely: use `--mount-named kind=disk` volumes instead of shadow disks. Named volumes are excluded from fork children (never copied), so no shadow disk copies are created and no residual correlation hazard arises.

### What reap does not touch

- TAP / bridge / netns (kernel-owned; auto-reclaimed)
- The record store itself (that is `recover`'s domain)
- Any resource not matched by `ResourceIndex.List()`'s filename patterns
- The `<stateRoot>/volumes/` directory — structurally excluded; named volumes are user-owned

## Recovery

`recover` (`internal/core/service/`) uses the record store as its **sole universe**. It finds records that have no corresponding running process and attempts substrate-level recovery.

**Scoped exception**: netns, TAP, and bridge are in-kernel and auto-reclaimed when the Cloud Hypervisor process group dies even under SIGKILL. For those three, the kernel is the handle — `recover` does not need to track them explicitly.

## Disk preflight

All `nexus3 create` entry points run a disk-space preflight before any expensive work (`CheckDiskSpace` in `internal/core/service/preflight.go`). The preflight:

- Measures existing sandbox disks (raw and shadow) using `stat(2).Blocks * 512` (allocated bytes, not apparent size — sparse ext4 images have inflated apparent sizes).
- Projects `count × per-sandbox-estimate` against the host's available bytes (`Bavail × Bsize` from `statfs(2)`).
- Returns `ErrInsufficientDisk` if the projection exceeds available space.

The preflight covers nexus3-managed disks only. Named volume backing files are outside the estimate — volume sizes are set explicitly at creation time and are the user's responsibility.

The per-sandbox estimate defaults to ~4.57 GiB (measured from a real pilot sandbox) when no existing sandbox disks are present to sample.

## Kernel preflight

All sandbox-creation entry points (`nexus3 create`, `nexus3 run`, `nexus3 orca`) call `resolveKernelPath()` before any store or VM setup. If `NEXUS3_KERNEL_PATH` is unset or the file does not exist, creation fails immediately with a legible error. Without this preflight, Cloud Hypervisor's own error is the opaque `"Cannot open kernel file"`.
