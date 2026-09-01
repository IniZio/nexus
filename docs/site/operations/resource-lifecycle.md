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

**RL-3** — Every system-owned resource that is **not** kernel-managed MUST carry a deterministic, parseable owner key. The primary key is the owning sandbox's ULID embedded in the on-disk path in a position `ResourceIndex.List()` can parse. A **supplementary correlation key** is also permitted where a resource is legitimately not ULID-keyed: shadow disks are keyed by the sandbox *handle* (`<safeHandle>.shadow.<name>.ext4`, `cli/shadowdisk.go:117`), and the reaper correlates them against live sandbox handles (`reap.go:250-254`). A resource carrying **neither** key is unclassifiable. Unclassifiable resources of a kind whose ownership can never be established — the legacy `*.shadow.ext4` format, which embeds no handle at all — are reclaimable (`reap.go:241-246`); for every other kind, an unresolved owner MUST resolve to KEEP under RL-9. **The detached perimeter supervisor now SATISFIES this requirement**: its state lives at `<stateRoot>/supervisors/<ULID>/`, a ULID in a position `ResourceIndex.List()` parses (`resource_index.go:245-262`, `KindSupervisorState`). Before that directory existed the supervisor was tracked only by `SupervisorPID`/`SupervisorSock` in the record and carried no owner key at all (RL-18).

### Reap CLI

**RL-4** — `nexus3 reap` MUST be a no-op (dry run) by default. Deletion MUST require the explicit `--apply` flag. A reap invocation without `--apply` MUST print a human-readable report of what would be deleted and exit 0.

### Intent-before-materialize and the create lease

**RL-5** <Badge type="tip" text="built" /> — Every disk creation MUST write a durable intent marker before the disk file is materialized. **The raw OS disk and the workspace disk** are covered by the ULID-keyed create intent at `disks/<ULID>.create-intent.json`: `writeCreateIntent` (`create.go:442`) runs ahead of `cowExt4`, and `createIntent` (`intent.go`) carries `DiskCopyPath` and `WorkspaceDiskPath`. **Shadow disks are covered by a separate, handle-keyed intent** at `disks/<safeHandle>.shadow-intent.json`. They need their own marker because they are materialized in the CLI before `CreateAndBoot` mints the ULID, so the create intent cannot name them and does not yet exist when they are written. `prepareShadowDisks` (`internal/cli/shadowdisk.go`) publishes the shadow intent and only then calls `createShadowDisk`, so no shadow disk is ever visible without a marker claiming it. Both markers publish through `publishLeasedIntent`, which stages, flocks, fsyncs, renames and fsyncs the directory, so an intent becomes discoverable only in the already-leased state. Named volume backing files are excluded from this requirement; they are created under per-volume exclusive locks in the volume store.

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

**RL-12** — The builder-supervisor ULID-named working directory (`<stateRoot>/builder-supervisors/<ULID>/`) MUST be removed when the builder supervisor exits or when its owning sandbox is removed. The reaper reclaims this directory via `deleteResource()` at `reap.go:307-311`: when a `KindBuilderSupervisor` resource is classified orphaned, `deleteResource` calls `os.RemoveAll(res.Path)`. The scanner at `resource_index.go:188-206` enumerates `<stateRoot>/builder-supervisors/` and emits `KindBuilderSupervisor` entries keyed by parsed ULID. **Neither trigger in this requirement is implemented.** There is no `os.RemoveAll` anywhere in `internal/supervisor/`, so the directory survives `StopSupervisor` + `WaitForExit`; and `Service.Remove` (`service.go:714-840`) calls `closeSupervisor` (`:741`), `store.Delete` (`:763`), `ReapDiskCopy` (`:778`), `ReapShadowDisks` (`:779`), `removeSupervisorStateDir` (`:797`) and `detachVolumeLocked` (`:817`) without touching `builder-supervisors/`. Note that the state-dir removal at `:797` covers the *perimeter* supervisor's `supervisors/<ULID>/` (RL-18), not the builder supervisor's directory — the two are separate trees and only the first is torn down with the sandbox. The sole reclaimer is `deleteResource()` under an operator-invoked `nexus3 reap --apply` — reclamation is manual, not automatic. The `kill -9` path is additionally untested.

