---
title: "Resource Lifecycle"
description: "Normative ownership and reclamation contract for all host resources nexus3 manages"
---

# Resource Lifecycle

> **Contract scope.** This page is the normative specification that R1/R2/R3/R4 slices are measured against. It defines which resources nexus3 owns, who must free them, and how orphans are detected and reclaimed. Where the current implementation does not yet satisfy a requirement, that requirement is badged; unbadged requirements are implemented and verified.

```sh
nexus3 reap           # report orphaned resources (dry-run)
nexus3 reap --apply   # delete orphans
```

## Normative requirements

The key words MUST, MUST NOT, SHOULD, and MAY carry RFC 2119 meaning throughout this page.

### Ownership and freeing

**RL-1** — nexus3 MUST classify every host resource it creates as either *system-owned* (lifecycle tied to a sandbox ULID) or *user-owned* (lifecycle governed exclusively by the user). System-owned resources are the reaper's domain; user-owned resources MUST NOT be touched by the reaper or by `nexus3 rm`.

**RL-2** — For every system-owned resource, a single component MUST be designated responsible for freeing it. Where that component is not yet identified the resource is unowned and the gap MUST be tracked as an open defect.

**RL-3** — Every system-owned resource that is **not** kernel-managed MUST carry a deterministic, parseable owner key. The primary key is the owning sandbox's ULID embedded in the on-disk path in a position `ResourceIndex.List()` can parse. A **supplementary correlation key** is also permitted where a resource is legitimately not ULID-keyed: shadow disks are keyed by the sandbox *handle* (`<safeHandle>.shadow.<name>.ext4`, `cli/shadowdisk.go:117`), and the reaper correlates them against live sandbox handles (`reap.go:213-216`). A resource carrying **neither** key is unclassifiable. Unclassifiable resources of a kind whose ownership can never be established — the legacy `*.shadow.ext4` format, which embeds no handle at all — are reclaimable (`reap.go:206-211`); for every other kind, an unresolved owner MUST resolve to KEEP under RL-9.

### Reap CLI

**RL-4** — `nexus3 reap` MUST be a no-op (dry run) by default. Deletion MUST require the explicit `--apply` flag. A reap invocation without `--apply` MUST print a human-readable report of what would be deleted and exit 0.

### Intent-before-materialize and the create lease

**RL-5** <Badge type="warning" text="partial" /> — Every disk creation MUST write a durable intent marker to `disks/<ULID>.create-intent.json` **before** the disk file is materialized. **This holds for the raw OS disk and the workspace disk only.** `writeCreateIntent` (`create.go:441`) runs ahead of `cowExt4`, and `createIntent` (`intent.go:48-60`) carries exactly two path fields, `DiskCopyPath` and `WorkspaceDiskPath`. **Shadow disks are never journaled at all**: they are materialized in the CLI at `cmd_sandbox.go:1251-1258`, before `CreateAndBoot` is entered at `cmd_sandbox.go:1344`, and no field of `createIntent` names them. This is the ordering half of the shadow-disk exposure; RL-17 is the reaper half. Both are open (TBD-PD-25). Named volume backing files are excluded from this requirement; they are created under per-volume exclusive locks in the volume store.

**RL-6** — The intent marker MUST carry an exclusive `flock(2)` lease that is acquired before the intent becomes visible on disk and is released **only after** the sandbox record is committed to the store. The kernel MUST release the lease when the holding process terminates for any reason, including SIGKILL. A reboot MUST release all leases.

**RL-7** — `Reap` MUST probe intent leases **before** it reads the sandbox record store. A create that releases its lease between the two observations MUST be guaranteed to appear in the subsequent record snapshot (because the release happens only after record commit). Reversing this order reopens the race and is prohibited.

### Liveness gate

**RL-8** — For each ULID-keyed resource that has no store record and no held intent lease, the reaper MUST apply the following two-stage liveness gate before reaching a verdict:

1. Scan `/proc/*/cmdline` for the ULID.
2. Probe the API socket derived from `<socketDir>/<ULID>.sock`.

**RL-9** — **Ambiguity MUST resolve to KEEP.** Any condition that does not yield a definitive *dead* verdict — including a partial `/proc` read, EACCES or ENOENT on a cmdline file, an unreadable intent file, or any other inconclusive signal — MUST leave the resource classified as live. An ambiguous keep MUST appear in the dry-run report with its reason, because it does not expire automatically and requires operator inspection.

