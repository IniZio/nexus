# Resource Inventory Audit — August 2026

**Date:** 2026-08-15  
**Branch:** milestone-a-agent-sandbox  
**Scope:** R0-AC1, R0-AC2, R0-AC3 (read-only; no code changes)  
**Covers:** `docs/site/operations/resource-lifecycle.md` inputs  

---

## A — Complete Resource-Kind Inventory (R0-AC1)

Resolves TBD-PD-10. Every cell is filled or explicitly marked **unknown-and-assigned**.

> **Owner-key definition:** a parseable, sandbox-ULID-encoded string present in the resource's name or a sidecar that lets a scanner identify the owning sandbox without consulting any record.

### A.1 — Primary Disk Resources

| # | Kind | Creator (file:func) | Freer (file:func) | Owner Key | Abnormal Termination |
|---|------|---------------------|-------------------|-----------|----------------------|
| D1 | **Ext4 sandbox disk copy (S-COW)** | `service/create.go:335` `cowExt4` | `service/disk.go:14` `ReapDiskCopy` called from `service.Remove:628` and `recovery/recover.go:309` (--rm path only) | YES: `~/.local/state/nexus3/disks/<ULID>.raw` | ORPHANED if process killed between `cowExt4` and `store.Create`. No record exists yet; resource is invisible to `recover` forever. |
| D2 | **Workspace ext4 disk** | `service/create.go:383` `WorktreeToDisk` | Deferred `os.Remove` on create failure only. `service.Remove` does **not** call `ReapDiskCopy` for `-workspace.ext4` — the `.raw` removal at line 628 misses it. | YES: `~/.local/state/nexus3/disks/<ULID>-workspace.ext4` | ORPHANED on create kill-9 (defer does not run). Also LEAKED on successful `service.Remove` (gap: remove path does not clean `-workspace.ext4`). |
| D3 | **Builder cache disk** | `builder/cachedisk.go:71` `EnsureCacheDisk` | No automatic eviction; shared across all sandboxes using the same ecosystem. Must be manually pruned. | **NO:** named by ecosystem key (`caches/buildkit.ext4`). No ULID. | Survives indefinitely (intentional shared cache). |
| D4 | **Builder image artifact** | `core/image/cache.go:141` `image.Cache.Put` | `core/image/cache.go:298` `image.Cache.Prune` (explicit only; no automatic eviction). | **NO:** named by content digest (`images/sha256/<sha256hex>/artifact` + `meta.json`). No ULID. | Survives until explicit Prune with a referenced-set that omits this digest. |
| D5 | **Build-cache entries** | Unknown (builder/buildkit metadata, content hash–named dirs) | Unknown | **NO:** named by content hash (`build-cache/<sha256hex>/`). Orphaned `.lock` files observed without parent directories. | Unknown-and-assigned: R1 to investigate cleanup contract and owner relationship. |
| D6 | **Snapshot directory** | `core/artifact/snapshot.go:42-65` | `artifact.DeleteSnapshot` or equivalent | Partial: snapshot ID in directory name; ULID relationship unknown | Currently empty on this host. Unknown-and-assigned: R1 to verify naming encodes sandbox ULID. |

### A.2 — Sandbox Record

| # | Kind | Creator | Freer | Owner Key | Abnormal Termination |
|---|------|---------|-------|-----------|----------------------|
| R1 | **Sandbox record** | `service/create.go:410` `store.Create` | `service/service.go:620` `store.Delete` via `service.Remove` | YES: `~/.local/state/nexus3/sandboxes/<ULID>/record.json` | Survives intentionally. The record IS the handle on all other resources. `recover` uses it as its universe. |

### A.3 — Network Resources