### Detached perimeter supervisor tracking <Badge type="danger" text="not built" />

**RL-13** — When a detached perimeter supervisor is spawned via `SpawnDetached()` during `nexus3 orca create`, its PID and socket path MUST be persisted in the sandbox record and checked by `recover`. Currently `recover` does not inspect `SupervisorPID` for orphaned processes; a killed nexus3 parent leaves the supervisor process running without any recovery path. Detection MAY use `kill(PID, 0)` → ESRCH to distinguish live from dead.

### Perimeter-supervisor state directory <Badge type="tip" text="built" />

**RL-18** <Badge type="tip" text="built" /> — The per-sandbox perimeter-supervisor state directory (`<stateRoot>/supervisors/<ULID>/`) MUST be ULID-keyed, MUST be removed when its owning sandbox is removed, MUST be reclaimable by the reaper when no owner remains, and MUST NOT be reclaimed while the owner might still exist. The path and its modes are owned by one package (`internal/core/statedir`) because `internal/supervisor` and `internal/core/service` both need them and cannot import each other. Directory mode is `0700` and every file inside is `0600`: the directory holds the egress decisions log and the **MITM CA private key** (`mitm-ca.pem`, D-HSH-18), so a reclamation error here discloses or destroys a key rather than wasting a few blocks. `statedir.Ensure` chmods a pre-existing directory rather than trusting `MkdirAll`, which is a no-op on an existing path — that is how directories already on disk at `0755` get tightened. Teardown is `removeSupervisorStateDir` from `Service.Remove` (`service.go:797`), ordered after `store.Delete` and idempotent. Reclamation is `deleteResource()`'s `os.RemoveAll` on a `KindSupervisorState` entry classified orphan.

The classification is **fail-closed on every gate** (`classifySupervisorState`, `reap.go:630`), which is what makes `--apply` safe against a key-bearing directory:

- a store record → owned, kept before any liveness question is asked, which covers the live-VM/dead-supervisor adoptable class — `recovery` only ever reaches `OutcomeAdoptable` from a record, so an adoptable sandbox always has one;
- a held create-intent lease, a ULID in `/proc/*/cmdline`, or a responsive API socket → live (the RL-8 gate);
- the record **directory** exists under the store root but the record does not decode → live (`reap.go:644`). `store.List` silently skips records it cannot decode — corrupt, half-written by an interrupted `Create`, or `ErrSchemaTooNew` — so record-map absence is not evidence of record absence, and an older binary sees `ErrSchemaTooNew` for *every* record. Without this gate one `reap --apply` under a stale binary would collect the state dir of every stopped sandbox at once;
- a responsive `supervisor.sock` inside the directory → live, even when every ULID-keyed check has gone quiet. A supervisor can outlive its own VM socket, and that socket lives inside the directory being judged;
- any ambiguity in any of the above — a stat error other than ENOENT, a socket timeout, an unexpected dial error — → live, per RL-9.

Covered by `statedir_lifetime_test.go`, which drives the real `Service.Remove`, `service.Reap` and `statedir.Ensure` rather than a re-implementation: teardown, idempotent teardown, `0700` creation, tightening a pre-existing `0755`, parents left alone, and five reap cases (live record, adoptable, responsive supervisor socket, unreadable record, genuinely-gone → reclaimed).

#### `mitm-ca.pem` — the persisted MITM CA <Badge type="tip" text="built" />

**RL-19** <Badge type="tip" text="built" /> — The per-sandbox MITM CA certificate and private key MUST be persisted inside the state directory when the perimeter mints them, MUST be re-seeded into the replacement perimeter on the crash-recovery path, and MUST NOT outlive the sandbox (D-HSH-18).