### Volume structural exclusion

**RL-10** — `ResourceIndex.List()` MUST NOT scan `<stateRoot>/volumes/`. A test (`TestReap_NeverTouchesVolumes`) MUST fail if the scan boundary ever widens to include volumes. This exclusion is structural, not policy: named volumes are user-owned (RL-1) and MUST survive reap even if the reaper were to encounter them.

### IID socket reclamation <Badge type="warning" text="partial" />

**RL-11** — The IID socket (`<socketDir>/<ULID>.iid`) MUST be removed as part of sandbox teardown. The cleanup site is `clearState()` at `driver.go:484`, which calls `os.Remove(d.iidPath(id))` on driver stop alongside the API socket and vsock. `ResourceIndex` enumerates `.iid` files as `KindSocketIID` (`resource_index.go:174-176`). A residual is open: three-to-five-day-old `.iid`/`.vsock`/`.sock` triples persist on this host with no store record, yet `nexus3 reap` reports zero socket-kind orphans. Why `reap` classifies no socket resources despite `ResourceIndex` enumerating them is an open question tracked for R1.

### Builder-supervisor directory reclamation <Badge type="warning" text="partial" />

**RL-12** — The builder-supervisor ULID-named working directory (`<stateRoot>/builder-supervisors/<ULID>/`) MUST be removed when the builder supervisor exits or when its owning sandbox is removed. The reaper reclaims this directory via `deleteResource()` at `reap.go:307-311`: when a `KindBuilderSupervisor` resource is classified orphaned, `deleteResource` calls `os.RemoveAll(res.Path)`. The scanner at `resource_index.go:188-206` enumerates `<stateRoot>/builder-supervisors/` and emits `KindBuilderSupervisor` entries keyed by parsed ULID. **Neither trigger in this requirement is implemented.** There is no `os.RemoveAll` anywhere in `internal/supervisor/`, so the directory survives `StopSupervisor` + `WaitForExit`; and `Service.Remove` (`service.go:660-720`) calls `closeSupervisor`, `store.Delete`, `ReapDiskCopy`, `ReapShadowDisks` and `detachVolumeLocked` without touching `builder-supervisors/`. The sole reclaimer is `deleteResource()` under an operator-invoked `nexus3 reap --apply` — reclamation is manual, not automatic. The `kill -9` path is additionally untested.

### Detached perimeter supervisor tracking <Badge type="danger" text="not built" />

**RL-13** — When a detached perimeter supervisor is spawned via `SpawnDetached()` during `nexus3 orca create`, its PID and socket path MUST be persisted in the sandbox record and checked by `recover`. Currently `recover` does not inspect `SupervisorPID` for orphaned processes; a killed nexus3 parent leaves the supervisor process running without any recovery path. Detection MAY use `kill(PID, 0)` → ESRCH to distinguish live from dead.

### Fork shadow-disk correlation <Badge type="warning" text="partial" />

**RL-14** — *Premise unconfirmed: no code path that creates fork shadow-disk copies could be located — `Service.Fork` (`service.go:1006-1200`) contains no shadow handling at all. R1 MUST confirm a producer exists before building against this requirement; if forks never copy shadow disks, the hazard below cannot occur.* Shadow disk copies created during `nexus3 fork` that are named `<childID>-<parentSafeHandle>.shadow.<name>.ext4` MUST be classified as owned by the child sandbox record, not orphaned. The current `ShadowDiskSafeHandle` function yields `<childID>-<parentSafeHandle>` for such copies, which cannot match any live sandbox handle; these copies are permanently misclassified as orphan once the fork create window closes. The flock lease (D-PD-74) covers only the child's ULID-keyed `.raw` and workspace disks; it does **not** cover shadow disks at any point in the window (RL-17). The post-window correlation residual is open.

### Snapshot ULID relationship and reap coverage <Badge type="danger" text="not built" />

**RL-15** — Snapshot directories (`artifact/snapshot.go`) MUST encode the owning sandbox's ULID in their path in a position that allows `ResourceIndex.List()` to scan and classify them. The ULID relationship is UNVERIFIED: the audit found no snapshots on the host to inspect and the naming convention has not been confirmed. `ResourceIndex.List()` does not currently scan artifact directories. R1 MUST verify naming and extend the scanner.

### Shadow disks are not lease-protected <Badge type="danger" text="not built" />

