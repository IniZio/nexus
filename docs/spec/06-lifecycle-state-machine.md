# 06 — Lifecycle State Machine

*Purpose: the five lifecycle states, the twelve edges, leases for in-flight operations, and substrate-first crash recovery.*

## Five states

**`created` · `running` · `paused` · `stopped` · `error`** — and nothing else (ticket 19 ruling 16). Old nexus's twelve are cut to five:

- `removed` — **gone**: removal deletes the record (ruling 10, below). No tombstone.
- `restored` — **gone**: provenance, not condition (ticket 30). Provenance is a field (ruling 14).
- `cloning`, `forking`, `snapshotting`, `stopping`, `starting` — **gone**: the transient operations are **leases**, not states (ticket 39's surviving clause (a): conditions are durable, in-flight is held alongside as a lease). Note `forking` existed in old nexus's *code* but never its spec; it is now deliberately in neither.

## In-flight operations are leases, not states

A `L` = a lease held for the duration of an operation. It is **not durable and not a state**; the sandbox reads as its **pre-operation condition** throughout (Incus's shape). The durable record holds: identity, the frozen envelope (resolved policy artifact, image digest, agent attachment), the five-value state, the internal `instance_id`, `--rm` intent + the write-ahead removal marker, and a **stop-reason qualifier** (six durable fields total; ruling 12).

## The transition table (five states, twelve ruled edges + one derived)

The table below has thirteen rows: **twelve user-ruled edges plus edge 4** (restore-in-place), which is marked *DERIVED, not ruled* — never put to the user (ticket 19). "Twelve edges" refers to the ruled set. One further completion is flagged inline: **edge 10 covers a `running` sandbox whose VM died, not only a `paused` one.** Ticket 19's ruling 9 ruled the paused variant and its transition table omitted the running row, but ruling 9's own principle (a reboot / VMM kill / power loss destroys memory; ruling 6 makes the substrate authoritative; route-to-`error` was explicitly declined) determines the running case identically. It is a faithful table-completion, not a new decision, and is flagged on the map for human ratification alongside edge 4.

| # | From | To | Trigger | Initiator | Notes |
|---|------|-----|---------|-----------|-------|
| 1 | ∅ | `created` | `nexus3 create` | user | record + frozen envelope + seeded rootfs; no VM yet |
| 2 | `created` | `running` | `nexus3 start` / `run` | user | cold boot. `L`: boot. On failure → stays `created`, command non-zero |
| 3 | `stopped` | `running` | `nexus3 start` | user | cold boot. `L`: boot. On failure → stays `stopped` |
| 4 | `stopped` | `running` | `nexus3 restore <snapshot>` in place | user | **DERIVED, not user-ruled.** `L`: restore. Snapshot must validate first (doc 05) |
| 5 | ∅ | `running` | `nexus3 fork --count n` | user | creates *n* new Sandboxes already `running`; parent unaffected |
| 6 | `running` | `paused` | `nexus3 pause` | user | a resting place, not an internal step (ruling 8) |
| 7 | `paused` | `running` | `nexus3 resume` | user | |
| 8 | `running` | `stopped` | `nexus3 stop` | user | `stop_reason = clean` |
| 9 | `paused` | `stopped` | `nexus3 stop` | user | `stop_reason = clean` |
| 10 | `paused` \| `running` | `stopped` | reconcile finds no substrate — memory destroyed (host reboot / VMM kill / power loss) | reconcile | `stop_reason = memory_lost`. **Not** an automatic transition: nothing decided to stop it (ruling 9, extended to the `running` variant — see the intro note above and the recovery bullet below) |
| 11 | `running` | ∅ | `--rm` primary-command exit, **or** `nexus3 rm` | supervisor / user | unconditional on exit status; record deleted; write-ahead marker |
| 12 | any | ∅ | `nexus3 rm` | user | including from `error` — this is how a terminal record is cleared |
| 13 | `created`\|`stopped` | `error` | write-ahead marker present for a crashed **removal** or **save**, **AND substrate observation finds nothing** | reconcile | the only two `error` producers. Terminal but not a tombstone: edge 12 clears it |

**Self-edges held under a lease** (operations that most look like states and are not): **snapshot** (`running`→`running`, `stopped`→`stopped`), **fork** (parent unchanged; edge 5 is the child).

## `--rm` semantics

- `--rm` is **one machine with one extra edge** (edge 11), not a second shape (ruling 1).
- Removal is **unconditional on exit status** — exactly `docker run --rm`: a non-zero primary command is still removed (ruling 2). Consequence carried, not solved here: a failed `--rm` run destroys the guest FS with no recovery path (getting work out is doc 09's `nexus3 cp`).
- The **per-sandbox supervisor owns the removal edge** (ruling 3) — the no-central-daemon architecture permits long-lived per-sandbox supervisors and forbids a central reaper.
- **Removal deletes the record** (ruling 10): no `removed` state, no tombstone; `nexus3 ls` simply stops listing it; `--json` can never report `removed`.

## `error` — exactly two producers, and no `running`→`error` edge

`error` means exactly one thing: **a write-ahead marker says a destructive operation crashed part-way, and nexus3 will not guess** (ruling 13). Two producers, no others:

- **removal crashed mid-way** (rulings 5, 11);
- **save/snapshot crashed mid-way** — detected by the marker plus the explicit validation step (doc 05).

- A **failed `start`/boot is explicitly NOT `error`** (ruling 13): the command returns non-zero, the lease releases, the durable condition stays `stopped`/`created`. Absorbing failed boots into `error` was declined — it would give `stopped` a second meaning.
- **There is no `running`→`error` edge.** Edge 13 is **gated on an empty substrate**: the marker is consulted only *after* substrate observation, never instead of it. A marker over a **live VM** is an orphan — the sandbox is `running` and stays `running`, and the suspect thing is the **`Snapshot` artifact**, not the sandbox. An earlier draft allowed `running`→`error` on a marker alone; that is old nexus's discarded-healthy-VM bug verbatim (a parent in `StateSnapshotting` with a healthy VM, relabelled `error`).

## Stop-reason qualifier

A narrow **stop-reason qualifier on `stopped` only** (ruling 12), clawk's shape, at minimum:

- `clean` — the user ran `stop`;
- `memory_lost` — a **paused or running** VM's RAM was destroyed by reboot / VMM kill / power loss (edge 10, ruling 9 extended to the running variant).

`nexus3 ls` and `--json` flag `memory_lost` (a user-visible data loss that must not be silent). `error` needs **no** qualifier — the write-ahead marker is itself durable and already names which operation crashed.

## Crash recovery — substrate first, record is a cache

Recovery (`internal/core/recovery`) **observes the substrate FIRST; the durable record is a cache, never authoritative** (ruling 6, clawk's rule). Order: ask the VMM what is alive → consult the durable record as a cache → consult the marker last. Reconciliation is **not an edge** in the table; it runs before any state is read, and edges 10 and 13 are its only two possible writes. Everything else it finds, it **adopts in place** — the acceptance test: a healthy live VM behind a stale record is **never discarded**.

- Crash mid-removal → `error` (edge 13), no automatic retry; explicit `nexus3 rm` clears it.
- Crash mid-`--rm` with **marker absent + dead VM + `--rm` set** → **remove** (the common case; ruling 5 intact).
- **Durable (non-`--rm`) `running` sandbox, VM gone, no marker** (host reboot / VMM kill / OOM) → **`stopped`, `stop_reason = memory_lost`** (edge 10). This is the running-variant completion of ruling 9: the sandbox cannot stay `running` (that is the stale-record incoherence ruling 6 forbids), cannot go to `error` (no marker, and route-to-`error` was declined), and its memory is gone — so it resolves to `stopped` with the loss surfaced, exactly as the paused case. Determined by ticket 19's rulings, flagged on the map for ratification.
- **A failed boot may leave a live VMM behind, and the lease holder must kill it.** Since the substrate is authoritative, a half-booted VMM that outlives a failed command would be adopted as `running` on the next reconcile — silently contradicting the state the command left. The per-sandbox supervisor (which owns the removal edge) must tear down a half-booted VMM before releasing the boot lease.

## Fan-out children and platform asymmetry

- A **fan-out child is an ordinary Sandbox** on the identical machine, provenance a field (ruling 14). `fork --count 3` produces three Sandboxes already `running`, each indistinguishable from a cold-booted one except by provenance.
- **Post-snapshot state is uniform `running` on both platforms** (ruling 15); the Linux/macOS asymmetry is cost, not state, and stays inside the driver. Original questions 1 and 3 answer *stays inside the driver* — not contingent on ticket 33, which changes cost not state.

---

*Sources: ticket 19 (all fifteen rulings + the transition table), 39 clause (a) (leases), 30 (`clone` is provenance), 13/40 (snapshot validation, doc 05), 20 §9 (no automatic transitions). Obligations carried: ticket 18/21 (run-state interrogation), 11 (`pause`/`resume`, stop-reason, no `removed` in `--json`).*
