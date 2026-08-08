# 02 — Core Module Boundary

*Purpose: the `internal/core/` module inventory, the seams between them, the single-module repo shape, and the binaries. Binds to Go package/type identifiers, not line numbers (ticket 21).*

## Repo shape

nexus3 is a **single-module repo** (ticket 28, ratified live): one `go.mod` (or a `go.work` covering the tree), so the entire self-hosting loop is one `go build ./...` / `go test ./...`. Old nexus's multi-module shape made the in-workspace loop per-module and awkward; nexus3 does not inherit it. This fixes module *hosting* only — the package boundaries below are ticket 21's and are unchanged.

## The homegrown core: `internal/core/`

Every substrate and surface concern sits behind a seam. The core modules:

| Package | Owns |
|---|---|
| `internal/core/domain` | The ubiquitous-language types (doc 01): `Sandbox`, `Snapshot`, `Image`, `Base`, identity, the five lifecycle states. Pure data, no I/O. |
| `internal/core/store` | Durable persistence of the Sandbox record and artifact records. The record is a **cache** of substrate reality (ticket 19 ruling 6), never authoritative over a live VM. |
| `internal/core/lifecycle` | The five-state machine and its twelve edges (doc 06); leases; the write-ahead removal/save marker. |
| `internal/core/driver` | The substrate seam (doc 03). Owns the vsock transport primitive `DialGuest` (host-dials-guest) and the **network-hook primitive** (tap-fd on Linux); owns **no protocol**. Must **not** import `agent` or `perimeter`. Exposes run-state interrogation (not merely liveness), per ticket 19's obligation. |
| `internal/core/agent` | The `nexus3.agent.v1` client (doc 04). Takes a handed `net.Conn` from `driver` and speaks the protocol, knowing no substrate. Owns reattach-across-snapshot (re-dial + resume). |
| `internal/core/builder` | The image pipeline (doc 07). A proper core module that **consumes `driver`** to run the builder VM (ticket 21 user ruling). |
| `internal/core/perimeter` | The egress/credential seam (doc 08). A **peer of `driver`**, consuming the `driver` network-hook primitive; `driver` must not import it; `service` consumes it. |
| `internal/core/recovery` | Crash/orphan reconciliation (doc 06): observe substrate first, treat the record as a cache, consult the marker last. |
| `internal/core/service` | The single façade the surfaces call. One module, with a recorded split-trigger to carve a `session` module when the reattach work (tickets 36/37) lands. |

### `agent` is a PEER of `driver` (the load-bearing layering rule)

`driver` owns the **transport** (the raw byte stream to the guest); `agent` owns the **protocol**. They are siblings under the core, not nested (ticket 21, Option A). Concretely:

- `driver.DialGuest` returns a generic `net.Conn`; `agent` speaks `nexus3.agent.v1` over it and never learns whether it is Cloud Hypervisor's `AF_VSOCK`-over-UDS multiplexer or (later) a VZ connection.
- **`driver` must not import `agent`.** Reattach-across-snapshot (re-dial the fixed guest port, resume from offset) lives in `agent`.
- This is backed by primary-source convergence across clawk / kata / firecracker-containerd / apple-containerization: all keep the agent client separate and let the substrate supply the dial as a generic byte stream. apple-containerization is the cautionary case — it has nexus3's exact VZ+gRPC-over-vsock topology and *nested* the dial, which left reattach a permanent TODO.

The same shape governs `perimeter`: `driver` hands the raw substrate network hook (analogous to `DialGuest`); `perimeter` consumes it and `driver` stays ignorant of policy.

A **provisional reverse-dial `ListenGuest`** (host-listens / guest-dials) is reserved on `driver`. Ticket 23 released the credential claim on it (the MITM credential path needs no guest→host channel); **ticket 15 claims it** for the forwarded ssh-agent socket. The reservation stands with one named consumer.

The crabbox 21-value `Feature` enum is **not adopted**; capability discovery is type-assertion on optional interfaces plus a derived `Capabilities()`, the deeper shape (ticket 21).

## Surfaces — flat, thin, no surface-to-core RPC

Surfaces are flat thin adapters over `service` with **no surface-to-core RPC** (ticket 11) — which is what makes no-central-daemon true by construction (doc 09):

- `internal/cli` — the CLI, and the machine contract (versioned `--json` control plane + raw-stdio attach).
- `internal/mcp` — MCP over stdio only, no network listener.
- out-of-tree `plugins/herdr/` — the herdr plugin, reaching the core only through a hidden `nexus3 __herdr-plugin` shim (i.e. through the CLI), never a library or socket into the core.

## Binaries

- `cmd/nexus3` — the CLI / core (core-as-library linked in).
- `cmd/nexus3-agent` — the guest PID-1 agent; **shares the `nexus3.agent.v1` proto** with `internal/core/agent`.
- `nexus3-vzd` — Swift, macOS-only, owned by ticket 18. **Backlogged** (doc 12); not built on the Linux path.

## Naming discipline

The spec binds to **Go package / interface / type identifiers, not line numbers** (ticket 21). This closes the old-nexus `internal/domain`-vs-`internal/core` divergence class, where specs cited `internal/domain/` while all code lived under `internal/core/`. Old-nexus code paths may be cited as provenance, but nexus3's own structure is always named by package/type.

---

*Sources: tickets 21 (module inventory, agent-peer-of-driver, naming discipline, Feature-enum rejection), 28 (single-module repo), 11 (surfaces, no surface-to-core RPC), 15 (perimeter naming + ListenGuest consumer), 23 (ListenGuest credential claim released), 19 (run-state interrogation obligation), 18 (binaries).*