| # | Kind | Creator | Freer | Owner Key | Abnormal Termination |
|---|------|---------|-------|-----------|----------------------|
| N1 | **Network namespace** (CLONE_NEWNET) | `driver/cloudhypervisor/ch_netns.go:136` `StartNetnsRuntime` → `clone()` syscall | Kernel: automatic reclamation when last process in the namespace exits | **None.** In-kernel; no named filesystem path. Process group ID is the transient handle. | Kernel reclaims automatically even on SIGKILL; no orphan possible for the namespace itself. |
| N2 | **Guest TAP interface** (`nx3g-<10hex>`) | `ch_netns.go:333` `createTapBridge` inside netns child | Kernel auto-reclaims with netns; `ch_net.go:292` `deleteTapBridge` is the explicit fallback | **PARTIAL:** first 5 bytes of ULID encoded as 10 lowercase hex chars in the interface name (`nx3g-<first-5B-of-ULID>`). Not the full ULID; collisions are theoretically possible but rare given ULID monotonicity. | Kernel auto-reclaims when netns child process group dies (even SIGKILL). |
| N3 | **Host TAP interface** (`nx3h-<10hex>`) | Same as N2 | Same as N2 | **PARTIAL:** same 10-hex suffix as N2 | Same as N2 |
| N4 | **L2 bridge interface** (`nx3b-<10hex>`) | Same as N2 | Same as N2 | **PARTIAL:** same 10-hex suffix as N2 | Same as N2 |
| N5 | **CloudHypervisor VMM process** | `ch_netns.go:338` `spawnVMM` inside netns child | `teardownSandboxNet` → `NetnsRuntime.Stop()` → `Kill(-childPgid, SIGKILL)` | YES: child process group ID derived from `cmd.Process.Pid`; stored in driver's in-process `nets` map | Process may remain orphaned if parent nexus3 is killed before `teardownSandboxNet`. Next `Observe` call reports Absent (a VMM with no VM). `recover` then resolves record to stopped but does **not** kill the orphan VMM — that requires an explicit `drv.Stop`. |

### A.4 — Unix Socket Files

All socket files in `/run/user/1003/nexus3/` and `/tmp/nxvmb-*/`:

| # | Kind | Creator | Freer | Owner Key | Abnormal Termination |
|---|------|---------|-------|-----------|----------------------|
| S1 | **CH API socket** | `driver.Start` → `StartNetnsRuntime`; socket path passed to CH | `driver.Stop` → `clearState()` → `os.Remove(socketPath)` (driver.go:425) | YES: `/run/user/1003/nexus3/sb-<ULID>.sock` | ORPHANED if process killed before `clearState`. File survives with no listener; next `Start` pre-flight should remove stale socket. |
| S2 | **VSock socket file** | `driver.Start` | `driver.Stop` → `clearState()` → `os.Remove(vsockPath)` (driver.go:426) | YES: `/run/user/1003/nexus3/sb-<ULID>.vsock` | ORPHANED if process killed before `clearState`. |
| S3 | **IID file** | `driver.Start` (presumably; stores instance_id) | **Unknown-and-assigned:** not cleaned by `recover` non-delete path or `service.Remove` directly. R1 to identify the cleanup site and add it if missing. | YES: `/run/user/1003/nexus3/sb-<ULID>.iid` | ORPHANED: 4 iid files observed on this host for 3 sandboxes with no records and 1 with no live process. |

### A.5 — Process Resources

| # | Kind | Creator | Freer | Owner Key | Abnormal Termination |
|---|------|---------|-------|-----------|----------------------|
| P1 | **Detached perimeter supervisor** (orca path) | `supervisor.SpawnDetached` called from `orca/create` after VM ready | `service.Remove:598` `closeSupervisor` sends `/supervisor/stop`, then waits for exit | PID stored in `domain.Sandbox.SupervisorPID` (record field); supervisor socket in `SupervisorSock`. No named path encoding ULID. | Orphaned process if parent nexus3 exits unexpectedly. Detected via `kill(SupervisorPID, 0)` → ESRCH = gone. `recover` does not perform this check; R1/R2 to address. |
| P2 | **Builder supervisor process** (ephemeral) | `cli/builder_supervisor_driver.go:133` `supervisor.SpawnDetached(Ephemeral:true)` | `supervisorBuilderDriver.Stop` → `supervisor.StopSupervisor` + `WaitForExit` | YES: `builder-supervisors/<ULID>/` state directory (ULID-named). PID file and socket inside dir. | SIGKILL safety via watchdog pipe: CLI kill causes EOF on pipe → supervisor shuts down cleanly. On abrupt death (kill -9 of supervisor itself): pid file and sock removed by supervisor's defer, but the ULID-named **directory is not removed**. |

