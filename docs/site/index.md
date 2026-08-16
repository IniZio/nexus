# nexus3 documentation

nexus3 is a KVM-backed microVM sandbox manager for isolated, reproducible workloads. Each sandbox
is a full Linux VM with its own disk image, network namespace, and vsock channel. nexus3 supports
fork-from-running (CoW disk clone of a live VM), workspace capture (host working tree — dirty
files, untracked files, and unpushed commits included), parallel fan-out with result harvesting,
and a seven-tool MCP surface for agent orchestration. It runs on Linux (KVM) with a macOS path
backlogged.

---

## Start here

If you are new to nexus3, read in this order:

1. **[Getting started](getting-started.md)** — install prerequisites, create your first sandbox, run a command, open a shell, and clean up.
2. **[Sandbox model](concepts/sandbox-model.md)** — what a sandbox is, how it is identified, and what the envelope contains.
3. **[Lifecycle states](concepts/lifecycle-states.md)** — the state machine every sandbox moves through, and what transitions are illegal.
4. **[Surface reference](surface.md)** — every command and flag, verified against the CLI source. Return here whenever you need the exact syntax for a command.

---

## Concepts

Foundational explanations of how nexus3 works.

| Page | What it covers |
|---|---|
| [Overview](concepts/index.md) | Quick mental model and key design decisions |
| [Sandbox model](concepts/sandbox-model.md) | The `Sandbox` struct, identity, labels, fork lineage |
| [Lifecycle states](concepts/lifecycle-states.md) | State machine, transition table, illegal transitions, recovery |
| [Execution substrate](concepts/execution-substrate.md) | Cloud Hypervisor driver, vsock transport, network, resource limits |
| [Guest agent](concepts/guest-agent.md) | PID-1 startup, gRPC control plane, session reattach, workload agnosticism |
| [Snapshots and fork](concepts/snapshots-and-fork.md) | Snapshot semantics, fork cost model, restore-in-place |
| [Images](concepts/images.md) | How images are built, the builder VM, the two shipped images, content addressing |

---

## Guides

Task-oriented walkthroughs.

| Page | What it covers |
|---|---|
| [Building images](guides/building-images.md) | `image build`, Containerfiles, builder VM, layer caching, disk sizing |
| [Docker in a sandbox](guides/docker-in-sandbox.md) | Adding Docker to a guest image and starting dockerd manually (not automatic) |
| [Parallel development flow](guides/parallel-dev-flow.md) | Seed → work → harvest → integrate: fan N sandboxes out and collect results |
| [Workspace capture](guides/workspace-capture.md) | Capturing a live host working tree (dirty + untracked + unpushed) into a sandbox |

---

## Operations

Reference material for running nexus3 in production.

| Page | What it covers |
|---|---|
| [Overview](operations/index.md) | Quick reference: reap, doctor, environment variables |
| [Resource lifecycle](operations/resource-lifecycle.md) | Intent journaling, reaper classification, disk and network reclamation |
| [Egress and perimeter](operations/egress-and-perimeter.md) | Default-deny allowlist, MITM proxy, credential seeding, v1 non-goals |
| [Accepted risks](operations/accepted-risks.md) | Live and retired risk register entries |
| [Orchestrator integration](operations/orchestrator-integration.md) | herdr plugin surface, Orca integration, MCP tools, what is and is not yet built |

---

## Surface reference

**[Surface reference](surface.md)** — the complete nexus3 command surface in one page. Every
command listed is verified against `internal/cli/surface_parity_test.go` and the CLI source.
Includes the departures table (how nexus3 differs from clawk, microsandbox, and OpenShell) and
the known gaps and open questions register.

**[Surface contract](surface-contract.md)** — normative rules: what the canonical API is, the
parity invariant (N-AC4), the envelope rules, and the decision record. Read this to understand
*why* the surface is shaped the way it is.