Without this, `kill -9 <supervisor>` + `nexus3 recover` restored plain networking but every in-guest TLS session broke, because the CA existed only in the dead supervisor's memory and travelled only in `handoff.Payload.CA` — which a crashed process never sent. Guest-side re-import was measured and rejected: a long-running Node process reads `NODE_EXTRA_CA_CERTS` **once, at process startup**, so no re-import can reach the already-running in-guest agent.

- **Written** by `startSupervisor` (`service.go`) right after `mitm.New`, via `statedir.SaveCA` — write-temp-then-`fsync`-then-`rename`, so a crash mid-write can never leave a half pair for the recovery path to trip over. One file holding both PEM blocks, because two files can diverge.
- **Read** by `reacquireSeedInput` (`internal/supervisor/reacquire.go`) on the `RunReacquire` crash path, which hands it to `StartPerimeterOnly` as a `service.CASeed`. The replacement perimeter keeps signing with the anchor the guest already trusts, so recovery is transparent to a running in-guest process.
- **Not encrypted.** Deliberate: host TPM2 support measured `partial` on the reference host, and an unwrap that can fail converts a recoverable sandbox into an unrecoverable one. Host-root can already read the CA out of the live supervisor's memory, so wrapping buys nothing against the attacker who matters. `0600` inside `0700`, plus the lifetime bound above, is the boundary.
- **Fail-closed and loud.** An absent, unreadable, corrupt, truncated, half-written, mismatched, or **expired** file mints a fresh CA and reports `CALost` with a WARN naming the cause and the path. `statedir.LoadCA` applies exactly the checks `mitm.New` applies to a seed, so a damaged file can never become a supervisor start-up failure — a recovery that *died* on a truncated CA would turn a recoverable sandbox into an unrecoverable one.
- **Bounded lifetime.** `generateCA`'s `NotAfter` was shortened from 10 years to **90 days** now that the key lives on disk: far longer than any observed sandbox lifetime (the oldest of the 641 orphaned state dirs measured for this ticket was 13 days), short enough that a key recovered from a stale image stops being a usable anchor within a quarter. An expired CA is rejected on load rather than silently reused.

Covered by `internal/core/statedir/ca_test.go` (round-trip through the real `mitm.Proxy.CAKeyPair` → `SaveCA` → `LoadCA` → `mitm.New` seed, modes, and every damage mode), `internal/supervisor/reacquire_ca_test.go` (the crash path's real `reacquireSeedInput`, including per-sandbox scoping) and `internal/core/service/ca_persistence_test.go` (the real `StartPerimeterOnly` writes it; the real `Service.Remove` leaves no private-key PEM anywhere under the store root).

### Fork shadow-disk correlation <Badge type="tip" text="built" />

**RL-14** <Badge type="tip" text="built" /> — Shadow disk copies created during `nexus3 fork` MUST be classified as owned by the child sandbox record, not orphaned. **The producer exists** — an earlier note here recorded it as unconfirmed because `Service.Fork` contains no shadow handling, but the copy is made one layer down: `ForkFrom` copies every parent extra disk and shadow disks ARE extra disks, so `ChildExtraDiskPath` (`internal/core/driver/cloudhypervisor/fork.go:108`) yields `<childULID>-<parentSafeHandle>.shadow.<name>.ext4`. `diskname.ShadowDiskSafeHandle` returns that whole composite, which matches no sandbox handle, so handle correlation alone orphans a live fork child's dependency tree permanently. Resolved by `forkChildShadowOwner`: every `-` position in the safeHandle is offered to `ParseSandboxID` (the ULID's own string form contains a `-`, and a parent handle may contain more, so no single split works) and the child is looked up in the record map. Ownership still requires a LIVE child record — once the child is gone its copies are reclaimable like anything else. Covered by `TestReap_ShadowDisk_ForkChildCopyIsOwned` and `..._ForkChildCopyReclaimedWhenChildGone`.

### Snapshot ULID relationship and reap coverage <Badge type="danger" text="not built" />

