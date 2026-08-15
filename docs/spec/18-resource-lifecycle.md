# Spec 18 — Resource Ownership and Reclamation Contract

**Status:** Normative  
**Date:** 2026-08-15  
**Inputs:** `docs/notes/2026-08-resource-inventory-audit.md` (R0-AC1, R0-AC2, R0-AC3)  
**Downstream:** R1 (ResourceIndex + reap), R2 (journaled create + recover fix), R3 (extreme-case leak suite)  

---

## 1. Purpose

This document states the **normative ownership and reclamation contract** for every host resource a nexus3 sandbox allocates. It is the reference that R1, R2, and R3 implement against. Downstream slices must not derive this contract from code inspection — they must use this document.

---

## 2. Governing Principles

**P-1 — Record-as-only-handle.** The sandbox record (`sandboxes/<ULID>/record.json`) is the only structured handle that links a sandbox ULID to all of its runtime resources. A resource with no corresponding record is permanently invisible to `recover` and permanently invisible to any consumer of the record store. This is a structural property, not a code bug: the record store is built around this invariant.

*Exception:* In-kernel resources (network namespace, TAP interfaces, bridge) are automatically reclaimed by the kernel when the CH process group exits — even under SIGKILL. These are not subject to P-1 because the kernel is the authoritative handle.

**P-2 — Sparse size is not allocated size.** ext4 images are created with `os.Truncate` + `mke2fs -E nodiscard`. Their apparent size can be orders of magnitude larger than their allocated size. All size accounting MUST use `du` (allocated) not `du --apparent-size`. Any preflight, census, or report that uses apparent size for a capacity decision is wrong.

**P-3 — Ambiguity resolves to keep.** The reaper (R1) must never delete a resource it cannot definitively prove is unowned. When the ownership relationship is uncertain — process exists but record is absent, record exists but state is ambiguous — the reaper leaves the resource alone and reports it. Liveness gates (P-5) enforce this.

**P-4 — Owner key is the ULID.** Every resource that does not carry a full sandbox ULID as a parseable token in its name or a sidecar is not directly reapable by name. Resources in this class require a supplementary correlation strategy (see § 4.4).

**P-5 — Liveness gate is mandatory.** Before reporting a resource as reclaimable, R1 MUST verify that no live process holds it. Live = any of: (a) a process with the sandbox ULID's PID is alive (`kill(pid, 0)` = 0), (b) the CH API socket responds, (c) a heartbeat/lease file is fresh. Ambiguity resolves to keep (P-3).

---

## 3. Resource Kinds — Normative Table

Each row states: **creation point, reclamation point, owner-key format, and failure-window characterization**.

### 3.1 — Disk Resources

| Kind | Created at | Reclaimed at | Owner-key format | Failure window |
|---|---|---|---|---|
| **Ext4 sandbox disk (S-CoW)** `disks/<ULID>.raw` | `service/create.go:335` `cowExt4` — BEFORE `store.Create` at line 410 | `service.Remove:628` `ReapDiskCopy`; or recovery `--rm` path in `recovery/recover.go:309` | Full ULID in filename | **Open:** disk exists without record if process dies between `cowExt4` (line 335) and `store.Create` (line 410). This window produced the P13 class of orphans. |
| **Workspace ext4 disk** `disks/<ULID>-workspace.ext4` | `service/create.go:383` `WorktreeToDisk` — BEFORE `store.Create` | Deferred `os.Remove` on create failure only. `service.Remove` does **not** reap `-workspace.ext4` (known gap, `ReapDiskCopy` only removes `.raw`). | Full ULID in filename prefix | **Open (two windows):** (a) same kill-9 window as S-CoW; (b) workspace disk is leaked by a successful `service.Remove` (gap not yet addressed). |
| **Builder cache disk** `caches/<key>.ext4` | `builder/cachedisk.go:71` `EnsureCacheDisk` | No automatic eviction. Must be explicitly pruned. | **None** — named by ecosystem key | N/A — shared resource; not sandbox-owned. R1 must NOT include this in the reap scope. |
| **Builder image artifact** `images/sha256/<hex>/artifact` | `core/image/cache.go:141` `image.Cache.Put` | `image.Cache.Prune(referenced)` — explicit only | **None** — named by content digest | N/A — content-addressed shared cache. Eviction is caller's responsibility. R1 must NOT reap these. |

