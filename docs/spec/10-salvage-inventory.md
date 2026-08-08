# 10 — Salvage Inventory

*Purpose: what nexus3 re-derives vs ports vs deletes from old nexus. A clean break with named exceptions.*

## Ground rules

Old nexus is **reference material only** — a clean break (map). The clone lives at `$HOME/magic/nexus/nexus-clone-repro` (~130k LOC Go microVM sandbox runtime). Two facts constrain how it can be salvaged:

- **It has NO git history** — `git rev-list --count HEAD` = 1, a single squashed commit. `git log -S` is useless; nothing can be dated. Every rationale must come from code and comments, not history.
- Its **specs have drifted from its code** — the `internal/domain`-vs-`internal/core` divergence and the `forking` state that exists in code but never in spec. A verbatim port would silently inherit both. nexus3 binds to Go identifiers (doc 02) and re-derived its state machine (doc 06) precisely to avoid carrying these.

Raw inventory lives in three explorer reports: `research/old-nexus-domain-vocabulary.md`, `research/old-nexus-topology-supervision.md`, `research/old-nexus-policy-model.md`.

## Re-derived (not ported)

- **The lifecycle state machine.** Re-derived from scratch (doc 06): twelve states → five, and the spec/code `forking` divergence deliberately landed in **neither**. Not a port.
- **The domain model / ubiquitous language** (doc 01). `Workspace`→`Sandbox`, one entity; identity scheme new.
- **The credential model** (doc 08). Old nexus shipped host-side refresh termination (`oauthbroker`+`credproxy`+`oauth_shim`, leaving short-lived `material` in the guest); nexus3 re-derived MITM placeholder substitution instead. The TLS-MITM that some stale notes credited to old nexus was its planned-never-built Phase 2.

## Strongest single salvage candidate: the supervision model

The **supervision model** is the strongest salvage candidate (map): long-lived per-sandbox supervisors, no central daemon, `flock`-based liveness with two-tier detection (flock + identity). nexus3's no-central-daemon capability (doc 00) and its per-sandbox-supervisor ownership of the removal edge (doc 06) are this model, re-expressed. The learning to honour: old nexus's orphan recovery had a too-coarse "VM alive vs error" boundary causing unnecessary resets; the fix was two-tier detection. nexus3 goes further — substrate-first recovery (doc 06 ruling 6) makes the discarded-healthy-VM bug structurally impossible.

## Deleted outright

| Deleted | Why | Size / note |
|---|---|---|
| `ocitools` + `internal/vm/rootfs` + `cmd/nexus-guest-agent/buildkit_task.go` image-baking | One builder-VM path over stock BuildKit; nexus3 owns zero OCI code (doc 07) | **~11.8k LOC** (map Correction — the earlier "~8.8k" figure was an undercount) |
| The **crane** dependency | Same — no hand-rolled OCI | |
| The **SSH gateway** (bubbletea/Bubbles/x-vt/Wish surface layer) | TUI offloaded to herdr; a Sandbox is an SSH *endpoint* not a gateway (doc 09) | old homegrown surface ~4.1k LOC |
| The **userspace `/tmp` resizer** | `/tmp` is disk-backed, not a percentage-sized tmpfs frozen at 50% of boot RAM (doc 05) | `mount.go`; note the resizer's ceiling-reading path had no production caller — a fossil |
| The **`--tool` overlays** | Exactly two images, no curated toolchain set (doc 07) | |

## Divergences to fix, not carry

- **`forking` state** exists in code (`internal/core/workspace/state.go:9`) but is absent from the spec. nexus3 has it in neither — transient operations are leases (doc 06).
- **`internal/domain` vs `internal/core`** — every old spec cites `internal/domain/` while all code lives in `internal/core/`. nexus3 binds to package/type identifiers so the divergence class cannot recur (doc 02).
- **False couplings not to inherit:** old nexus's `2 GiB` fork-child memory floor is **not** about tmpfs, and three numerically-equal constants (`types.go:15-23`, `tmpfsAbsoluteCapBytes`, `DefaultMemoryMinBytes`) are **causally unrelated** (map). Do not port a coupling that was never real.
- **Filesystem-view stale spec:** old nexus's spec claimed a live read-only virtiofs `/workspace` share; **no such runtime share exists** — project content enters by **one-time copy through a transient share** (`copyLocalSeed`). nexus3 keeps **seed-by-copy** as the capability (ticket 20 §3; doc 08), not the phantom live share.

## Open (noted, not a live ticket)

Whether the guest should carry sshd at all (for editors/sshfs/git) versus clawk's zero-sshd guest is **not settled** — it would reopen ticket 11's SSH-endpoint amendment and cost VS Code Remote-SSH, so it is deliberately not a live ticket. Revisit only if the sshd surface proves to cost more than the tooling buys.

---

*Sources: map "Not yet specified" (salvage), Corrections (no git history, ~11.8k LOC, false 2GiB coupling, stale filesystem-view spec), tickets 14 (ocitools/crane/`--tool` deletion), 11/32 (SSH gateway drop, endpoint retention), 13 (/tmp disk-backed), 19 (state machine re-derived), 21 (identifier binding), 23 (credential model re-derived).*