### A.6 — Cgroups

| # | Kind | Creator | Freer | Owner Key | Abnormal Termination |
|---|------|---------|-------|-----------|----------------------|
| C1 | **Sandbox cgroup** | Unknown-and-assigned: no cgroup entries found under `/sys/fs/cgroup` for nexus3 on this host. CloudHypervisor may or may not create cgroups; R1 to verify. | Unknown | Unknown | Unknown |

---

## B — Census of Currently-Leaked Resources (R0-AC2)

### B.1 — P13 Baseline (Evidence of Record)

**The P13 baseline remains the authoritative evidence of record, regardless of whether the one-off deletion (TBD-PD-12) has already run.**

The one-off deletion does **not** close the underlying defect. The structural failure window (disk materialized before record committed; kill-9 produces orphans invisible to `recover`) is unchanged. R0 measures; R1 and R2 fix.

| Measured (2026-08-14) | Quantity | Allocated | Apparent | Notes |
|-----------------------|----------|-----------|----------|-------|
| Large `.raw` files in `disks/` | 5 | 11.2 GiB | ~30 GiB | Named with sandbox ULIDs whose records were already deleted. |
| Small orphan `.raw` files (≤16 bytes real data) | 829 | ~0 (sparse) | ~70 GiB | Images created via `os.Truncate` before record commit; abandoned on interrupted create. Signature of the kill-9 window. |
| **P13 total** | **834 files** | **~11.2 GiB** | **~101 GiB** | — |

### B.2 — Current Host State (Post-One-Off Deletion, 2026-08-15)

All sizes: **allocated** unless marked `(apparent)`.

#### Disks directory (`~/.local/state/nexus3/disks/`)

| File | Allocated | Apparent | Status | Verdict |
|------|-----------|----------|--------|---------|
| `sb-06FZZX7V8XZM12YE7VTR7T8168.raw` | 120 MiB | 4 GiB | Record exists (state=`running`); no live CH process | Held by stale record. After `recover`: transitions to `stopped`, disk retained for potential restart. Not reclaimed without `nexus3 sandbox rm`. |

**Total allocated in disks/: 120 MiB (apparent: 4 GiB).**  
The P13 large orphans have been manually deleted. The defect that created them is not fixed.

#### Socket files (`/run/user/1003/nexus3/`)

Ten files observed: 3 `.iid` files (Aug 12), 3 `.sock` files (Aug 12–14), 2 `.vsock` files (Aug 12), and the Aug 14 uni5 pair, plus 1 stale `.iid` (Aug 12 `sb-06FZCZC4DDVYN063QADZZ3V0PW`) with no sock/vsock sibling.

| Sandbox ULID | Files present | Record exists? | Live process? | Verdict |
|---|---|---|---|---|
| `sb-06FZ9SJ2YXRAHE4C596NAP7ND0` | `.iid`, `.sock` | **NO** | NO | LEAKED: no record, no process |
| `sb-06FZCZC4DDVYN063QADZZ3V0PW` | `.iid` only | **NO** | NO | LEAKED: no record, no process |
| `sb-06FZD3WRZNYYV2CD9KATFE0J38` | `.iid`, `.sock`, `.vsock` | **NO** | NO | LEAKED: no record, no process |
| `sb-06FZZX7V8XZM12YE7VTR7T8168` (uni5) | `.iid`, `.sock`, `.vsock` | YES (`running`) | NO | STALE: record present but no live process; `recover` transitions record to `stopped` but does NOT clean these socket files (see C below) |

Socket files carry 0 bytes allocated (AF_UNIX socket nodes; negligible storage). The operational impact is invisible sockets with no listener that consume directory entries.

#### Builder-supervisor directories (`~/.local/state/nexus3/builder-supervisors/`)