### 3.2 — Sandbox Record

| Kind | Created at | Reclaimed at | Owner-key format | Failure window |
|---|---|---|---|---|
| **Sandbox record** `sandboxes/<ULID>/record.json` | `service/create.go:410` `store.Create` | `service/service.go:620` `store.Delete` | Full ULID as directory name | Record creation is the last step of the create sequence. Resources created before this point are orphaned if the process dies. |

### 3.3 — Network Resources

| Kind | Created at | Reclaimed at | Owner-key format | Failure window |
|---|---|---|---|---|
| **Network namespace** | `ch_netns.go:136` `StartNetnsRuntime` → `clone()` | Kernel: auto-reclaims when last process in namespace exits | None — in-kernel, no named path | **None** — kernel handles even under SIGKILL. |
| **Guest TAP** `nx3g-<10hex>` | `ch_netns.go:333` `createTapBridge` (inside netns child) | Kernel auto-reclaims with netns; `deleteTapBridge` as explicit fallback | **Partial ULID** — first 5 bytes of ULID as 10 lowercase hex chars. Sufficient for correlation; not globally unique across all ULIDs. | **None** — kernel handles even under SIGKILL. |
| **Host TAP** `nx3h-<10hex>` | Same | Same | Same partial-ULID scheme | None |
| **Bridge** `nx3b-<10hex>` | Same | Same | Same partial-ULID scheme | None |
| **CH VMM process** | `ch_netns.go:338` `spawnVMM` inside netns child | `teardownSandboxNet` → `NetnsRuntime.Stop()` → `Kill(-childPgid)` | In-process: `driver.nets[SandboxID]` map | **Open:** CH may survive if nexus3 parent is killed before `teardownSandboxNet`. `recover` resolves the record to stopped but does NOT kill orphan CH processes on the non-delete path. |

### 3.4 — Unix Socket Files

| Kind | Created at | Reclaimed at | Owner-key format | Failure window |
|---|---|---|---|---|
| **CH API socket** `/run/user/1003/nexus3/sb-<ULID>.sock` | `driver.Start` → `StartNetnsRuntime` | `driver.Stop` → `clearState()` → `os.Remove` | Full ULID in filename | **Open:** orphaned if process killed before `clearState`. `recover` non-delete path does not call `drv.Stop`, so sockets are not cleaned when record transitions to stopped. |
| **VSock socket file** `/run/user/1003/nexus3/sb-<ULID>.vsock` | `driver.Start` | `driver.Stop` → `clearState()` → `os.Remove` | Full ULID in filename | Same as CH API socket |
| **IID file** `/run/user/1003/nexus3/sb-<ULID>.iid` | `driver.Start` (stores hypervisor instance ID) | **Unknown (gap):** no cleanup observed in `service.Remove` or `recover`. R2 to identify and fix. | Full ULID in filename prefix | Same as above; additionally: 3 IID files observed on this host with no backing record. |

### 3.5 — Process Resources

| Kind | Created at | Reclaimed at | Owner-key format | Failure window |
|---|---|---|---|---|
| **Detached perimeter supervisor** | `supervisor.SpawnDetached` (orca path) after VM ready | `service.Remove:598` `closeSupervisor` | PID in `domain.Sandbox.SupervisorPID` (record field); socket in `SupervisorSock` | **Open:** `recover` does not check liveness of SupervisorPID on non-delete path. Orphaned supervisor can persist until next `service.Remove`. |
| **Builder supervisor** (ephemeral) | `cli/builder_supervisor_driver.go:133` `SpawnDetached(Ephemeral:true)` | `supervisorBuilderDriver.Stop` → `StopSupervisor` + `WaitForExit` | `builder-supervisors/<ULID>/` state directory | SIGKILL-safe via watchdog pipe: parent kill → EOF → supervisor shuts down cleanly. Exception: kill -9 of the supervisor itself leaves the **directory shell** (though pid/sock files are cleaned by defers). |
| **Builder supervisor state dir** `builder-supervisors/<ULID>/` | `os.MkdirAll` in `builder_supervisor_driver.go:106` | **Not cleaned** — only contents (pid, sock) are removed on supervisor exit. Directory itself persists. | Full ULID as directory name | **Open:** directory is always leaked after supervisor exit. R1 must include this in reap scope. |