**RL-17** — The reaper MUST NOT classify a shadow disk as orphaned while a create that is materializing it holds an intent lease. **This is not implemented.** `Reap` routes shadow disks to `classifyShadowDisk(res, shadowHandleMap)` (`reap.go:172-174`), whose signature (`reap.go:201`) takes no `inFlight` argument — the lease-probe result is passed only to `classifyResource` (`reap.go:176`). `shadowHandleMap` is built exclusively from committed store records (`reap.go:151-157`), so during a create the owning handle is absent and the disk classifies as orphan. Separately, `buildShadowDiskSpecs` and `createShadowDisk` run in the CLI at `cmd_sandbox.go:1251-1258`, **before** `writeCreateIntent` at `create.go:441` — so part of the window has no lease in existence at all. Consequence: `nexus3 reap --apply` running concurrently with `nexus3 create` can delete a live sandbox's shadow disks. No test covers this; `reap_shadow_test.go` exercises legacy, B1-no-match, and live-owner cases only. Closing this requires either passing `inFlight` into `classifyShadowDisk`, or moving shadow-disk creation behind the intent write. Tracked as TBD-PD-25. **The supported mitigation is to use `--mount-named` volumes instead of shadow disks** — named volumes are structurally excluded from the reaper (RL-10).

### Cgroup accounting <Badge type="info" text="backlogged" />

**RL-16** — If Cloud Hypervisor creates per-sandbox cgroups, those cgroups MUST be removed when the sandbox is torn down. Cgroup creation and cleanup are UNVERIFIED; it is unknown whether CH creates them and unknown where cleanup would occur. R1 MUST audit this.

---

## Resource inventory

Four-column table: creator, responsible freer, parseable owner key, abnormal-termination behaviour. Evidence labels: **CODE-VERIFIED** = traced to a production code line in this audit; **KERNEL-GUARANTEED** = OS invariant; **UNVERIFIED** = not confirmed.