**RL-15** — Snapshot directories (`artifact/snapshot.go`) MUST encode the owning sandbox's ULID in their path in a position that allows `ResourceIndex.List()` to scan and classify them. The ULID relationship is UNVERIFIED: the audit found no snapshots on the host to inspect and the naming convention has not been confirmed. `ResourceIndex.List()` does not currently scan artifact directories. R1 MUST verify naming and extend the scanner.

### Shadow disks are lease-protected <Badge type="tip" text="built" />

**RL-17** <Badge type="tip" text="built" /> — The reaper MUST NOT classify a shadow disk as orphaned while a create that is materializing it holds an intent lease. `Reap` probes every `KindShadowIntent` resource with `probeIntentLease` and builds a handle-keyed in-flight map, which it passes to `classifyShadowDisk` alongside the record-derived handle map. A held lease classifies the disk `live` and the check runs FIRST, before the record lookup — mid-create the owning handle has no committed record, so any later branch would conclude orphan. The map is keyed by handle rather than ULID because shadow disks are correlated by handle; the ULID-keyed in-flight map used for `.raw` and workspace disks cannot answer the question this classifier asks. **The lease expires with its holder**: `flock(2)` is released by the kernel when the creating process dies, so a crashed create leaves an unleased intent that protects nothing and both the intent and its disks are reclaimed by the next reap. Covered by `reap_shadow_inflight_test.go` (in-flight keep, unleased-intent reclaim, handle scoping) and `shadowdisk_intent_order_test.go`, which runs a real `Reap(apply=true)` from inside disk creation. Closed as TBD-PD-25. The same guarantee now extends to fork and restore children via `leaseForkChildren`, proven the same way by `fork_shadow_inflight_test.go` and `restore_inflight_test.go` (TBD-PD-38).

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
| Detached perimeter supervisor | `supervisor.go SpawnDetached()` from orca create | `service.go:741 closeSupervisor()` → Stop + waitExit | PID in `domain.Sandbox.SupervisorPID`; sock in `SupervisorSock` | **UNVERIFIED-ASSUMPTION:** orphan survives if parent exits before `closeSupervisor()`. `recover` does NOT check `SupervisorPID`. R1/R2 (see RL-13). |
| Perimeter-supervisor state dir | `statedir.Ensure()` from every supervisor entry point (Serve, Adopt, RunReacquire, WriteSpawnSpec) | `service.go:797 removeSupervisorStateDir()` ← `Service.Remove`; reaper via `deleteResource()` `os.RemoveAll` **CODE-VERIFIED** | YES `supervisors/<ULID>/` | Survives an abnormal exit deliberately — it is what a re-acquisition consumes. Reclaimed only when no record dir, no live process, and no responsive VM or supervisor socket; every ambiguity keeps. `0700`/`0600`, holds the MITM CA private key (RL-18, D-HSH-18). |
| Builder supervisor (ephemeral) | `builder_supervisor_driver.go:133 SpawnDetached(Ephemeral:true)` | `deleteResource()` `reap.go:307` via `os.RemoveAll` **CODE-VERIFIED** | YES `builder-supervisors/<ULID>/` | Watchdog-pipe path exits cleanly via `supervisor.go StopSupervisor()` + WaitForExit. Reap reclaims directory via `os.RemoveAll`. Kill -9 scenario untested (RL-12). |
| Shadow disk (handle-keyed CoW) | `cli/shadowdisk.go:117 buildShadowDiskSpecs()` | `disk.go:67 ReapShadowDisks()` ← `service.go:710` | YES (B1) `<safeHandle>.shadow.<name>.ext4`, safeHandle = ReplaceAll(Handle,"/","_") | B1: owned when handle matches live record, else orphan. Legacy `.shadow.ext4`: unconditionally orphan. Lease-protected by a **handle-keyed shadow intent** (`<safeHandle>.shadow-intent.json`), published before the first disk byte on create and before `ForkFrom` on fork/restore; the ULID create intent cannot cover these because the reaper correlates them by handle (RL-17). Fork/restore copies are named `<childID>-<parentSafeHandle>.shadow.<name>.ext4` and are owned via `forkChildShadowOwner` once the child record commits (RL-14). |
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