### 3.6 — Cgroup Resources

| Kind | Owner-key format | Status |
|---|---|---|
| **Sandbox cgroup** | Unknown-and-assigned | No cgroup entries found under `/sys/fs/cgroup` for nexus3 sandboxes on this host. CloudHypervisor may create cgroups internally. R1 to verify presence, naming convention, and cleanup path before including in ResourceIndex. |

---

## 4. Ownership Rules for R1 (ResourceIndex)

### 4.1 — Full-ULID resources (directly reapable)

Resources whose filename or directory name contains the full sandbox ULID can be directly enumerated and cross-referenced against the record store. The following kinds are in this class:

- `disks/<ULID>.raw`
- `disks/<ULID>-workspace.ext4`
- `sandboxes/<ULID>/` (record itself — not a reap target, but defines the live set)
- `/run/user/1003/nexus3/sb-<ULID>.sock`
- `/run/user/1003/nexus3/sb-<ULID>.vsock`
- `/run/user/1003/nexus3/sb-<ULID>.iid`
- `builder-supervisors/<ULID>/`

**R1 algorithm for this class:**
1. Enumerate all files/dirs matching the ULID pattern.
2. Extract the ULID from the name.
3. Check whether a record exists for that ULID in the record store.
4. If no record: apply liveness gate (P-5). If gate passes (no live process): report as reclaimable.

### 4.2 — Partial-ULID resources (in-kernel; auto-reclaimed)

TAP interfaces and bridge are named with the first 5 bytes (10 hex chars) of the sandbox ULID: `nx3g-<prefix>`, `nx3h-<prefix>`, `nx3b-<prefix>`. These are in-kernel and auto-reclaimed by the kernel when the netns process group dies. R1 may include them in the inventory for completeness but they are not reap targets — the kernel handles them.

If a TAP/bridge survives past process group death (theoretically impossible in the netns design), the partial prefix allows correlation: enumerate `ip link` entries matching `nx3[ghb]-<hex>`, strip the prefix, compare against ULID prefixes of all sandbox records.

### 4.3 — No-key shared resources (not in reap scope)

The following resources have no ULID in their name and are shared across sandboxes. R1 must exclude them from the reap scope:

- `caches/<key>.ext4` — ecosystem cache disks (no ULID)
- `images/sha256/<digest>/` — builder image cache (no ULID)
- `build-cache/<hash>/` — build metadata cache (no ULID; cleanup contract unknown, R1 to document)

### 4.4 — Temporary directories without ULID (`/tmp/nxvmb-*`, `/tmp/spike-*`)

These use random numeric IDs, not ULIDs, and cannot be correlated to a sandbox by name. R1 must document them and define a correlation strategy (e.g., via process liveness: if no process holds the socket files, the dir is reclaimable regardless of sandbox correlation). Definitive scope to be settled in R1.

---

## 5. The Create Failure Window — Journaling Requirement for R2

The failure window between resource materialization and record commit is the root cause of the P13 class of leaks. The sequence in `service/create.go` is:

```
cowExt4()          → disks/<ULID>.raw      materialized
WorktreeToDisk()   → disks/<ULID>-workspace.ext4  materialized  (if workspace requested)
store.Create()     → sandboxes/<ULID>/record.json  COMMITTED
```

Any process death between the first materialization and `store.Create` leaves orphaned files with no record and no reclamation path in the current code.

**R2 MUST implement journaled creation:**
1. Write an **intent marker** (e.g., `sandboxes/<ULID>/intent.json`) BEFORE materializing any resource.
2. Materialize resources.
3. Commit the full record (`record.json`), then remove the intent marker.
4. On startup, any `intent.json` without a `record.json` is an interrupted create: the reaper can act on it.

The intent marker transforms the invisible orphan into a discoverable one. Without it, R1's `ResourceIndex` cannot distinguish "disk created before record commit" from "disk whose record was manually deleted".

---

## 6. Recover's Scope — Boundaries for R2

`recover` (`internal/core/recovery/recover.go`) owns exactly:

