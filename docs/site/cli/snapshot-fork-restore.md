---
title: "Snapshot, Fork and Restore"
description: "Reference for snapshot, fork, and restore commands"
---

# Snapshot, Fork and Restore

> Fork a sandbox or restore a snapshot into a new isolated copy-on-write child. The orchestrator loops to fan out N children.

`fork` and `restore` are the verbs that distinguish nexus3: each call produces one new running child from a copy-on-write parent, which is what makes isolated agents cheap enough to be routine. See [Snapshots and fork](/sandboxes/snapshots-and-fork) for the conceptual model.

## nexus3 snapshot create

Capture a consistent snapshot of a running sandbox. The sandbox keeps running; the snapshot is a point-in-time copy retained on disk.

```
nexus3 snapshot create <sandbox-ref> [<name>]
```

The optional `<name>` argument assigns a human-chosen name to the snapshot. <Badge type="danger" text="not built" /> Named snapshots are a target feature; today every snapshot is addressed by its auto-assigned ULID only.

### Named snapshot addressing <Badge type="danger" text="not built" />

When `<name>` is given, `snapshot rm` and `restore` accept either the name or the ULID. The name is an additional alias — the ULID remains the underlying stable identifier. Name uniqueness is enforced per-project; duplicate names are rejected.

## nexus3 snapshot list

List all retained snapshots.

```
nexus3 snapshot list
```

## nexus3 snapshot rm

Delete a retained snapshot by name or ID.

```
nexus3 snapshot rm <name-or-id>
```

Addressing by name is <Badge type="danger" text="not built" /> — today only the ULID is accepted.

## nexus3 fork

Fork a running sandbox into a new copy-on-write child. The parent keeps running. The child is an independent VM sharing the parent's disk pages until it writes.

```
nexus3 fork <sandbox-ref>
```

## nexus3 restore

Restore a retained snapshot into a new running sandbox. Does not require the original sandbox to still exist.

```
nexus3 restore <name-or-id>
```

Addressing by name is <Badge type="danger" text="not built" /> — today only the ULID is accepted.

## Fork and snapshot refusals on mounted sandboxes <Badge type="danger" text="not built" />

Once `-v`/`--volume` is implemented, `fork` and `snapshot create` will be refused on a sandbox that holds a live mount, with an explicit error. Two operations cannot be made correct under a live mount:

- **Fork** — two VMs would share one host worktree and one `.git/index.lock`.
- **Snapshot** — the mounted tree lives on the host and is not captured; a restore would resume memory state referencing files that changed underneath it.

The N-way parallel flow is unaffected: it uses independent `create` calls (see [Lifecycle commands](/cli/sandbox-commands)), each with its own git worktree, so the shared-tree conflict never arises.
