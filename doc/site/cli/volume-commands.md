---
title: "Volume commands"
description: "Reference for nexus volume verbs: create, ls, rm, prune"
---

# Volume commands

> Named volumes are user-owned resources that persist independently of any sandbox.

A named volume stores data in a dedicated directory at `<stateRoot>/volumes/<name>/`. Volumes survive `nexus rm` — the sandbox is detached from the volume but the backing files are never deleted by nexus. Use `nexus volume rm` or `nexus volume prune` to reclaim them explicitly.

The reaper (`nexus reap`) never touches the volumes directory. This is a structural guarantee: `ResourceIndex.List()` scans only `<stateRoot>/disks/` and `<stateRoot>/sockets/`; no code path from the reaper reaches `<stateRoot>/volumes/`.

## nexus volume create

Create a named volume.

```
nexus volume create <name> [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--kind <dir\|disk>` | string | `disk` | Volume kind. `disk` = sparse ext4 block image; `dir` = host directory served via virtiofs |
| `--size <bytes>` | int | 10 GiB | Backing disk size in bytes (kind=disk only). Accepts an integer byte count; common shorthand (e.g. `10g`) must be provided as bytes (`10737418240`) |
| `--path <host-dir>` | string | managed | Pin the volume to a specific host directory path (kind=dir only). Useful when the host directory already exists (e.g. a project working tree) |

`create` is idempotent: if the volume already exists with compatible configuration, it is a no-op. A kind mismatch on an existing volume returns an error.

Volume names may contain letters, digits, hyphens, and underscores. The agent skill convention for agent-generated volumes is `<projectslug>-<dirname>` (e.g. `myapp-node_modules`, `myapp-target`).

## nexus volume ls

List volumes, optionally filtered to those attached to a specific sandbox.

```
nexus volume ls [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--sandbox <id>` | string | — | Show only volumes currently attached to this sandbox ID |

Output columns: **NAME**, **KIND**, **SIZE** (kind=disk only), **ATTACHED** (count of current attachments), **CREATED**.

## nexus volume rm

Delete a named volume and its backing files.

```
nexus volume rm <name>
```

`rm` refuses if the volume is currently attached to any running or paused sandbox. Stop or remove the sandbox first.

## nexus volume prune

Identify and optionally delete orphaned or detached volumes, and manage warm volume copies.

```
nexus volume prune [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--apply` | bool | false | Perform deletions (default: dry-run — report only) |
| `--include-detached` | bool | false | Also delete volumes that have no current attachments (requires `--apply`) |
| `--warm` | bool | false | Target warm copies under `.warm/` instead of user-visible volumes |
| `--project <key>` | string | — | Limit `--warm` sweep to a single project key (combined with `--warm`) |

`prune` without `--apply` prints what would be deleted without removing anything. With `--apply` it removes backing files for volumes whose meta.json exists but whose backing file (kind=disk) or data directory (kind=dir) is absent.

Adding `--include-detached` extends the sweep to any volume that has no current attachment to a known sandbox record, regardless of whether its backing file exists.

### Pruning warm copies

```sh
nexus volume prune --warm                      # list all warm copies (dry-run)
nexus volume prune --warm --apply              # delete all warm copies
nexus volume prune --warm --project <key>      # list copies for one project
nexus volume prune --warm --project <key> --apply  # delete copies for one project
```

Warm copies are stored under `~/.local/state/nexus/volumes/.warm/` and are never shown by `nexus volume ls`. They accumulate as worktrees are promoted at teardown. Use `--warm` to recover disk space when a project is no longer active.

## Warm volume seeding

> **Why this exists.** Each herdr worktree sandbox mounts its own private disk volumes — `<slug>-docker` (backing `/var/lib/docker`, only when the repo has a `.nexus/Containerfile`), `<slug>-gocache` (backing `/root/.cache`), and `<slug>-gopath` (backing `/root/go`). Before warm seeding, every new worktree started with blank volumes. Timed on a representative compose-stack project (agentic-artifacts, 2026-09-26), a cold worktree spends ~120–150 s re-pulling images, re-running `docker compose build` (apt, npm ci, layer export), and waiting for healthchecks — before a developer can write a single line. For Go-only projects the gap is narrower but still significant: cold `go mod download` + `go build` costs ~100 s per worktree when GOMODCACHE starts empty.

Warm seeding eliminates this by keeping a project-scoped warm copy of each volume and reflink-cloning it into every new worktree at creation time.

### Project key

The warm store is keyed by *project*, not by branch or sandbox handle, so all worktrees of the same repository share one set of warm copies. The project key is derived from two stable facts:

- The **readable repository basename** (e.g. `nexus`, `agentic-artifacts`).
- A **short hash of the absolute path returned by `git --git-common-dir`** — the common `.git` directory shared by all worktrees of a single clone. Two clones of the same repo on different paths get independent warm stores; two repos with the same name on different paths do not collide.

### Warm store location

```
~/.local/state/nexus/volumes/.warm/<projectKey>/<kind>/
  disk.ext4     # reflink clone promoted from the last worktree that was torn down
  meta.json     # source volume name, promoted-at timestamp, size
```

Kinds stored: `docker`, `gocache`, `gopath`. Kinds never stored: `agentcfg`, `nexusstate` (credentials and per-workspace daemon state must never leak across worktrees). Warm copies do not appear in `nexus volume ls` and cannot be attached directly.

### Seed on create

When a new worktree's per-handle volume (`<slug>-docker`, `<slug>-gocache`, `<slug>-gopath`) does not yet exist and a warm copy is available, nexus creates the volume by copying the warm `disk.ext4` rather than formatting an empty image. Existing volumes are never touched.

Copy strategy (tried in order):