- **Universe:** all records returned by `st.List()`.
- **Per record:** calls `drv.Observe()` (substrate-first, under flock), then corrects the record or triggers deletion.
- **Does NOT:** scan `disks/`, `/run/user/1003/nexus3/`, or `builder-supervisors/` for resources without records.
- **Does NOT:** clean up run-dir sockets when transitioning `running → stopped` (non-delete path).
- **Does NOT:** reap workspace disks (only `.raw`).

**R2 scope (from R0-AC3):**
The confirmed gap from R0 is that `recover`'s non-delete path (running → stopped transition) does not clean up run-dir socket and IID files. R2-AC2 must either fix this (call `drv.Stop` on non-delete reconciliation, or add an explicit socket/IID cleanup step) or document why this is intentional. Both outcomes close TBR-PD-9. Silence does not.

---

## 7. Liveness Gate Definition (for R1-AC3)

The liveness gate MUST check all of the following before reporting a resource as reclaimable:

1. **Process scan — `/proc/*/cmdline`:** scan every numeric entry under `/proc` for the sandbox ULID string. Result is three-way:
   - **LIVE:** ULID found in at least one process cmdline → resource is NOT reclaimable.
   - **DEAD:** every process was scanned and ULID not found → continue to next gate.
   - **AMBIGUOUS (→ KEEP):** scan was incomplete or inconclusive. **All of the following cases are AMBIGUOUS, not DEAD:**
     - `/proc` itself cannot be listed (`os.ReadDir` failure, e.g. inside a container with a restricted proc mount).
     - A per-PID `cmdline` file cannot be opened (EACCES — process owned by another uid) or no longer exists (ENOENT — process exited between ReadDir and Open; it may have been our target mid-create).
     - A per-PID `cmdline` file was read up to the per-file limit (`maxCmdlineRead`) and the ULID was not found in that window — the ULID may appear beyond the truncation point.
   - The original implementation returned **false (DEAD)** in all three ambiguous cases. This was wrong: any of these can conceal a live sandbox. The correct contract is **AMBIGUOUS → KEEP** per P-3.
2. **CH API socket responsiveness:** attempt an HTTP GET to the CH API socket with a short timeout (≤500 ms). A successful response means a live CH process holds the sandbox. Timeout or unexpected error → ambiguous → keep.
3. **Lease/heartbeat file** (if introduced by R2): a file with a recent mtime confirms liveness even if the process PID has wrapped.

**Ambiguity resolves to keep (P-3).** If any gate is inconclusive (timeout, unexpected error, incomplete scan), the resource is not reported as reclaimable. A false keep wastes space; a false delete destroys a live sandbox. The cost asymmetry is intentional: reclaim errors are always preferable to false deletes.

---

## 8. Reap Scope Summary (for R1-AC1 through R1-AC4)

| Resource | Reapable by ULID? | Requires liveness gate? | Notes |
|---|---|---|---|
| `disks/<ULID>.raw` | YES | YES | Core reap target; P13 class |
| `disks/<ULID>-workspace.ext4` | YES | YES | Also leaked by `service.Remove` (gap D2) |
| `sandboxes/<ULID>/` (record) | YES (reaper only removes orphan intent markers; records belonging to live sandboxes are kept) | YES | Never delete a record for a live sandbox |
| `/run/user/1003/nexus3/sb-<ULID>.sock` | YES | YES | Zero allocated bytes; report for cleanup |
| `/run/user/1003/nexus3/sb-<ULID>.vsock` | YES | YES | Same |
| `/run/user/1003/nexus3/sb-<ULID>.iid` | YES | YES | Same |
| `builder-supervisors/<ULID>/` | YES | YES | Empty dirs only; confirm no live supervisor |
| TAP/bridge (`nx3[ghb]-<10hex>`) | PARTIAL (prefix only) | N/A — kernel-managed | Not a primary reap target |
| `caches/<key>.ext4` | NO | N/A | Shared; not ULID-keyed |
| `images/sha256/<digest>/` | NO | N/A | Content-addressed; Prune API |
| `build-cache/<hash>/` | NO | Unknown | Cleanup contract TBD in R1 |
| `/tmp/nxvmb-<random>/` | NO | Heuristic | No ULID; correlate via process liveness only |
| Cgroups | Unknown | Unknown | R1 to investigate |

---

## 9. Non-Goals of This Spec