| Resource kind | Creates | Responsible for freeing | Owner key | Abnormal-termination behaviour |
|---|---|---|---|---|
| Ext4 sandbox disk (CoW) | `create.go:869 cowExt4()` | `disk.go:21 ReapDiskCopy()` ← `service.go:709 Service.Remove()` | YES `disks/<ULID>.raw` | ORPHANED between `cowExt4` and `store.Create`; flock lease on `.create-intent.json` prevents reap during window (`intent.go:236` + `reap.go:75` probe order). **CODE-VERIFIED** |
| Workspace ext4 disk | `create.go:560` (`captureFn = builder.WorktreeToDisk`) | `disk.go:39` removes `-workspace.ext4` | YES `disks/<ULID>-workspace.ext4` | Lease protects in-flight creates. **CODE-VERIFIED** |
| Create-intent marker | `intent.go:236 writeCreateIntent()` | `disk.go:45`; defer cleanup on create failure | YES `disks/<ULID>.create-intent.json` | Exclusive `flock(2)` lease released only after record commit. Kernel releases on SIGKILL. Reap probes leases before loading records (`reap.go:75`). **CODE-VERIFIED** |
| Builder cache disk | `cachedisk.go:60 EnsureCacheDisk()` | MANUAL — no automatic eviction | NO — ecosystem key `caches/buildkit.ext4` | Survives indefinitely (intentional shared cache). No orphan reclamation. |
| Builder image artifact | `image/cache.go:141 Cache.Put()` | `image/cache.go:298 Cache.Prune()` explicit only | NO — content digest `images/sha256/<hex>/` | Survives until explicit Prune. No orphan reclamation. |
| Build-cache entries | UNKNOWN (buildkit metadata dirs) | UNKNOWN | NO — content hash `build-cache/<hex>/` | **UNVERIFIED.** Orphaned `.lock` files observed without parent dirs. R1 audit action. |
| Snapshot directory | `artifact/store.go:66 Store.Write` | `artifact/store.go:188 Store.Remove(SnapshotID)` | PARTIAL — snapshot ID in name; ULID relationship **UNKNOWN-ASSIGNED** | Empty on audit host. R1 must verify naming encodes ULID for reap scanning (RL-15). |
| Sandbox record | `create.go:628 store.Create()` | `service.go:694 store.Delete()` | YES `sandboxes/<ULID>/record.json` | Survives intentionally; record is the handle on all other resources; `recover`'s universe. |
| Network namespace | `ch_netns.go:136 StartNetnsRuntime()` clone | Kernel auto-reclaim on last process exit | NONE (in-kernel) | **KERNEL-GUARANTEED** incl. SIGKILL; no orphan possible. |
| Guest TAP / Host TAP / L2 bridge | `ch_netns.go:333 createTapBridge()` in netns child | Kernel with netns; fallback `ch_net.go:297 deleteTapBridge()` | PARTIAL — first 5 bytes of ULID as 10 hex: `nx3g-/nx3h-/nx3b-<10hex>` | **KERNEL-GUARANTEED** auto-reclaim on netns death. |
| CH VMM child process group | `process.go:115 spawnVMM()` | `teardownSandboxNet` → `NetnsRuntime.Stop()` → `Kill(-childPgid, SIGKILL)` | YES child PID in driver in-process `nets` map | **UNVERIFIED-ASSUMPTION:** orphan survives if parent nexus3 is killed before teardown. `Observe()` reports Absent; `recover` marks stopped but does NOT kill orphan. R1/R2. |
| CH API socket | `driver.go:Start()` | `driver.go:482 clearState()` `os.Remove` | YES `/run/user/<uid>/nexus3/sb-<ULID>.sock` | ORPHANED if killed before `clearState()`. |
| VSock socket | `driver.go:Start()` | `driver.go:483 clearState()` `os.Remove` | YES `.../sb-<ULID>.vsock` | ORPHANED if killed before `clearState()`. |
| IID socket | `driver.go:Start()` (presumed) | `driver.go:484 clearState()` `os.Remove` **CODE-VERIFIED** | YES `.../sb-<ULID>.iid` | `clearState()` removes on normal driver stop. Stale `.iid` triples (3–5 days old, no store record) persist on host; `reap` reports zero socket orphans — open question for R1 (RL-11). |
| Detached perimeter supervisor | `supervisor.go SpawnDetached()` from orca create | `service.go:672 closeSupervisor()` → Stop + waitExit | PID in `domain.Sandbox.SupervisorPID`; sock in `SupervisorSock`. **NO ULID-encoded path** | **UNVERIFIED-ASSUMPTION:** orphan survives if parent exits before `closeSupervisor()`. `recover` does NOT check. R1/R2 (see RL-13). |
| Builder supervisor (ephemeral) | `builder_supervisor_driver.go:133 SpawnDetached(Ephemeral:true)` | `deleteResource()` `reap.go:307` via `os.RemoveAll` **CODE-VERIFIED** | YES `builder-supervisors/<ULID>/` | Watchdog-pipe path exits cleanly via `supervisor.go StopSupervisor()` + WaitForExit. Reap reclaims directory via `os.RemoveAll`. Kill -9 scenario untested (RL-12). |
| Shadow disk (handle-keyed CoW) | `cli/shadowdisk.go:117 buildShadowDiskSpecs()` | `disk.go:67 ReapShadowDisks()` ← `service.go:710` | YES (B1) `<safeHandle>.shadow.<name>.ext4`, safeHandle = ReplaceAll(Handle,"/","_") | B1: owned when handle matches live record, else orphan. Legacy `.shadow.ext4`: unconditionally orphan. **NOT lease-protected** — routed to `classifyShadowDisk` (`reap.go:172-174`), which never sees `inFlight`; deletable mid-create (RL-17). FORK RESIDUAL UNRESOLVED: copies named `<childID>-<parentSafeHandle>.shadow.<name>.ext4` misclassified orphan after window (see RL-14). |
| Cgroups | **UNVERIFIED** — CH may or may not create them | UNKNOWN | UNKNOWN | **UNVERIFIED.** R1 to audit (see RL-16). |
| Named volume disk | `volume store` | **user** — `nexus3 volume rm <name>` | YES `volumes/<name>/disk.ext4` | **USER-OWNED.** Structurally excluded from reap (RL-10). Never deleted by nexus3 tooling except explicit user command. |
| Named volume data dir | `volume store` | **user** — `nexus3 volume rm <name>` | YES `volumes/<name>/data/` | **USER-OWNED.** Structurally excluded from reap (RL-10). |

TAP interfaces, bridge, and the network namespace are in-kernel resources named with the first 10 hex characters of the sandbox ULID. They are auto-reclaimed by the kernel when the Cloud Hypervisor process group dies — even under SIGKILL. The reaper does not target them; for correlation purposes, enumerate `ip link` entries matching `nx3[ghb]-<hex>` and strip the prefix.

## Named volumes

