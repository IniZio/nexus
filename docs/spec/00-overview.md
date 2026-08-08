# 00 — Overview

*Purpose: what nexus3 is, its scope and the Linux-first boundary, the acceptance test, the homegrown-vs-offloaded thesis with the library-offload inventory, and the capability-to-mechanism mapping. Reading guide to docs 01–12.*

## What nexus3 is

nexus3 is a **microVM-grade sandbox runtime for coding agents**. It boots each sandbox as a real hardware-isolated virtual machine, runs a coding agent (or an interactive shell) inside it under a policy the operator controls, and lets that VM be snapshotted, forked into several independently-runnable branches, and reattached to across snapshot/restore without losing a live session.

The design commitment that shapes every other decision: **homegrown code is confined to a sandbox policy core; every substrate concern is offloaded to a vetted third-party library.** nexus3 owns the *policy* — what a sandbox is, what it may reach, how it forks, how its lifecycle recovers from a crash — and owns as little *mechanism* as it can. It writes no VMM, no OCI image builder, no terminal multiplexer, no userspace network stack.

Go module: `github.com/IniZio/nexus3`. Single-module repo (one `go.mod`, or a `go.work` covering the tree) so the whole self-hosting loop is one `go build ./...` / `go test ./...` (ticket 28).

## Scope: Linux-first, macOS backlogged

The near-term critical path is **Linux only**. The Cloud Hypervisor execution tier, the Go core, the Go guest agent, the egress perimeter and self-hosting all target Linux first (map ruling 2026-08-06).

macOS / Apple `Virtualization.framework` support is **backlogged — deferred, not abandoned.** All macOS design (`nexus3-vzd`, the VZ fork tier, the Swift ecosystem choice) is preserved and has been empirically validated on the `newman@minion` test host (ticket 33), and re-enters as a milestone once the Linux path self-hosts. It lives entirely in **doc 12 (macOS addendum)** and is never a blocker on the Linux path. The `driver` seam (doc 02, doc 03) exists precisely to keep macOS re-addable without touching the core: a second `driver` implementation plus `nexus3-vzd` is the whole macOS delta.

Everywhere below, normative statements describe the Linux system unless a clause is explicitly marked macOS.

## The acceptance test — "usable"

nexus3 is usable when you can **develop nexus3 itself inside a nexus3 workspace** (map ruling; ticket 28, proven on the real 247k-LOC nexus core). Read precisely:

- **Edit / build / unit-test happen in-workspace.** `go build ./...` (cold ~32s, worst-case incremental ~11s, per-package test ~2s), `CGO_ENABLED=0`, no gcc, over a seed-by-copy sandbox. The host filesystem is never mutated by the in-workspace loop.
- **End-to-end runs happen on the host.** Two parts cannot self-host: running nexus3's own VMM inside a workspace needs nested virtualisation (`/dev/kvm` is absent inside the guest, confirmed), and macOS `nexus3-vzd` builds only on macOS. So booting a real sandbox to test it is a host operation.

This split is the definition of done for the Linux milestone, not a limitation to be removed.

## The homegrown-vs-offloaded thesis

> **NOTE — this thesis corrects the map's Destination one-liner.** The Destination sentence says the *agent runtime* is "offloaded to a library." Tickets 05/08/09/21 resolved the **opposite**: no production OSS library provides exec/PTY/streaming composable with an external microVM substrate, so nexus3 ships a **thin homegrown guest agent**. Do not read the agent runtime as offloaded.

