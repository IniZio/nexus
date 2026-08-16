# Sandbox model

`Sandbox` is the **one durable entity** in nexus3. Everything else — a running VM, a snapshot, an image — is either a transient instantiation or an artifact of the sandbox, not a first-class entity alongside it.

## The Sandbox struct

Verified against `internal/core/domain/sandbox.go`.

| Field | Type | Description |
|-------|------|-------------|
| `ID` | `SandboxID` | Stable `sb-` prefixed identifier. Never reused. Content-addressing is explicitly rejected for sandboxes (see [Identity](#identity)). |
| `Name` | `string` | Human-readable name within a project. |
| `Project` | `string` | Namespace for grouping sandboxes. |
| `Labels` | `map[string]string` | Arbitrary key=value metadata (D-PD-21). CLI: `--label KEY=VALUE`, multiple flags AND-matched on fleet verbs. Nil and empty map are equivalent. |
| `State` | `State` | Cached lifecycle state. The VMM is authoritative; where a live VM disagrees, the VM wins. |
| `Envelope` | `Envelope` | Frozen configuration resolved at creation. Never mutated after the record is written. |
| `InstanceID` | `string` | Identifier of the current running instantiation. Must not be used to key runtime-scoped resources; kept internal to preserve the option to split `Sandbox` and `VM` later. |
| `RemoveOnExit` | `bool` | The `--rm` flag, recorded at creation. Durable. |
| `RemovalMarker` | `bool` | Write-ahead tombstone. Set before any destructive removal work begins; cleared only on success. If the process crashes while this is true, the sandbox is gone and must not be retried. |
| `StopReason` | `StopReason` | Qualifier on the `stopped` state. Meaningful only when `State == stopped`. |
| `Provenance` | `*Provenance` | Fork lineage. Nil for sandboxes created with `create`. Frozen at creation. |
| `SupervisorPID` | `int` | PID of the detached per-sandbox supervisor process. Zero means in-process perimeter. |
| `CreatorPID` | `int` | OS PID of the `__builder` record's creator. Used for orphan reaping only. |
| `BaseRef` | `string` | Full 40-hex SHA of the host repo's HEAD at creation time. Git bundle anchor; empty when no workspace was mounted. |

### Envelope fields

| Field | Type | Description |
|-------|------|-------------|
| `ImageDigest` | `string` | Content-addressable digest of the rootfs image. |
| `AllowedHosts` | `[]string` | Hostnames the sandbox may reach through the egress perimeter. |
| `SSHPublicKey` | `string` | OpenSSH public key injected into `/root/.ssh/authorized_keys` at boot. Empty = no SSH provisioned. |

### StopReason values

| Value | Meaning |
|-------|---------|
| `"clean"` | Stopped by explicit user command. Safe to restart. |
| `"memory_lost"` | Substrate destroyed (host reboot, VMM kill, power loss). In-progress work was lost. |

## Identity

**Content-addressing applies to images and snapshots, not sandboxes.**

Fan-out exists precisely to create N sandboxes from identical inputs. Content-addressing would collapse them into one, so it is explicitly rejected for `Sandbox` (design ticket 10). A sandbox's identity is its `sb-` ID.

`Project` identity is content-addressed (hash of normalized project inputs). This is the surviving half of a retired "deterministic addressing" capability.

Handle format: `<project>/<name>` — parsed by `domain.ParseHandle`.

## Labels, not workflow nouns

A previous design had a `MotiveID` field on `Sandbox`. It was replaced by `Labels map[string]string` (decision D-PD-21) so the core ships primitives. Legacy records that carried a `motive_id` field are migrated to `Labels["motive"]` on load.

To filter by motive: `nexus3 list --label motive=<slug>`.

## Fork children are ordinary Sandboxes

A sandbox produced by `fork` is an **ordinary `Sandbox`** — same struct, same lifecycle, same states. Its fork origin is recorded in the `Provenance` field, not by a separate type or state. See [Snapshots and fork](snapshots-and-fork.md).

## Host vs. client

The **host** is the machine running Cloud Hypervisor. The **client** is any machine that issues nexus3 commands (usually the same machine, but the seam is kept clean for remote use). The host owns the VMM process; the client speaks to the core library, which speaks to the driver, which drives the VMM.

## What is NOT an entity

- **VM** — the running instantiation is an internal field (`InstanceID`), not an entity. There is a recorded design trigger to split `Sandbox` into `Sandbox + VM` if one sandbox ever needs two concurrent VMs; that split is kept cheap precisely because `InstanceID` is not exposed in external keys.
- **Workspace** — the term is retired. nexus3 has no `Workspace` type; a source tree mounted into a sandbox is a configuration detail on `Envelope`, not an entity.
- **Project** (as an entity) — there is no `Project` record. Project is a string namespace on `Sandbox`.