Named volumes (`volume_disk` and `volume_dir`) are user-owned resources that live in `<stateRoot>/volumes/<name>/`. They are **structurally excluded** from the reaper: `ResourceIndex.List()` MUST NOT scan `<stateRoot>/volumes/` (RL-10). A guard test (`TestReap_NeverTouchesVolumes`) fails if that exclusion is ever violated.

### Volume detach on sandbox rm <Badge type="warning" text="partial" />

`nexus3 rm` detaches a sandbox's named volumes but **never** deletes their backing files (D-PD-87). The volume persists until the user explicitly removes it with `nexus3 volume rm <name>` or reclaims detached volumes with `nexus3 volume prune`.

The detach is **incomplete**, which is why this section is badged. Observed live on 2026-08-19: after `nexus3 rm` removed both owning sandboxes, `nexus3 volume rm <name>` still failed with `volume in use: attached to <removed-sandbox-id>` — an attachment record naming a sandbox that no longer exists. `nexus3 volume prune`'s dry-run correctly classified the same volumes as detached, so the stale record blocks `volume rm` specifically; clearing it required `nexus3 volume prune --apply --include-detached`. Whether this is a deliberate deferral of D-PD-87's detach semantics or a defect in the `rm` cleanup path is undetermined (TBD-PD-22).

See [Volume commands](/cli/volume-commands) for the full volume lifecycle.

## Creation: intent-before-materialize

Every raw OS disk and workspace disk creation is journaled before the disk is materialized. **Shadow disks are not** — they are created in the CLI before `CreateAndBoot` writes the intent, and `createIntent` has no field for them (RL-5, RL-17). Named volume backing files are not subject to the intent system — they are created inside the volume store under exclusive per-volume locks, not via the `disks/` intent path. The write sequence in `writeCreateIntent` (`internal/core/service/intent.go`):

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

`nexus3 reap` reclaims orphaned host resources. It is **always a dry-run by default** (RL-4); nothing is deleted without `--apply`.

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

The reaper probes leases **before** it snapshots the store records (RL-7). The two observations are not atomic, and this is the order that makes them safe together: a lease is released only after the record is committed, so a create that releases between the two observations is guaranteed to appear in the record snapshot taken afterwards. Listing records first inverts that guarantee and reopens the window.

Both creation paths take a lease, but only for ULID-keyed disks: `nexus3 create`, and `nexus3 fork` for each child's `<childID>.raw`. `Service.Fork` calls `writeCreateIntent` exactly once per child (`service.go:1140`), passing only the `.raw` path and an empty workspace path — **no lease is taken for any shadow disk**, and per RL-17 a lease would not protect one in any case. Fork children's workspace disks are named `<childID>-workspace.ext4` (ULID-keyed) and handled by the standard liveness gate.

#### Fork with named volumes <Badge type="warning" text="partial" />

`nexus3 fork` refuses when the parent has any attached named volume (D-PD-96, TBR-PD-15 pending design).

Two residuals remain on both create and fork. `ResourceIndex.List()` is a `readdir`, not an atomic directory snapshot, so a scan can in principle observe a `.raw` dirent while having already passed the slot where its `.create-intent.json` landed, yielding a disk with no lease to probe. And an intent file that cannot be read at all (for example one left mode-0600 by another uid) keeps its sandbox's disks indefinitely, as described above.

### Liveness gate

For each ULID-keyed resource that has no store record and no held lease, the reaper runs two further checks before reaching a verdict (RL-8). **Ambiguity always resolves to KEEP** (RL-9).

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

### Shadow disk classification <Badge type="danger" text="not built" />

Shadow disks (`KindDiskShadow`) use handle-based correlation rather than the ULID liveness gate:

- **Legacy format** (`.shadow.ext4`, no embedded handle): unconditionally orphan.
- **B1 format**: owned when the handle matches a live sandbox record; orphan otherwise.

Two shadow-disk hazards follow from handle correlation. **Neither is addressed** — both remain open, and RL-17 is the normative statement of the first:

- **On create — windowed.** A shadow disk is materialized before the sandbox record that will own it. This window is **open**. The flock lease on the create-intent artifact (D-PD-73) protects ULID-keyed resources, but shadow disks never reach the lease check: they are routed to `classifyShadowDisk` (`reap.go:172-174`), which receives only the handle map built from committed records. See RL-17.

