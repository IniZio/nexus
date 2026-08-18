---
title: "Volume commands"
description: "Reference for nexus3 volume verbs: create, ls, rm, prune"
---

# Volume commands

> Named volumes are user-owned resources that persist independently of any sandbox.

A named volume stores data in a dedicated directory at `<stateRoot>/volumes/<name>/`. Volumes survive `nexus3 rm` — the sandbox is detached from the volume but the backing files are never deleted by nexus3. Use `nexus3 volume rm` or `nexus3 volume prune` to reclaim them explicitly.

The reaper (`nexus3 reap`) never touches the volumes directory. This is a structural guarantee: `ResourceIndex.List()` scans only `<stateRoot>/disks/` and `<stateRoot>/sockets/`; no code path from the reaper reaches `<stateRoot>/volumes/`.

## nexus3 volume create

Create a named volume.

```
nexus3 volume create <name> [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--kind <dir\|disk>` | string | `disk` | Volume kind. `disk` = sparse ext4 block image; `dir` = host directory served via virtiofs |
| `--size <bytes>` | int | 10 GiB | Backing disk size in bytes (kind=disk only). Accepts an integer byte count; common shorthand (e.g. `10g`) must be provided as bytes (`10737418240`) |
| `--path <host-dir>` | string | managed | Pin the volume to a specific host directory path (kind=dir only). Useful when the host directory already exists (e.g. a project working tree) |

`create` is idempotent: if the volume already exists with compatible configuration, it is a no-op. A kind mismatch on an existing volume returns an error.

Volume names may contain letters, digits, hyphens, and underscores. The agent skill convention for agent-generated volumes is `<projectslug>-<dirname>` (e.g. `myapp-node_modules`, `myapp-target`).

## nexus3 volume ls

List volumes, optionally filtered to those attached to a specific sandbox.

```
nexus3 volume ls [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--sandbox <id>` | string | — | Show only volumes currently attached to this sandbox ID |

Output columns: **NAME**, **KIND**, **SIZE** (kind=disk only), **ATTACHED** (count of current attachments), **CREATED**.

## nexus3 volume rm

Delete a named volume and its backing files.

```
nexus3 volume rm <name>
```

`rm` refuses if the volume is currently attached to any running or paused sandbox. Stop or remove the sandbox first.

## nexus3 volume prune

Identify and optionally delete orphaned or detached volumes.

```
nexus3 volume prune [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--apply` | bool | false | Perform deletions (default: dry-run — report only) |
| `--include-detached` | bool | false | Also delete volumes that have no current attachments (requires `--apply`) |

`prune` without `--apply` prints what would be deleted without removing anything. With `--apply` it removes backing files for volumes whose meta.json exists but whose backing file (kind=disk) or data directory (kind=dir) is absent.

Adding `--include-detached` extends the sweep to any volume that has no current attachment to a known sandbox record, regardless of whether its backing file exists.

## Attach rules and concurrency

| Kind | Concurrent RW | Concurrent RO |
|------|--------------|---------------|
| `disk` | 1 (exclusive) | unlimited |
| `dir` | unlimited | unlimited |

A kind=disk volume can be mounted read-write in at most one sandbox at a time. Attempting a second read-write attach returns an error. Multiple sandboxes may attach the same kind=disk volume read-only simultaneously.

## Volume lifecycle with fork <Badge type="info" text="backlogged" />

`nexus3 fork` and `nexus3 snapshot` are **refused** when the parent sandbox has **any** attached named volume, regardless of kind. This is an interim gate (D-PD-96, TBR-PD-15) — the correct fork-with-volumes and snapshot-with-volumes semantics are pending design and have not been ratified yet. The refusal exists to prevent silent data hazards until that design lands:

- **kind=disk**: two VMs sharing the same ext4 image read-write simultaneously corrupt it; a per-child copy leaks permanently into unreclaimed storage.
- **kind=dir**: two VMs sharing the same host directory over virtiofs get a single mutable view — fork isolation does not exist.

There is no hot-detach command (TBD-SD2-LIVE-3 is not yet built). To run N parallel sandboxes that each need a volume, use independent `nexus3 create` calls instead of forking.

## Hot-attach follow-on <Badge type="danger" text="not built" />

`nexus3 sandbox volume-attach <sandbox-id> <name>:<guest-path>[:<options>]` hot-plugs a named volume into a running sandbox without rebooting it. This requires `virtio-mem` hotplug support in the guest kernel and is gated on TBD-SD2-LIVE-3.

## See also

- [`--mount-named` flag on `nexus3 create`](/cli/sandbox-commands#named-volumes)
- [Resource lifecycle — named volumes](/operations/resource-lifecycle#named-volumes)
- [AI agents — volume-config skill](/ai-agents)