1. **Reflink (`FICLONE`)** — instant, copy-on-write; requires XFS with the `reflink=1` mount option or btrfs. The new volume shares unmodified blocks with the warm source and diverges only as the sandbox writes data.
2. **Sparse data copy (`SEEK_DATA`/`SEEK_HOLE` + `copy_file_range`)** — used when the filesystem cannot reflink (e.g. plain ext4, which is the common case for the default `~/.local/state/nexus` location). Only allocated extents are copied, so a warm docker disk with several GiB of real data typically copies in seconds — still far faster than a ~120–150 s re-pull/rebuild. The copy consumes real disk space (roughly the used bytes of the source volume, per project and kind); use `nexus volume prune --warm --apply` to reclaim it when a project is no longer active.

Only a genuine copy error (I/O failure, out of space) falls back to an empty volume; this is logged and the create proceeds. Which method was used and how long it took is also logged.

### Promote on teardown

When a worktree's volumes are removed — either because the worktree was removed (`worktree.removed` hook), `nexus herdr prune --apply` resolves a stale binding, or `nexus rm` is run — nexus promotes each eligible volume to the warm store. Promotion copies `disk.ext4` into a temp file under `.warm/<projectKey>/<kind>/`, fsyncs it, then atomically renames it into place. A promotion failure never blocks teardown.

**Replace guard.** An incoming candidate replaces an existing warm copy only when the existing copy is older than 7 days, OR the existing copy's allocated bytes exceed its kind's cap, OR the candidate's allocated bytes are no larger than the existing copy's and at least half of them (`WarmShrinkRatio`), so a near-empty sandbox cannot wipe a useful seed. Sandboxes are seeded from the warm copy, so a larger candidate is the seed plus churn, and accepting it made seeds snowball. When rejected, `Promote` returns `ErrWarmNotReplaced`. Caps on allocated bytes: docker seeds `WarmMaxDockerCopyBytes` (4 GiB), other kinds `WarmMaxCopyBytes` (16 GiB), plus a total-store cap (`WarmMaxTotalBytes`, 64 GiB) with oldest-first eviction. The same two-step copy strategy applies: reflink (`FICLONE`) if the filesystem supports it; otherwise a sparse data copy via `SEEK_DATA`/`SEEK_HOLE` + `copy_file_range`.

`nexus rm` promotes and deletes the worktree volumes as described above, then tears down the herdr binding. **Exception:** the `<slug>-nexusstate` volume (nested nexus state) is intentionally kept by `nexus rm`. Because `nexus rm` also deletes the herdr binding, space-prune (`nexus herdr prune --apply`) has no binding left to act on and cannot reach the volume. After `nexus rm`, a kept `<slug>-nexusstate` volume is removed only if the same handle gets a new binding that is later torn down, or by running `nexus volume rm <slug>-nexusstate` by hand.

### What carries over

A seeded worktree inherits the state of the last worktree that was fully torn down for that project:

- **Docker images** — all images the previous worktree pulled or built are immediately available; no re-pull or re-build needed.
- **BuildKit build cache** — layer cache is intact, so unchanged Dockerfile steps are cache hits.
- **Compose named volumes** — these live inside `/var/lib/docker` and are included in the `<slug>-docker` volume. This means a seeded worktree inherits the previous worktree's database state (schema migrations already applied, seed data present). If you need a clean database, run `docker compose down -v` inside the sandbox before starting services.
- **Go build and module caches** — `GOCACHE` and `GOMODCACHE` carry over; the first `go build` in a new worktree is incremental rather than cold.

### Opt-out

To disable seeding and promotion for a repository, set in `.nexus/config.yaml`:

```yaml
volumes:
  seed: false
```

This disables both directions: the new worktree starts with empty volumes and teardown no longer promotes. For a one-off override without editing the config file, set the environment variable before the create or prune command:

```sh
NEXUS_NO_VOLUME_SEED=1 nexus herdr worktree-sandbox <ws-id>
```

## Attach rules and concurrency

| Kind | Concurrent RW | Concurrent RO |
|------|--------------|---------------|
| `disk` | 1 (exclusive) | unlimited |
| `dir` | unlimited | unlimited |

A kind=disk volume can be mounted read-write in at most one sandbox at a time. Attempting a second read-write attach returns an error. Multiple sandboxes may attach the same kind=disk volume read-only simultaneously.

## Volume lifecycle with fork <Badge type="info" text="backlogged" />

`nexus fork` and `nexus snapshot` are **refused** when the parent sandbox has **any** attached named volume, regardless of kind. This is an interim gate (D-PD-96, TBR-PD-15) — the correct fork-with-volumes and snapshot-with-volumes semantics are pending design and have not been ratified yet. The refusal exists to prevent silent data hazards until that design lands:

- **kind=disk**: two VMs sharing the same ext4 image read-write simultaneously corrupt it; a per-child copy leaks permanently into unreclaimed storage.
- **kind=dir**: two VMs sharing the same host directory over virtiofs get a single mutable view — fork isolation does not exist.

There is no hot-detach command (TBD-SD2-LIVE-3 is not yet built). To run N parallel sandboxes that each need a volume, use independent `nexus create` calls instead of forking.

## Hot-attach follow-on <Badge type="danger" text="not built" />

`nexus sandbox volume-attach <sandbox-id> <name>:<guest-path>[:<options>]` hot-plugs a named volume into a running sandbox without rebooting it. This requires `virtio-mem` hotplug support in the guest kernel and is gated on TBD-SD2-LIVE-3.

## See also

- [`--mount-named` flag on `nexus create`](/cli/sandbox-commands#named-volumes)
- [Resource lifecycle — named volumes](/operations/resource-lifecycle#named-volumes)
- [AI agents — volume-config skill](/ai-agents)