- **On fork.** Forked children's workspace disks are named `<childID>-workspace.ext4` (D-PD-80(b)) and are ULID-keyed, so they are owned by the standard liveness gate and never misclassified. Shadow disk copies created during fork are **not** protected by the flock lease either — the same `classifyShadowDisk` routing applies (RL-17), so D-PD-74's protection covers the child's ULID-keyed disks but not its shadow copies. After the window closes, the fork correlation hazard remains for shadow disks named `<childID>-<parentSafeHandle>.shadow.<name>.ext4`: `ShadowDiskSafeHandle` yields `<childID>-<parentSafeHandle>`, which cannot match any live sandbox handle. Such copies are classified orphan permanently once the fork create window closes (RL-14).

The recommended parallel-dev approach avoids this residual entirely: use `--mount-named` volumes instead of shadow disks. The mechanism is a **hard refusal, not a skip** — `nexus3 fork` refuses outright when the parent has any attached named volume, of either kind (D-PD-96, TBR-PD-15 pending design; asserted by `fork_uniform_volume_refusal_test.go`). Because no fork of a volume-carrying sandbox occurs at all, no shadow disk copies are created and the correlation hazard never arises. Note the consequence: a sandbox using named volumes cannot currently be forked.

### What reap does not touch

- TAP / bridge / netns (kernel-owned; auto-reclaimed)
- The record store itself (that is `recover`'s domain)
- Any resource not matched by `ResourceIndex.List()`'s filename patterns
- The `<stateRoot>/volumes/` directory — structurally excluded; named volumes are user-owned (RL-10)
- IID sockets — `clearState()` removes them on normal driver stop; why `reap` classifies zero socket resources despite `ResourceIndex` enumerating them is an open question for R1 (RL-11)
- Builder-supervisor directories — reclaimed by `deleteResource()` at `reap.go:307` via `os.RemoveAll`; kill -9 scenario untested (RL-12)
- Detached perimeter supervisors — recovery path not yet implemented (RL-13) <Badge type="danger" text="not built" />

## Recovery

`recover` (`internal/core/service/`) uses the record store as its **sole universe**: it finds records that have no corresponding running process and attempts substrate-level recovery at the record level. It does **not** check for orphaned processes or files that have no record — those are the reaper's domain.

**Known gaps:** `recover` does not inspect `SupervisorPID` for orphaned detached perimeter supervisor processes (RL-13). IID sockets are removed by `clearState()` on normal driver stop (`driver.go:484`); `recover` does not explicitly re-run IID cleanup for sockets that survive abnormal termination — the open question is why `reap` classifies zero socket resources despite `ResourceIndex` enumerating them (RL-11). A sandbox whose record was deleted before all teardown steps completed may leave detached perimeter supervisor processes orphaned with no recovery path.

**Scoped exception**: netns, TAP, and bridge are in-kernel and auto-reclaimed when the Cloud Hypervisor process group dies even under SIGKILL. For those three, the kernel is the handle — `recover` does not need to track them explicitly.

## Disk preflight <Badge type="warning" text="partial" />

Only `nexus3 up` runs a disk-space preflight. `service.CheckDiskSpace` (`internal/core/service/preflight.go:122`) has exactly one non-test caller in the repo, `cmd_up.go:60`; `nexus3 create` (`cmd_sandbox.go:812`), `nexus3 run` (`cmd_run.go:58`) and `nexus3 orca` (`cmd_orca.go:523`) do **not** call it — they resolve the kernel path only. Where it does run, the preflight:

- Measures existing sandbox disks (raw and shadow) using `stat(2).Blocks * 512` (allocated bytes, not apparent size — sparse ext4 images have inflated apparent sizes).
- Projects `count × per-sandbox-estimate` against the host's available bytes (`Bavail × Bsize` from `statfs(2)`).
- Returns `ErrInsufficientDisk` if the projection exceeds available space.

The preflight covers nexus3-managed disks only. Named volume backing files are outside the estimate — volume sizes are set explicitly at creation time and are the user's responsibility.

The per-sandbox estimate defaults to ~4.57 GiB (measured from a real pilot sandbox) when no existing sandbox disks are present to sample.

## Kernel preflight

All sandbox-creation entry points (`nexus3 create`, `nexus3 run`, `nexus3 orca`) call `resolveKernelPath()` before any store or VM setup. If `NEXUS3_KERNEL_PATH` is unset or the file does not exist, creation fails immediately with a legible error. Without this preflight, Cloud Hypervisor's own error is the opaque `"Cannot open kernel file"`.
