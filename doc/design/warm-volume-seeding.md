# Warm volume seeding for worktree sandboxes

## Context

Worktree sandboxes maintain three disk volumes per workspace: `docker` (BuildKit
cache), `gocache`, and `gopath`. Without seeding, every new worktree sandbox
starts cold — a full `go build` takes minutes and BuildKit re-downloads base
layers. The warm-seeding feature maintains per-project warm copies at
`<volumes root>/.warm/<projectKey>/<kind>/` (implemented in
`internal/core/volumestore/warm.go`) and reflink-copies them into new volumes at
creation time. Fast clones on ext4 fall back to a full copy.

## Teardown seam (G1)

Previously each teardown path handled promotion ad hoc or not at all. All three
paths now funnel through a single function, `herdrWtRemoveVolumesFn`
(`internal/cli/cmd_herdr_plugin.go`):

1. `herdr worktree remove` — invoked via the `worktree.removed` hook calling
   `__herdr-plugin space-prune`
2. `nexus sandbox rm` — `runSandboxRmFull` in `internal/cli/cmd_sandbox.go`
3. MCP `delegate_teardown` — shells out to one of the two above

The ordering is deliberate and enforced by the **callers**, not by
`herdrWtRemoveVolumes` itself. `herdrWtRemoveVolumes` only promotes volumes
and deletes them; it has no knowledge of VM removal or binding deletion.
The callers impose the sequence:

1. **VM removal** (`svc.Remove` in `runSandboxRmFull`, or the VM is already
   gone in the space-prune path) — detaches all volumes.
2. **`herdrWtRemoveVolumes`** — promotes each volume to warm, then deletes it.
3. **Binding deletion** (`herdrSpaceTeardown` / `HerdrSpaceDelete`) — closes
   the herdr workspace and removes the binding record.

### Why this order matters

- Promotion must happen **after VM removal**: `herdrWtRemoveVolumes` calls
  `vs.PromoteToWarm`, which opens the volume's `disk.ext4`. That file must
  not be attached to a running VM (the guest holds a write lock on it).
  `svc.Remove` guarantees the VM is gone and volumes are detached before
  `herdrWtRemoveVolumes` is called.

- Promotion must happen **before binding deletion**: `herdrWtRemoveVolumes`
  looks up `HerdrSpaceGetByHandle(ctx, storeRoot, handle)` to read
  `b.RepoRoot`, which is required to derive the project key for the warm
  store path (`.warm/<projectKey>/<kind>/`). Deleting the binding first
  would make `RepoRoot` unavailable and silently skip all promotions.

Promotion failures are fail-open: errors are logged but do not abort the
removal. A failed promote leaves the old warm copy intact; the next teardown
can try again.

### `nexus rm` vs `herdr worktree remove` / space-prune (nexusstate)

`runSandboxRmFull` passes the `<slug>-nexusstate` volume name as a `keep`
argument to `herdrWtRemoveVolumes`, so nested nexus state survives `nexus rm`.
`nexus rm` also deletes the herdr binding (`herdrSpaceTeardown`). Because
space-prune acts on existing bindings only, it cannot reach a nexusstate volume
whose binding was already deleted. After `nexus rm`, the volume remains until
either the same handle gets a new binding that is later torn down, or it is
removed explicitly:

    nexus volume rm <slug>-nexusstate

## Retention policy (G2)

The policy lives in `internal/core/volumestore/warm.go`. Key types and vars:

| Name | Value | Purpose |
|---|---|---|
| `WarmShrinkRatio` | 0.5 | replace-guard threshold |
| `WarmStaleAfter` | 7 days | staleness bound for replace guard |
| `WarmMaxCopyBytes` | 16 GiB | per-copy size cap |
| `WarmMaxTotalBytes` | 64 GiB | total `.warm/` size cap |

These are package vars today; user-configurable thresholds are a follow-up
(see below).

`WarmMeta` records `AllocatedBytes` — the on-disk allocated size of the
volume, not the logical sparse size. Logical size is identical across copies
(all volumes are created at the same declared capacity), so it gives no
signal; allocated bytes reflect how full the cache actually is.

**Replace guard.** An existing warm copy is NOT replaced when both conditions
hold:

- candidate's `AllocatedBytes` < `WarmShrinkRatio` × existing `AllocatedBytes`
- existing copy's `PromotedAt` < `WarmStaleAfter` ago (i.e., it is still fresh)

When rejected, `Promote` returns `ErrWarmNotReplaced`.

Rationale: the incident that motivated this guard had an auto-spawned sandbox
whose `docker` volume held 133 MB (never built anything) overwrite a 3.7 GB
warm copy under a latest-teardown-wins policy. A near-empty volume almost
always means a short-lived or never-built sandbox, not a fresher cache. The
staleness bound (`WarmStaleAfter`) prevents a large-but-obsolete copy from
pinning forever: once the copy ages past 7 days the guard disengages, allowing
any new candidate — even a smaller one — to replace it.

Legacy `WarmMeta` records without `AllocatedBytes` (written before this
field existed): the allocated size is measured from `disk.ext4` via `stat`
at promote time.

**Per-copy cap.** Candidates larger than `WarmMaxCopyBytes` (16 GiB) are
rejected with `ErrWarmTooLarge`. This guards against a runaway BuildKit cache
filling the warm store.

**Total cap and eviction.** After each successful promote, the store checks
whether the total allocated bytes across all `.warm/` copies exceeds
`WarmMaxTotalBytes` (64 GiB). If so, it evicts whole `(projectKey, kind)`
copies oldest-`PromotedAt` first until the total drops below the cap. The
just-promoted copy is never evicted in the same pass. Eviction failures are
logged and do not abort the promote. Eviction takes no seed lock: it can
unlink a copy that `SeedFromWarm` is reading, because an open file descriptor
keeps the inode alive until the seed copy closes it (Linux unlink semantics).

**Rejected alternatives:**

- *LRU-by-seed-time*: requires meta writes on the seed path, adding churn and
  a write-lock on every sandbox creation.
- *Content markers / hashes*: prohibitively expensive on multi-GB ext4 images.
- *Always-newest (latest-teardown-wins)*: the incident above.

## Follow-ups

- Expose thresholds (`WarmShrinkRatio`, `WarmStaleAfter`, `WarmMaxCopyBytes`,
  `WarmMaxTotalBytes`) via `.nexus/config.yaml` or env vars so projects with
  unusual cache profiles can tune them without a binary rebuild.
- `nexus volume prune --warm` dry-run output should show allocated bytes
  alongside logical size so operators can estimate reclaim before `--apply`.
- Verify reflink fallback on ext4 (no `FICLONE` support) measures actual copy
  throughput and consider async promotion for very large volumes.
