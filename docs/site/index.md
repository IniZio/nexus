---
title: "Introduction"
description: "MicroVM sandbox primitives: isolated Linux VMs with their own kernel, disk, and network"
---

# nexus3

> MicroVM sandboxes for agentic parallel development — each task gets its own isolated Linux kernel.

nexus3 runs workloads in microVM sandboxes: real Linux VMs with their own kernel, disk image, network namespace, and an in-guest agent reachable over vsock. The CLI and the MCP server expose the same primitives, so an orchestrator drives nexus3 the same way you do.

```
nexus3 run --memory 2048 nexus3-base:20260807 -- go test ./...
```

## Capabilities

| | |
|---|---|
| [Quickstart](/quickstart) | First sandbox in minutes |
| [AI agents](/ai-agents) | MCP server and per-task sandbox orchestration |
| [Sandbox model](/sandboxes/sandbox-model) | Lifecycle states and primitives |
| [Snapshots and fork](/sandboxes/snapshots-and-fork) | CoW branching from a running VM |
| [Execution substrate](/sandboxes/execution-substrate) | How the in-guest agent works |
| [Building images](/recipes/building-images) | In-VM image builds with buildkitd |
| [Docker in sandbox](/recipes/docker-in-sandbox) | Compose stacks inside a sandbox |
| [Parallel dev flow](/recipes/parallel-dev-flow) | N sandboxes in parallel, worktree-per-task |
| [Egress and perimeter](/security/egress-and-perimeter) | MITM proxy and host allowlists |
| [Resource lifecycle](/operations/resource-lifecycle) | Reaping stale sandboxes |
| [Accepted risks](/security/accepted-risks) | Known gaps and threat model |
| [CLI reference](/cli/) | All commands and flags |

## Why nexus3

- **Hardware isolation.** A sandbox is a VM, not a namespace on the host kernel.
  A compromised agent is inside a different machine.
- **Fork and restore.** `fork` and `restore` produce running children from a
  copy-on-write snapshot — each copy pays disk deltas rather than a full boot.
- **Your real working tree.** Mount a host path into the sandbox as a named disk
  at create time — dirty files, untracked files, and unpushed commits included —
  without a clean-clone step. <Badge type="danger" text="not built" />
- **Credentials stay on the host.** Egress is default-deny; the credential broker
  swaps a placeholder for the real secret at the perimeter — no real token is ever
  present inside a guest.
- **Local and self-contained.** No daemon, no hosted control plane, no account.
  Linux with KVM. macOS <Badge type="info" text="backlogged" /> — the driver seam
  exists; the second implementation does not.

## How to read this site

**These pages describe the nexus3 we are building — the target — not a report of
what is currently working.** Where the implementation has not caught up, the text
says so inline with a marker:

| Marker | Meaning |
|---|---|
| <Badge type="danger" text="not built" /> | In the target design. No implementation exists yet. |
| <Badge type="warning" text="partial" /> | Implemented, but diverges from what this page describes. |
| <Badge type="info" text="backlogged" /> | Deliberately deferred. |

**No marker means built and matching this description.** The marked set is the
reconciliation worklist: agree the target first, then close the markers.

::: info CLI surface reference
These pages introduce the design and do not document every flag. The exact current
surface is generated from source with `scripts/docs/extract-surface.sh`, which reads
the code and cannot go stale.
:::