### Agent config overlay volume <Badge type="tip" text="built" />

`nexus3 create --agent <name>` silently provisions a 2 GiB named ext4 volume (`<proj>__<handle>__agentcfg`) and attaches it to the sandbox. The volume backs the writable upper layer of an overlayfs mounted at `/root/.claude` inside the guest. Moving the upper layer off the root disk makes it governor-visible (the root `/dev/vda` is never enrolled in `ResizableDiskIndices`). The lower layer is a read-only virtiofs share of the host's curated Claude config; it is host-backed and consumes zero guest disk.

Boot **fails closed** (aborts with a hard error) when the volume is absent and no prior upper-layer data is found on the root disk. The fail-closed path prevents a new agent sandbox from silently losing session transcripts, todos, and stats to the ungrowable root disk. The sole exception is a pre-existing sandbox created before this provisioning was introduced: if root-disk data is found at the old path, boot degrades gracefully (D-RAM-11) and emits a structured `slog.Warn`; no non-destructive drain to a named volume exists yet (D-RAM-15).

## Creation: intent-before-materialize

Every raw OS disk and workspace disk creation is journaled before the disk is materialized. **Shadow disks are journaled separately** — they are created in the CLI before `CreateAndBoot` writes the ULID intent, and `createIntent` has no field for them, so they carry their own handle-keyed shadow intent instead (RL-5, RL-17). Named volume backing files are not subject to the intent system — they are created inside the volume store under exclusive per-volume locks, not via the `disks/` intent path. The write sequence in `writeCreateIntent` (`internal/core/service/intent.go`):

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

### Failed reclamations are reported <Badge type="tip" text="built" />

`--apply` reports every orphan it could not reclaim, on stderr and in the JSON `failed[]` array, and **exits non-zero**. Two cases land there:

- the delete returned an error (permissions, a non-empty directory, a busy mount), and
- the delete returned success while the path was still present on a verify pass.

The second case exists because a reported success is not evidence of a removal. On 2026-08-19 an `--apply` pass printed `Deleted 129 resource(s)` while one file survived it; a second pass removed the same file, and nothing in the first pass's output contradicted the claim. `Reap` now re-stats every path it believes it deleted and moves survivors out of `deleted[]` into `failed[]`, so the count never overstates the work.

A path that vanished between the scan and the delete is **not** a failure — another reaper or a concurrent `sandbox rm` finished the job, and reclamation is idempotent.

::: warning The 2026-08-19 survivor has no known cause
Enumeration was cleared as the culprit — a probe enumerated 5, 129, 130 and 200 disks correctly. The verify pass makes that class of discrepancy visible at the moment it happens rather than on the next run; it does not explain it. If a path lands in `failed[]` with "delete reported success but the path still exists", that is new evidence worth capturing.
:::

### In-flight creates (intent lease)

A create materializes its disk **before** it commits the store record — a multi-second copy for a multi-GiB image. During that window the disk has no record, and no process carries the ULID in its command line (the creator is the `nexus3` CLI itself; the VMM is not launched until afterwards), so the `/proc` gate below cannot see it. The reaper would classify a live create's disk as an orphan and delete it.

The create-intent file therefore carries an exclusive `flock(2)` **lease**, taken before the intent becomes discoverable and released only after the record is committed:

- **lease held** → a create is in flight → keep.
- **lease free** → the creator is gone (crashed, killed, or the host rebooted) → classify normally.
- **intent unreadable** → cannot rule out a live creator → keep, and say so distinctly in the report. This is the one keep that does **not** expire on its own and needs an operator to inspect the file.

The kernel releases `flock` when the holding process dies — including on `SIGKILL` — and a reboot releases everything, so a dead creator can never block reclamation.

