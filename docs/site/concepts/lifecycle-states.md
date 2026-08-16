# Lifecycle states

A sandbox occupies exactly one of five states at all times. There are no transient states: an operation in flight holds a lease alongside the durable record; the record never enters an intermediate `snapshotting` or `restoring` value.

This is a deliberate fix to a predecessor bug: nexus2 had twelve states including transient operation states, which left working VMs stranded in `snapshotting` when a process crashed — matching neither the adoption gate (keyed on `running`) nor the reaper gate (keyed on `pid == 0`).

## States

| State | Meaning |
|-------|---------|
| `created` | Record minted; no VM running. |
| `running` | VM live and accepting commands. |
| `paused` | VM execution suspended; memory intact in host RAM. A stable, deliberately-held state — not an internal step in snapshot or checkpoint. |
| `stopped` | VM absent; record survives. `StopReason` qualifier is set. |
| `error` | Write-ahead marker present for a crashed destructive operation, or `TriggerFail` received. |

`error` is a recoverable state. The user acknowledges it with `TriggerReset`, which returns the sandbox to `stopped` for restart or deletion.

## Transition table

Verified against `internal/core/lifecycle/transitions.go`.

| # | From | To | Trigger | Initiator |
|---|------|----|---------|-----------|
| 1 | `created` | `running` | `start` | user |
| 2 | `running` | `paused` | `pause` | user |
| 3 | `paused` | `running` | `resume` | user |
| 4 | `running` | `stopped` | `stop` | user |
| 5 | `stopped` | `running` | `start` | user |
| S1 | `running` | `running` | `snapshot` | user (self-edge) |
| S2 | `stopped` | `stopped` | `snapshot` | user (self-edge) |
| 6 | `paused` | `stopped` | `substrate_lost` | system |
| 6b | `running` | `stopped` | `substrate_lost` | system |
| 7 | `created` | `error` | `fail` | system |
| 8 | `running` | `error` | `fail` | system |
| 9 | `paused` | `error` | `fail` | system |
| 10 | `stopped` | `error` | `fail` | system |
| 11 | `error` | `error` | `fail` | system (idempotent) |
| 12 | `error` | `stopped` | `reset` | user |
| 13 | `running` | *(remove)* | `primary_command_exit` | system (`--rm` only) |

## What is explicitly illegal

### `paused → stopped` via `stop`

**Calling `nexus3 stop` on a paused sandbox returns an `IllegalTransitionError`.** Row 4 in the table has `running → stopped`; there is no `paused → stopped` row for the `stop` trigger.

This is the correct behavior: a paused sandbox holds its full memory state. Stopping it cleanly requires a resume first: `nexus3 resume <id> && nexus3 stop <id>`.

The `paused → stopped` edge exists, but only for substrate loss (trigger `substrate_lost`, row 6) — when the host reboots or the VMM is killed and the memory state is already gone.

> An older doc (spec 06, edge 9) described `paused → stopped` via `stop` as valid. The code disagrees. The code is right.

### Snapshot from `paused`

Snapshot is a self-edge valid only from `running` (row S1) and `stopped` (row S2). It is not legal from `created`, `paused`, or `error`. A paused VM's memory state cannot be safely snapshotted across all platforms.

### Fork bypasses the table

`TriggerFork` has no entry in the transition table. `Machine.Next` returns `IllegalTransitionError` for any `(parentState, TriggerFork)` pair. Fork children are created directly in `running` state by the service layer; the parent sandbox state is unchanged.

## State diagram

```
                  ┌─────────────┐
            ╔═════▶   created   ║──── TriggerFail ────────┐
            ║     └──────┬──────┘                         │
       (create)          │ start                           │
            ║            ▼                                 ▼
            ║     ┌─────────────┐    stop         ┌──────────────┐
            ║  ╔══▶   running   ╠─────────────────▶   stopped    │
            ║  ║  └──────┬──────┘                 └──────┬───────┘
            ║  ║         │ pause   substrate_lost         │
            ║  ║         ▼         (row 6b)               │ start
            ║  ║  ┌─────────────┐                         │
            ║  ║  │   paused    │─── substrate_lost ───▶  │
            ║  ║  └──────┬──────┘   (row 6)        (stopped)
            ║  ║         │ resume                         │
            ║  ╚══════════╝                               │
            ║                                             │
            ║         TriggerFail (any state) ──▶ ┌──────────────┐
            ║                                      │    error     │
            ║         TriggerReset  ◀──────────────└──────────────┘
            ║         (error→stopped, row 12)
            ║
            ║  --rm: primary command exit ──▶ (remove, not a state)
            ║
      (nexus3 rm from any state ──▶ remove)
```

## The `stop_reason` qualifier

When a sandbox enters `stopped`, a `StopReason` qualifier is written:

- **`clean`** — user-requested stop. The sandbox is safe to restart with `nexus3 start`.
- **`memory_lost`** — substrate loss (host reboot, VMM kill). In-progress work at the time of the event was lost. The operator should be informed.

`StopReason` is cleared when the sandbox transitions back to `running`.

## Resource governor

A per-sandbox governor runs three control loops while the VM is running:

| Axis | Mechanism | Guard |
|------|-----------|-------|
| Memory | virtio balloon inflate/deflate | Host `MemAvailable` floor |
| vCPU | hot-plug / hot-unplug | None — vCPU has no guard |
| Disk | virtio-blk online resize (no guest reboot) | Host free-space guard |

Telemetry (pressure stall, disk usage, memory stats) arrives from the guest over a dedicated vsock port. The governor polls it and issues resize calls to the driver when the control law fires.

Configuration (`vmcfg.Resolve`) is the single source of truth for all three axes at any moment. The governor does not hold a separate configuration copy.

## Recovery

When a nexus3 process starts, it reconciles the durable records against the live VMM state. Sandboxes whose VMM process is absent but whose record shows `running` or `paused` receive `TriggerSubstrateLost`, resolving to `stopped` with `StopReason = memory_lost`. The write-ahead `RemovalMarker` field handles crashed destructive operations — a record with `RemovalMarker = true` is treated as already-removed.
