# Snapshots and fork

## The `Snapshot` artifact

A `Snapshot` is a first-class artifact: it records the full memory and disk state of a sandbox at a point in time and can be restored into one or more new sandboxes.

- Snapshots are **content-addressed** by the artifact store (like images).
- Snapshots are **not portable** across machines or platforms. On macOS, VZ save files are hardware-encrypted and host+account-bound. On Linux, CH snapshot files are specific to the host's CH version.
- A snapshot has a **commit marker**: nexus3 writes the marker only after the full snapshot is on disk, then length-checks before restore. This is necessary because CH has no integrity check of its own — a truncated `memory-ranges` file is silently zero-filled on restore.

### When snapshot is legal

| State | Snapshot legal? |
|-------|----------------|
| `running` | Yes (self-edge, row S1) — sandbox stays `running` |
| `stopped` | Yes (self-edge, row S2) — sandbox stays `stopped` |
| `created` | No |
| `paused` | No — unsafe across platforms |
| `error` | No |

The operation runs under a lease alongside the record; the sandbox never enters a transient state.

## Fork

`nexus3 fork <id> [--count N]` creates N new sandboxes from a snapshot of the given sandbox.

### Semantics

- The **parent sandbox state is unchanged.** Fork is not a transition in the lifecycle table. `TriggerFork` has no table entry; calling `Machine.Next` with it returns `IllegalTransitionError`.
- **Children are ordinary Sandboxes**, created directly in `running` state. Their `Provenance` field records the parent ID and source snapshot ID.
- Children get **identity fixup** on wake: new MAC address, new IP, new hostname, new `machine-id`. The kernel, disk state, and memory image are otherwise identical to the snapshot.

### Cost model (Linux)

| Resource | Per-child cost |
|----------|---------------|
| Host RAM | Full snapshot size — no page sharing between siblings |
| Disk | CoW sparse copy — only written pages consume real space |
| Boot time | Restore from snapshot (fast, sub-second) vs. cold boot |

**There is no page-sharing between sibling VMs in Cloud Hypervisor.** CH copies snapshot memory into each child's private anonymous memory. This is different from Firecracker's design. Plan for one full memory footprint per concurrent child.

### Parent state and fork validity

Fork requires the parent to have a snapshot. The parent sandbox itself can be in any state while children are running — the child relationship is tracked only through `Provenance`, not through the parent's lifecycle state.

## Snapshot integrity

nexus3 owns integrity; CH provides none.

The snapshot protocol:
1. Pause the VM.
2. Issue `TakeSnapshot` to CH (writes `memory-ranges`, `vm-state`, `disk-state` files to a directory).
3. Write a **commit marker** only after all files are flushed.
4. On restore, check the commit marker exists **and** assert the `memory-ranges` file length matches the recorded value.

If the length check fails, the snapshot is corrupt and the restore is refused. Without this check, CH would restore from a truncated file, silently zero-filling the missing RAM and returning success.

## `snapshot rm` and child references

`nexus3 snapshot rm` refuses while any child sandbox is still paging from the snapshot directory. The artifact store tracks reference counts; `rm` fails if any reference is live.

## Restore-in-place (edge 4)

The spec describes a `stopped → running` restore edge (edge 4). **This edge is intentionally deferred**: it would move a stopped sandbox directly to running via snapshot restore. The code omits it pending operator ratification. Do not rely on it.

## macOS (backlogged)

macOS fork holds the same uniform semantics (children are ordinary sandboxes, parent unchanged, identity fixup on wake). The cost characteristics differ:

- No free-page reporting from the guest → provisioned size, not working set, is billed.
- VZ save files are hardware-encrypted and non-portable.

The cost table above reflects Linux/CH. macOS measurements are in spec 12 (not yet ported to this doc site).