The reaper probes leases **before** it snapshots the store records (RL-7). The two observations are not atomic, and this is the order that makes them safe together: a lease is released only after the record is committed, so a create that releases between the two observations is guaranteed to appear in the record snapshot taken afterwards. Listing records first inverts that guarantee and reopens the window.

Every creation path takes **two** leases, because the reaper asks two different questions:

| Intent | Keyed by | Covers |
|---|---|---|
| `<ULID>.create-intent.json` | sandbox ULID | `<id>.raw`, `<id>-workspace.ext4` |
| `<safeHandle>.shadow-intent.json` | sandbox handle | that sandbox's shadow disks |

Neither substitutes for the other, and that is the whole point: `.raw` is correlated by ULID and shadow disks by handle, so a single intent cannot answer both questions. Treating them as one is what produced TBD-PD-25 and TBD-PD-38 — the same bug in two places.

`nexus3 create` publishes both (the shadow intent in the CLI, before the first disk is materialised; the ULID intent inside `CreateAndBoot`). `nexus3 fork` and `nexus3 restore` publish both per child via `leaseForkChildren`, before `ForkFrom` runs, and release each pair only after that child's `store.Create` commits. Fork children's workspace disks are named `<childID>-workspace.ext4` (ULID-keyed) and are covered by the first intent.

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

### Shadow disk classification <Badge type="tip" text="built" />

Shadow disks (`KindDiskShadow`) use handle-based correlation rather than the ULID liveness gate:

- **Legacy format** (`.shadow.ext4`, no embedded handle): unconditionally orphan.
- **B1 format**: owned when the handle matches a live sandbox record; orphan otherwise.

Two shadow-disk hazards follow from handle correlation. **Both are now closed**; RL-17 is the normative statement of the first and RL-14 of the second:

- **On create — closed.** A shadow disk is materialized before the sandbox record that will own it. A handle-keyed shadow intent (`disks/<safeHandle>.shadow-intent.json`) is published before the first disk is written and leased for the whole window; `Reap` probes it and `classifyShadowDisk` consults the resulting in-flight map first. The ULID-keyed create-intent lease (D-PD-73) could never have covered this — it answers a question about a ULID, and shadow disks are correlated by handle. See RL-17.

- **On fork and restore — closed.** Forked children's workspace disks are named `<childID>-workspace.ext4` (D-PD-80(b)) and are ULID-keyed, so the standard liveness gate owns them. Their shadow copies are named `<childID>-<parentSafeHandle>.shadow.<name>.ext4`, which matches no sandbox handle; `classifyShadowDisk` falls back to parsing the child ULID out of that composite and resolving it against the record map, so a live child owns its copies (RL-14). The fork window itself is covered too: `leaseForkChildren` publishes a handle-keyed shadow intent per child before `ForkFrom` writes anything, released only after that child's record commits (TBD-PD-38). `nexus3 restore` was the wider of the two exposures — it had no intent leases of any kind — and now takes the same pair.

The recommended parallel-dev approach sidesteps shadow disks entirely: use `--mount-named` volumes instead. The mechanism is a **hard refusal, not a skip** — `nexus3 fork` refuses outright when the parent has any attached named volume, of either kind (D-PD-96, TBR-PD-15 pending design; asserted by `fork_uniform_volume_refusal_test.go`). Because no fork of a volume-carrying sandbox occurs at all, no shadow disk copies are created and the correlation hazard never arises. Note the consequence: a sandbox using named volumes cannot currently be forked.

### What reap does not touch

