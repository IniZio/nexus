# 05 — Fork, Restore, Snapshot

*Purpose: the `Snapshot` artifact, the `fork`/`restore` verbs, uniform cross-platform semantics with asymmetric cost, and snapshot integrity.*

## `Snapshot` as a first-class artifact

A **`Snapshot`** is a first-class artifact (ticket 13) with its own durable record and retention rules — the saved memory + device state of a VM. `fork` and `restore` are **distinct verbs**, both copied from crabbox's command surface, both taking `--count <n>`.

The key structural move: **the capability rides on the artifact's `kind`, not on the method** (ticket 13). There is exactly **one `Fork` method** in the core and **nothing branches on platform**. What differs between a transient fork and a retained snapshot is the artifact kind, not a different code path.

## Uniform semantics, asymmetric cost

Fork/snapshot/restore/fan-out **work on both platforms with identical semantics** (ticket 13; ticket 19 ruling 15). No user-visible capability is Linux-only. The spec declares a **cost table, not a feature matrix.**

- **Snapshot is state-preserving and uniform** (ticket 19 ruling 15). A running sandbox is `running` again when the snapshot completes; a stopped one stays `stopped`. macOS pays a restore-from-save to get back where Linux resumes from pause — but the *state* is uniform. A platform-conditional post-snapshot state (`running` on Linux, `stopped` on macOS) was selected and then reversed by the user: it is script-observable and contradicts "nothing branches on platform." Do not record it.
- On wake, a child is a **true clone with identity fixup only** (MAC/IP, hostname, `machine-id`, reported sandbox id). On macOS this in-guest re-identity is *mandatory* because VZ rejects pre-restore MAC mutation (doc 12).

### The cost table

| Platform | A branch costs | Mechanism |
|---|---|---|
| **Linux (CH)** | its **working set** | CH `free_page_reporting` punches holes in the memfd; the snapshot writer skips them, so reclaim shrinks the save file **and** every child. Memory is **copied** per-VM — there is **no CoW page-sharing between siblings** (that is Firecracker, not CH). Host-RAM-bounded. |
| **macOS (VZ)** | its **full provisioned size** | VZ has no free-page reporting and no memory hot-plug; the balloon is a weak pressure signal, not a reclaim lever (~6% of the ask returned, decaying). Recorded in doc 12. |

The bar is **seconds, 2–3 branches** (not milliseconds, not 10+). This fits CH's copy-per-VM model and keeps ticket 07 closed; a sub-second bar would have reopened it for Firecracker. **No warm pool; `create` cold-boots.**

## Restore path coupled to retention

- A **transient `fork`** restores **eagerly**.
- A **retained snapshot** may use **UFFD** (Linux-only lazy paging).
- `/tmp` is **disk-backed, not tmpfs** — a percentage-sized tmpfs is frozen at 50% of *boot* RAM forever (verified in `mm/shmem.c`), which was exactly the builder-exhaustion old nexus papered over with a userspace resizer. nexus3 does not inherit that resizer.

## Snapshot integrity

Integrity is a **substrate-agnostic commit marker + a Linux-only length assertion** (ticket 40):

- **Commit marker** (primary guarantee): fsync'd after `vm.snapshot` returns. Substrate-agnostic.
- **Length assertion** (defense-in-depth, Linux/CH only): `Σ length == st_size`, eager and O(1), comparing *apparent* size because CH writes sparse. Implementable exactly from CH's `MemoryRange{gpa,length}` table.
- **Content-digest is rejected.** No microVM VMM checksums its memory file (Firecracker/QEMU/CRIU rely on magic+version+length); a multi-GB hot-path read is a cost no competitor pays. No opt-in deep-verify in v1.

**Validity is a DERIVED property** (marker-present AND length-matches), checked **at-use** on every restore/fork — **not** a stored mutable `invalid` state. A restore-time-bad artifact is a **clean op failure with sandbox state unchanged** — it produces **no third `error` producer** (doc 06's two stand).

## `snapshot rm` refuses while children page

`snapshot rm` **refuses while children page from it** (tickets 13, 40). There is **no force-remove in v1** (accepted risk, doc 11). The eager O(1) length check catches a truncated parent *before* any UFFD child can be created, so a child paging from a truncated parent can never exist; the refusal protects only healthy children. This rule stands unamended.

## macOS (backlogged)

The backlogged macOS path holds the same fork capability with the same uniform semantics; its cost characteristics (provisioned-size, no free-page reporting) and validated measurements are in **doc 12**. Snapshots are non-portable on every platform (a portable memory-state kind cannot exist because VZ save files are hardware-encrypted and host+account-bound).

---

*Sources: tickets 13 (Snapshot artifact, fork/restore verbs, kind-not-method, cost table, no warm pool, /tmp disk-backed, non-portable), 19 ruling 15 (uniform state-preserving snapshot), 40 (integrity: marker + length, derived validity, no third error producer, rm-refuses), 07 (bar keeps substrate closed). Map Correction 127: CH copies, no CoW page-sharing between siblings.*
