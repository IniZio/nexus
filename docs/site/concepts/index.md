# Concepts

nexus3 is a microVM sandbox manager. It boots isolated Cloud Hypervisor VMs, manages their lifecycle, and routes execution and I/O between the host and the in-guest agent. The CLI and MCP server are thin surfaces over the same Go library core.

The governing principle is **D-PD-21: the core ships primitives, never workflow verbs.** There is no `motive start`; there is `nexus3 start --label motive=X`. Every command in the [surface reference](../surface.md) is a generic primitive.

## Pages in this section

| Page | What it covers |
|------|----------------|
| [Sandbox model](sandbox-model.md) | The `Sandbox` entity — the one durable type in nexus3 |
| [Lifecycle states](lifecycle-states.md) | The five states, every legal transition, and what is explicitly illegal |
| [Execution substrate](execution-substrate.md) | Cloud Hypervisor, the `driver` seam, vsock, and the network hook |
| [Guest agent](guest-agent.md) | The in-guest PID-1 agent: control plane, data plane, session reattach |
| [Snapshots and fork](snapshots-and-fork.md) | The `Snapshot` artifact, `fork` fan-out, cost table |
| [Images](images.md) | How a rootfs is built — Containerfile contract, builder VM, shipped images |

## Quick mental model

```
┌─ host process ──────────────────────────────────────────┐
│  CLI / MCP surface  →  core library  →  driver seam     │
│                                            │             │
│  filestore (durable Sandbox records)       │ REST/unix   │
│  artifact store (Snapshots)                ▼             │
│                                     Cloud Hypervisor     │
│  ┌── supervisor (detached) ───────────────────────────┐  │
│  │  egress perimeter · MITM · OAuth broker            │  │
│  └────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────┘
               vsock (control + data)
┌─ guest VM ──────────────────────────────────────────────┐
│  nexus3-agent (PID 1)                                    │
│  │  gRPC control plane  →  Exec / Signal / Copy         │
│  │  clawk-framed data   →  PTY / stdio / output ring    │
│  └── workloads (user processes)                          │
└──────────────────────────────────────────────────────────┘
```

## Key design decisions

- **One entity.** `Sandbox` is the only durable type. There is no separate VM, Project, or Workspace entity.
- **No transient states.** An operation in flight holds a lease alongside the record; the record never enters an intermediate state.
- **Homegrown agent, not a container runtime.** The agent is a thin Go binary baked into every image. It speaks a narrow bespoke gRPC protocol; it does not implement OCI or any container spec.
- **Zero VMM code.** nexus3 drives Cloud Hypervisor over its REST API. It owns no hypervisor code.

## The acceptance test — "usable"

nexus3 is usable when you can **develop nexus3 itself inside a nexus3 sandbox** (proven on the
real 247k-LOC nexus core). Read precisely:

- **Edit / build / unit-test happen in-sandbox.** `go build ./...` (cold ~32s, worst-case
  incremental ~11s, per-package test ~2s), `CGO_ENABLED=0`, no gcc, over a seed-by-copy sandbox.
  The host filesystem is never mutated by the in-sandbox loop.
- **End-to-end VM tests run on the host.** Running nexus3's own VMM inside a sandbox requires
  nested virtualisation (`/dev/kvm` is absent inside the guest by default). Booting a real sandbox
  to test it is a host operation.

This split is the definition of done for the Linux milestone, not a limitation to be removed.

## Homegrown vs. offloaded

nexus3 ships a thin homegrown guest agent. No production OSS library provided exec/PTY/streaming
composable with an external microVM substrate, so the agent is homegrown Go, `CGO_ENABLED=0`,
one static binary per guest arch.

Everything else is offloaded to vetted libraries:

| Concern | Library | License | What nexus3 still owns at the seam |
|---|---|---|---|
| VM execution (VMM) | **Cloud Hypervisor** (Linux) | Apache-2.0 | `driver` seam: `DialGuest`, network-hook primitive, snapshot/fork/restore orchestration |
| macOS VM execution | **Virtualization.framework** via `nexus3-vzd` | — (Apple SDK) | Same `driver` seam — a second implementation, backlogged |
| Guest networking / egress | **gvproxy** | Apache-2.0 | TAP-level network hook; the perimeter policy layer on top |
| L7 MITM / TLS | **goproxy** + clawk CA | MIT | Certificate issuance; MITM ruleset for credential injection |
| Image build | **BuildKit** | Apache-2.0 | `internal/core/builder`; `.nexus/Containerfile` contract; agent-as-final-layer; raw ext4 export |
| MCP surface | **`modelcontextprotocol/go-sdk`** | MIT | `internal/mcp`, stdio-only, no network listener |

## Capability-to-mechanism mapping

Four old-nexus invariants carry forward as **capabilities**, with the mechanism resolved:

| Capability | Status | Mechanism in nexus3 |
|---|---|---|
| Instant CoW fork/restore | **HELD, re-costed** | Snapshot a running VM and restore N times with memory intact. CH copies memory per-VM (no page-sharing between siblings); a branch costs its working set, shrunk by `free_page_reporting`. Bar = seconds, 2–3 branches. |
| Deterministic addressing | **RETIRED** | Only content-addressed `Project` identity survives. nexus3 derives no host port or network identity from a sandbox ID; the control plane is vsock. |
| No central-daemon SPOF | **HELD** | Core-as-library plus long-lived per-sandbox supervisors. No surface-to-core RPC. |
| Policy-controlled egress | **LIVE** | `perimeter` module: default-deny per-sandbox hostname allowlists, L7 TLS-MITM, credential placeholder-swap. Ships on; old nexus only aspired to this. |

## Integration seams

The `driver` seam owns the vsock transport primitive `DialGuest` (host dials guest) and a
network-hook primitive, and **owns no protocol**. `agent` is a peer of `driver`, not nested inside
it: it takes a handed `net.Conn` and speaks `nexus3.agent.v1`; `driver` must not import `agent`.
`perimeter` is likewise a peer of `driver`, consuming the network-hook primitive. Surfaces
(`internal/cli`, `internal/mcp`, out-of-tree `plugins/herdr/`) are **thin adapters over `service`
with no surface-to-core RPC**.