**Homegrown (nexus3's own code):**

- The **policy core** and every `internal/core/*` module: `domain`, `store`, `lifecycle`, `driver`, `agent`, `builder`, `recovery`, `service`, `perimeter` (doc 02).
- A **thin PID-1 guest agent** — its own `nexus3.agent.v1` protocol (~10 RPCs), homegrown Go, `CGO_ENABLED=0`, one static binary per guest arch (doc 04).

**Offloaded to vetted libraries** — one row per offloaded concern. This inventory table is a headline of the spec (ticket 12):

| Concern | Chosen library | License | What nexus3 still owns at the seam |
|---|---|---|---|
| VM execution (VMM) | **Cloud Hypervisor** (Linux) | Apache-2.0 | The `driver` seam: `DialGuest`, the network-hook primitive, snapshot/fork/restore orchestration, run-state interrogation. nexus3 writes zero VMM code. |
| TUI / multiplexer | **herdr** (in-repo plugin) | Apache-2.0 | An out-of-tree `plugins/herdr/` plugin (manifest + argv shims) and the `nexus3 attach` pane path; herdr owns every PTY and pane. |
| Egress / network perimeter | **gvproxy** (`containers/gvisor-tap-vsock`) + **`elazarl/goproxy`** + **copied clawk `netfilter.AllowList`** | Apache-2.0 (gvproxy, clawk), BSD-3 (goproxy) | The `internal/core/perimeter` module: the policy artifact, the default-deny allowlist contents, the placeholder→bearer swap, the SNI→CONNECT shim, audit. |
| Image build | **BuildKit** (stock `buildkitd` + Go client) | Apache-2.0 | The `internal/core/builder` module, the `.nexus/Containerfile` contract, the per-arch guest kernel, agent-as-final-layer, the raw ext4 export. nexus3 writes zero OCI code. |
| MCP surface | **`modelcontextprotocol/go-sdk`** | MIT | `internal/mcp`, stdio-only, no network listener. |

Read this table as the load-bearing claim of the whole architecture: the security-critical and mechanism-heavy parts are vetted upstream code; nexus3's differentiator is the policy that composes them.

## Capability-to-mechanism mapping

The four old-nexus invariants carry as **capabilities, not mechanisms** (map). Restated against the resolved tickets:

| Old invariant (capability) | Status | Mechanism in nexus3 |
|---|---|---|
| Instant CoW fork/restore | **HELD, re-costed** | *Snapshot a running VM and restore it N times with memory intact*, host-RAM-bounded. Cloud Hypervisor **copies** memory per-VM (no page-sharing between siblings — that is Firecracker, not CH); a branch costs its working set, shrunk by `free_page_reporting`. Bar = seconds, 2–3 branches (tickets 13, 07). |
| Deterministic addressing | **RETIRED as a live capability** | Only **content-addressed `Project` identity** survives. nexus3 derives no host port or network identity from a sandbox ID; the control plane is vsock, port-forwarding is `ssh -L` against a declared list through `ProxyJump` (ticket 10). Do not restate "stable derivation of network identity from an opaque ID." |
| No central-daemon SPOF | **HELD** | Core-as-library plus long-lived per-sandbox supervisors. No surface-to-core RPC, so no single process whose death takes down all sandboxes or blocks any CLI operation (tickets 11, 19, 21). |
| Policy-controlled egress | **Now real** | The `perimeter` module: default-deny per-sandbox hostname allowlists, L7 TLS-MITM, credential placeholder-swap. Old nexus only aspired to this (it shipped open egress by default); nexus3 ships it (ticket 15). |

## Integration seams (the shape in one paragraph)

The `driver` seam owns the vsock transport primitive `DialGuest` (host dials guest) and a network-hook primitive, and owns **no protocol**. `agent` is a **peer of `driver`**, not nested inside it: it takes a handed `net.Conn` and speaks `nexus3.agent.v1`, knowing no substrate; `driver` must not import `agent`. `perimeter` is likewise a peer of `driver`, consuming the network-hook primitive. Surfaces (`internal/cli`, `internal/mcp`, out-of-tree `plugins/herdr/`) are **thin adapters over `service` with no surface-to-core RPC**. Details in docs 02, 03, 04, 08, 09.

## Reading guide

- **01 — Domain model.** Ubiquitous language; `Project`→`Sandbox` as one entity; identity; artifacts; host vs client.
- **02 — Core module boundary.** `internal/core/*` inventory; `agent`-as-peer-of-`driver`; single-module repo; surfaces as thin adapters; binaries.
- **03 — Execution substrate.** The `driver` seam; Cloud Hypervisor; guest kernel; `DialGuest` + network-hook.
- **04 — Agent runtime.** Thin PID-1 agent; `nexus3.agent.v1`; control + data planes; session reattach.
- **05 — Fork / restore / snapshot.** `Snapshot` artifact; uniform semantics, asymmetric cost; integrity.
- **06 — Lifecycle state machine.** Five states, twelve edges; leases; recovery.
- **07 — Image pipeline.** OCI base + `.nexus/Containerfile`; builder VM; two images; guest kernel.
- **08 — Perimeter and credentials.** `perimeter` module; gvproxy/goproxy/clawk; MITM placeholder substitution; SSH agent-forward.
- **09 — Surfaces.** CLI + MCP only; `nexus3 attach`; SSH-endpoint-ness; herdr plugin; agent selection; `nexus3 cp`.
- **10 — Salvage inventory.** Ported vs re-derived vs deleted from old nexus.
- **11 — Accepted-risk register.** Two retired gates; three live risks.
- **12 — macOS addendum.** Backlogged, validated macOS design.

---

*Sources: tickets 12, 05, 08, 09, 21 (homegrown/offloaded split); 07, 13 (fork capability); 10 (addressing); 11, 19 (no-daemon); 15 (egress); 14 (image); 28 (self-host/single-module); 42 (library inventory). Map Destination + Corrections (agent-runtime split, deterministic-addressing retirement, CoW→copy).*