6 ULID-named empty directories:
`sb-06FZZS2HTXTYBFAF6CZVV0Z8RW`, `sb-06FZZSE3F1V4NF7ET98QMA2P88`, `sb-06FZZTA4BHT3SE8SNPV978SYJ4`, `sb-06FZZVSHASWRH2WMVSTY6B7VV4`, `sb-06FZZW8B99SY9DDG5M31FG5GBW`, `sb-06FZZWWMYNRSBC9DPGXF7AAEZ0`.

None of these ULIDs has a sandbox record in `sandboxes/`. The supervisor process cleaned up its own files (pid, sock) inside the dir on normal exit, but the directory shell was not removed. Allocated: ~24 KiB (6 × 4096-byte directory entries). Not individually dangerous but invisible to `recover`.

#### Tmp directories (`/tmp/nxvmb-*`, `/tmp/spike-ch-*`)

| Dir pattern | Count | Created | Allocated | Content | Verdict |
|-------------|-------|---------|-----------|---------|---------|
| `/tmp/nxvmb-<random>/` | 3 | Jul 23 | 48 KiB | Orphaned sockets (ch-api, vsock, passt-vhost) and log files | LEAKED: no live processes, random numeric IDs (no ULID, cannot correlate to sandbox) |
| `/tmp/spike-ch-<random>/` | 4 | Aug 8 | ~16 KiB | Orphaned ch-api sockets ± serial logs | LEAKED: test/spike artifacts with no ULID |

These dirs have no ULID-based name and cannot be correlated to any sandbox by the reaper without additional heuristics.

#### Shared caches (not leaked, but noted for completeness)

| Resource | Location | Allocated | Apparent | Notes |
|---|---|---|---|---|
| Builder cache disk | `caches/buildkit.ext4` | 9.7 GiB | 10 GiB | Shared; no ULID; intentionally persistent |
| Builder images | `images/sha256/` (16 entries) | 78 GiB | 79 GiB | Content-addressed; no ULID; intentionally persistent; Prune removes unreferenced |
| Build-cache | `build-cache/` (42 entries) | 92 KiB | 1 KiB (apparent) | Sparse metadata dirs; cleanup contract unknown |
| Snapshot dir | `snapshots/` | 0 | 0 | Empty |

### B.3 — Leaked Bytes Summary (Current, Allocated)

| Kind | Allocated | Owner-key present? |
|------|-----------|-------------------|
| Ext4 sandbox disk (uni5, stale `running` record) | 120 MiB | YES |
| Run-dir socket/iid files (3 no-record, 1 stale-process) | ~0 bytes | YES |
| Builder-supervisor dirs (6 empty) | 24 KiB | YES |
| `/tmp/nxvmb-*` dirs (3) | 48 KiB | NO |
| `/tmp/spike-ch-*` dirs (4) | 16 KiB | NO |
| **Totals** | **~120 MiB** | — |

**The one-off deletion reclaimed the P13 large orphans (11.2 GiB). The defect that produced them is not fixed.**

---

## C — Recover Characterization (R0-AC3)

Source: `internal/core/recovery/recover.go` (read in full).

### C.1 — What `recover` actually reconciles

`recover.Recover()` calls `st.List()` to enumerate all sandbox records, then calls `recoverByID()` for each. **It never inventories host resources directly.** Its universe is the record store.

For each record, `recoverByID()`:
1. Calls `drv.Observe(ctx, id)` **inside** the per-sandbox exclusive flock (`store.Update` callback). The substrate is authoritative.
2. Branches on the observed VM state — NOT on the stored record state — as the primary branch:

| Observed state | Action |
|---|---|
| `Running` | `applyAdopt`: corrects record state and instanceID to match. Clears stale removal marker. **Outcome: adopted.** |
| `Paused` | `applyAdopt`: same as Running. **Outcome: adopted.** |
| `Absent` | `applyAbsent`: branches on record state (only after Absent is confirmed). |
| `Unknown` / error | No action. **Outcome: indeterminate.** |

For `Absent` + various record states:

| Record state | RemovalMarker | RemoveOnExit | Action |
|---|---|---|---|
| Any | `true` | — | No action. **Outcome: terminal** (manual cleanup required). |
| `Running` | `false` | `false` | Transition to `stopped` (StopReason=memory_lost). **Disk retained.** Run-dir sockets NOT cleaned. |
| `Paused` | `false` | `false` | Transition to `stopped` (StopReason=memory_lost). |
| `Running` | `false` | `true` | Set RemovalMarker, then call `drv.Stop` + `st.Delete` + `ReapDiskCopy`. **Outcome: removed.** |
| `Created`, `Stopped`, `Error` | `false` | any | No action. **Outcome: unchanged.** |

### C.2 — The P15 / uni5 finding

**Label: CONFIRMED (recover behaviour was correct in P15; the gap is structural, not a code defect in recover itself).**

In the P15 incident the operator believed uni5 had no backing process. Investigation found PID 1451104 alive. `recover` (had it been run) would have called `drv.Observe()` → result Running → `applyAdopt` → record unchanged. This is correct. The misread was in manual inspection, not in `recover`.

**Current state (2026-08-15):** uni5's record says `state=running`, but no CH process is alive. If `nexus3 recover` were run now, `drv.Observe()` would return Absent → `applyAbsent` → running+absent+no-removal-marker → transitions to `stopped` (memory_lost). The disk file (`sb-06FZZX7V8XZM12YE7VTR7T8168.raw`) would be **retained** (not reaped) because the non-delete path does not call `ReapDiskCopy`. The three socket/iid files in `/run/user/1003/nexus3/` for uni5 would also **remain**, because `recover`'s non-delete path does not call `drv.Stop` (which runs `clearState` → `os.Remove`).

### C.3 — Structural gaps in `recover`'s scope

The following resource classes are permanently outside `recover`'s scope:

| Gap | Description | Classification |
|-----|-------------|----------------|
| **No-record orphans** | Resources (disks, run-dir files, builder-supervisor dirs) that were materialized before `store.Create` ran are invisible to `recover`. `st.List()` only sees committed records. | Structural — requires `ResourceIndex` (R1) to address. |
| **Run-dir cleanup on non-delete recovery** | When `recover` transitions `running → stopped`, it does NOT call `drv.Stop`. Socket and IID files in `/run/user/1003/nexus3/` are not cleaned up. | Gap in recover — scope of R2-AC2 to decide whether to fix or document. |
| **Workspace disk reaping** | `service.Remove` calls `ReapDiskCopy` which removes `<ULID>.raw` but NOT `<ULID>-workspace.ext4`. Both are in `disks/`. | Gap — not addressed by recover; workspace disk can be leaked by successful Remove. |
| **Builder supervisor dirs** | Empty ULID-named dirs in `builder-supervisors/` after supervisor exit are not cleaned up. `recover` never touches that directory. | Gap — requires reaper (R1). |

### C.4 — D-PD-13 verdict

**CONFIRMED: "the record is the only handle."**

Evidence:
- `recover.Recover()` calls `st.List()` as its only universe-enumeration step. No directory scan of `disks/`, `/run/user/1003/nexus3/`, or `builder-supervisors/` is performed.
- A resource with no record is permanently invisible to every current reconciliation path.
- The P13 class (829 tiny orphans) were created by `os.Truncate` before `store.Create`; they had no record and therefore no reclamation path.
- Patching individual create failure sites cannot close this class: any unhandled panic or SIGKILL in the window between disk materialization and record commit produces a new orphan. The window is structurally present in `create.go:335–410`.

**Implication:** R1 must build a `ResourceIndex` that enumerates host resources by kind WITHOUT consulting any record, then compares against the record store to identify orphans.

**Caveat on in-kernel resources:** TAP interfaces, bridge interfaces, and network namespaces are auto-reclaimed by the kernel when the CH process group dies — even under SIGKILL. These three kinds are NOT subject to the "record is the only handle" problem; the kernel is the handle. Their names encode a partial ULID (first 5 bytes = 10 hex), sufficient for correlation but not uniquely the full ULID.

---

*This document is the input for `docs/site/operations/resource-lifecycle.md` and is the evidence of record for TBD-PD-10, TBD-PD-12 (baseline), and TBR-PD-9.*