- TAP / bridge / netns (kernel-owned; auto-reclaimed)
- The record store itself (that is `recover`'s domain)
- Any resource not matched by `ResourceIndex.List()`'s filename patterns
- The `<stateRoot>/volumes/` directory — structurally excluded; named volumes are user-owned (RL-10)
- IID sockets — `clearState()` removes them on normal driver stop; why `reap` classifies zero socket resources despite `ResourceIndex` enumerating them is an open question for R1 (RL-11)
- Builder-supervisor directories — reclaimed by `deleteResource()` at `reap.go:307` via `os.RemoveAll`; kill -9 scenario untested (RL-12)
- Detached perimeter supervisor **processes** — reap does not enumerate `SupervisorPID`, so an orphaned supervisor process is not reclaimed (RL-13) <Badge type="danger" text="not built" />. Its **state directory** is a different resource and reap *does* touch it: `supervisors/<ULID>/` is enumerated as `KindSupervisorState` and reclaimed when nothing can still own it (RL-18)

## Recovery

`recover` (`internal/core/service/`) uses the record store as its **sole universe**: it finds records that have no corresponding running process and attempts substrate-level recovery at the record level. It does **not** check for orphaned processes or files that have no record — those are the reaper's domain.

**Known gaps:** `recover` does not inspect `SupervisorPID` for orphaned detached perimeter supervisor processes (RL-13). IID sockets are removed by `clearState()` on normal driver stop (`driver.go:484`); `recover` does not explicitly re-run IID cleanup for sockets that survive abnormal termination — the open question is why `reap` classifies zero socket resources despite `ResourceIndex` enumerating them (RL-11). A sandbox whose record was deleted before all teardown steps completed may leave detached perimeter supervisor processes orphaned with no recovery path.

**Scoped exception**: netns, TAP, and bridge are in-kernel and auto-reclaimed when the Cloud Hypervisor process group dies even under SIGKILL. For those three, the kernel is the handle — `recover` does not need to track them explicitly.

## Disk preflight <Badge type="tip" text="built" />

Every sandbox-creating path refuses up front when the projected allocation exceeds host free space. There are two call sites, both in the service layer so the CLI, MCP and Orca surfaces are covered by the same code:

| Path | Where | What is projected |
|---|---|---|
| `create`, `run`, `orca`, MCP `sandbox_create` | `CreateAndBoot` step 3.55 | source artifact's allocated size, plus a workspace-disk estimate when `--workspace` is given |
| `fork`, `restore` | `Service.Fork` / `Service.RestoreFromSnapshot` | parent's whole measured footprint × child count |

The check runs **before** the create intent is written and before any byte is copied, so a refusal leaves nothing behind to reap.

All measurement uses `stat(2).Blocks * 512` (allocated bytes), never apparent size — sparse ext4 images report apparent sizes many times their real footprint.

### The projection is an upper bound, not a cost

The create-path figure is the **source artifact's** allocated size. `cowExt4` runs `cp --sparse=always`, which punches holes for zero runs that the source had allocated, so the copy can only come out smaller. Measured on a real host: a **6.00 GiB artifact produced a 2.64 GiB copy** — the projection over-charges by roughly 2.3×.

It errs toward refusing rather than toward filling the disk, which is the right direction, but it does mean a create can be refused that would have fit. On btrfs and xfs the gap is wider still, because `cp --reflink=auto` clones extents at near-zero cost while the projection charges full price.

`--force` skips the check on `sandbox create`, `run`, `fork` and `restore`. It exists for exactly these cases.

Fork is the opposite: every file it copies already exists, so its projection is measured rather than estimated. Fork is also the largest allocator nexus3 has — an N-way fork of a 5 GiB parent needs 5N GiB — and it was entirely unguarded until this landed.

### What is not covered

Only the workspace capture is estimated rather than measured, because it cannot be sized before it runs; it falls back to the mean of existing `*-workspace.ext4` files, or ~4.57 GiB when there are none to sample.

Named volume backing files are outside the projection entirely — volume sizes are set explicitly at creation time and are the user's responsibility.

## Kernel preflight

All sandbox-creation entry points (`nexus3 create`, `nexus3 run`, `nexus3 orca`) call `resolveKernelPath()` before any store or VM setup. If `NEXUS3_KERNEL_PATH` is unset or the file does not exist, creation fails immediately with a legible error. Without this preflight, Cloud Hypervisor's own error is the opaque `"Cannot open kernel file"`.
