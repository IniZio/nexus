---
title: "Sandbox model"
description: "Sandbox identity, labels, the envelope, and fork lineage"
---

# Sandbox model

> `Sandbox` is the one durable entity in nexus3 — everything else is a transient instantiation or an artifact of it.

A running VM, a snapshot, an image — none are first-class entities alongside `Sandbox`. When the VM dies, the record survives. When you fork, children are new `Sandbox` records with the same struct and lifecycle.

```sh
# Create a sandbox — mints the Sandbox record and boots the VM
nexus3 create my-app --image nexus3-base --label task-id=42

# Filter by label — AND-semantics across multiple --label flags
nexus3 ps --label task-id=42
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

## The Sandbox struct

| Field | Type | Description |
|-------|------|-------------|
| `ID` | `SandboxID` | Stable `sb-` prefixed identifier. Never reused. |
| `Name` | `string` | Human-readable name within a project. |
| `Project` | `string` | Namespace for grouping sandboxes. |
| `Labels` | `map[string]string` | Arbitrary key=value metadata. CLI: `--label KEY=VALUE`, multiple flags AND-matched on fleet verbs. Nil and empty map are equivalent. |
| `State` | `State` | Cached lifecycle state. The VMM is authoritative; where a live VM disagrees, the VM wins. |
| `Envelope` | `Envelope` | Frozen configuration resolved at creation. Never mutated after the record is written. |
| `InstanceID` | `string` | Identifier of the current running instantiation. Internal; not exposed in external keys. |
| `RemoveOnExit` | `bool` | The `--rm` flag, recorded at creation. Durable. |
| `RemovalMarker` | `bool` | Write-ahead tombstone set before any destructive removal. If the process crashes while this is true, the sandbox is gone. |
| `StopReason` | `StopReason` | Qualifier on the `stopped` state. Meaningful only when `State == stopped`. |
| `Provenance` | `*Provenance` | Fork lineage. Nil for sandboxes created with `create`. Frozen at creation. |
| `SupervisorPID` | `int` | PID of the detached per-sandbox supervisor process. Zero means in-process perimeter. |
| `BaseRef` | `string` | Full 40-hex SHA of the host repo HEAD at creation time. Empty when no source mount is declared. |

### Envelope fields

| Field | Type | Description |
|-------|------|-------------|
| `ImageDigest` | `string` | Content-addressable digest of the rootfs image. |
| `AllowedHosts` | `[]string` | Hostnames the sandbox may reach through the egress perimeter. |
| `SSHPublicKey` | `string` | OpenSSH public key injected into `/root/.ssh/authorized_keys` at boot. Empty = no SSH provisioned. |
| `Mounts` | `[]MountSpec` | Live virtiofs mounts: host path → guest path. Edits inside the sandbox appear on the host immediately. Shadow disks sit in front of write-heavy directories to keep virtiofs metadata cost off hot build paths. |

### StopReason values

| Value | Meaning |
|-------|---------|
| `"clean"` | Stopped by explicit user command. Safe to restart. |
| `"memory_lost"` | Substrate destroyed (host reboot, VMM kill, power loss). In-progress work was lost. |

## Identity

**Content-addressing applies to images and snapshots, not sandboxes.**

Fan-out exists precisely to create N sandboxes from identical inputs. Content-addressing would collapse them into one, so it is explicitly rejected for `Sandbox`. A sandbox's identity is its `sb-` ID.

`Project` identity is content-addressed (hash of normalized project inputs). Handle format: `<project>/<name>` — parsed by `domain.ParseHandle`.

## Labels

Labels are arbitrary key=value pairs on a sandbox. The `--label KEY=VALUE` flag on `nexus3 create` and `nexus3 ps` is the primary way to attach intent to a sandbox and then select it from a fleet.

```sh
# Attach labels at create time
nexus3 create worker --label task-id=lint --label env=ci

# Select by label
nexus3 ps --label task-id=lint
```

## Source model

The primary source-init answer is a **live virtiofs mount**: declare one or more host paths to mount into the sandbox via `--mount <host-path>:<guest-path>`. Edits inside the sandbox appear on the host immediately. This is the target model for agentic workflows where a `git worktree` directory is the sandbox source.

Shadow disks sit in front of write-heavy directories inside the mount (`node_modules`, `.next`, `target`, `dist`) to keep virtiofs metadata cost off the hot build paths.

Fork and snapshot are refused on a live-mounted sandbox (see [Snapshots and fork](snapshots-and-fork.md#live-mounted-sandbox--fork-refused)). N-way parallelism uses independent `create` calls, each with its own worktree.

## Fork children are ordinary Sandboxes

A sandbox produced by `fork` is an **ordinary `Sandbox`** — same struct, same lifecycle, same states. Its fork origin is recorded in the `Provenance` field, not by a separate type or state. See [Snapshots and fork](snapshots-and-fork.md).

## Host vs. client

The **host** is the machine running Cloud Hypervisor. The **client** is any machine that issues nexus3 commands — usually the same machine, but the seam is kept clean for remote use. The host owns the VMM process; the client speaks to the core library, which speaks to the driver, which drives the VMM.

## What is NOT an entity

- **VM** — the running instantiation is an internal field (`InstanceID`), not an entity.
- **Workspace** — the term is retired. nexus3 has no `Workspace` type; a source tree mounted into a sandbox is a configuration detail on `Envelope`, not an entity.
- **Project** (as an entity) — there is no `Project` record. Project is a string namespace on `Sandbox`.