- Eviction policy for builder images and ecosystem cache disks. These are explicitly excluded from the reap scope (they have no ULID and are shared).
- macOS support (spec 12).
- Herdr integration.
- Any change to `recover`'s behaviour in this slice. Changes belong to R2.

---

---

## 10. Intent-File Durability Contract (B8)

### 10.1 — What is now guaranteed

`writeCreateIntent` (`internal/core/service/intent.go`) performs the following durable-write sequence before returning:

```
os.OpenFile(intentPath, O_WRONLY|O_CREATE|O_TRUNC, 0o600)
f.Write(data)
f.Sync()      ← makes file data durable (survives ext4 journal replay)
f.Close()
dir.Sync()    ← makes the directory entry durable (file appears in diskDir listing after replay)
```

Both syncs are required:

- **File sync** guarantees that the JSON data is written to the storage medium. Without it, the page cache could hold the data in memory; a power loss before writeback drops the bytes.
- **Directory sync** guarantees that the new directory entry (the filename→inode link in `diskDir`) is flushed. On Linux, `fsync` on a file does not flush its parent directory. Without `dir.Sync()`, after power loss + ext4 journal replay the file's inode can survive while the directory entry is absent, making the file invisible to the reaper's `os.ReadDir(diskDir)` scan.

The intent must be durable **before** `cowExt4` materialises the `.raw` disk. The call ordering in `create.go` (writeCreateIntent → cowExt4) combined with the sync sequence inside `writeCreateIntent` closes the window together.

### 10.2 — What is verified by tests

`TestWriteCreateIntent_SyncIsCalled` (`internal/core/service/create_intent_test.go`) injects spy functions via the `intentFileSyncer` and `intentDirSyncer` package-level seams and asserts both are invoked. The test is **falsifying**:

- Removing the `intentFileSyncer(f)` call causes the test to report: *"writeCreateIntent did not call Sync() on the intent file"* and fail.
- Removing the `intentDirSyncer(diskDir)` call causes the test to report: *"writeCreateIntent did not sync the directory"* and fail.

Both removals were verified during implementation.

Note: reading the intent file after `writeCreateIntent` returns is **not** a valid substitute for this test. `os.ReadFile` succeeds from the kernel page cache regardless of whether `Sync` was called — the data is in memory either way. Only the seam-based spy proves the call happened.

### 10.3 — Unverified residuals

The following durability properties are **not verified** by the test suite and require additional infrastructure to test:

| Residual | Why unverified | What would be needed |
|---|---|---|
| **True power-loss durability** | `fsync` guarantees are only as strong as the storage driver and hardware honour them. Some write-cache configurations (barrier=0, volatile write caches) ignore fsync. | `dm-flakey`, a fault-injecting filesystem (libfiu, fail_make_request), or actual hardware power-cycle testing. |
| **Directory-entry durability after crash** | Verified by sync call, not by observing filesystem state post-replay. | Write intent, sync, trigger kernel panic or dm-flakey, replay journal, assert entry visible. |
| **Atomicity of the directory update** | ext4 guarantees the directory entry is written or not, but this is not exercised. | As above. |

### 10.4 — R3's process-kill test is not a power-loss proxy

R3's extreme-case leak suite (`internal/test/leak/`) simulates sudden termination by sending SIGKILL to a process group. This is a **process-level proxy** — it proves that the reaper can find orphaned resources when a process dies mid-create, but it does **not** simulate lost filesystem writes.

In R3's scenario the page cache is intact: the kernel outlives the killed process, so any write committed to the page cache before the kill is still there. A power loss kills the kernel too; only writes that reached the storage medium survive. R3's test therefore does not validate `f.Sync()` or `dir.Sync()` behaviour — it would pass whether or not those calls exist.

The falsifying test in §10.2 fills this gap at the process level (proves the call is made). The hardware-level gap is documented in §10.3 and remains open.

---

*This spec supersedes no earlier spec. It introduces new normative content alongside spec 13 (state machines) and spec 14 (compose gap map). Cross-references: TBD-PD-10 (RESOLVED by R0-AC1), TBR-PD-9 (SHARPENED by R0-AC3, RESOLVED by R2-AC2), D-PD-13 (CONFIRMED by R0-AC3 C.4).*
