# Operations

This section covers running nexus3 in a real environment: how resources are created and reclaimed, how the egress perimeter is enforced, what risks are accepted in v1, and how orchestrators integrate.

## Pages

| Page | What it covers |
|---|---|
| [Resource Lifecycle](resource-lifecycle.md) | Intent journaling, creation phases, reaper, disk and network reclamation |
| [Egress and Perimeter](egress-and-perimeter.md) | Default-deny allowlist, MITM proxy, credential seeding, v1 non-goals |
| [Accepted Risks](accepted-risks.md) | Live and retired risk register entries |
| [Orchestrator Integration](orchestrator-integration.md) | herdr plugin surface, Orca integration, MCP tools, what is and is not yet built |

## Quick reference

**Reclaim orphaned resources**

```sh
nexus3 reap           # dry-run: classify only, delete nothing
nexus3 reap --apply   # delete orphans
```

**Inspect kernel and substrate health**

```sh
nexus3 doctor
```

**Environment variables that affect operations**

| Variable | Effect |
|---|---|
| `NEXUS3_KERNEL_PATH` | Path to the guest kernel image. All creation entry points validate this before expensive work. Missing = `"Cannot open kernel file"` from Cloud Hypervisor. |
| `NEXUS3_SUBSTRATE` | `cloudhypervisor` (default) or `none` (store-only, skip capability checks). |
| `XDG_RUNTIME_DIR` | Socket directory root. Falls back to `$TMPDIR/nexus3-<uid>`. |
