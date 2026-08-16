# Orchestrator Integration

nexus3 exposes three sandbox-creation surfaces to orchestrators: the herdr plugin shim, the Orca integration, and an MCP server. This page covers what is real and proven versus what is planned.

## Sandbox creation entry points

Three paths exist today, each targeting a different orchestrator or use case.

| Entry point | Caller | Status |
|---|---|---|
| `__herdr-plugin launch` | herdr plugin | Real and proven |
| `__herdr-plugin space-create[-from-file]` | herdr plugin | Real and proven |
| `nexus3 orca` | Orca workspace integration | Partially real (see below) |
| MCP tools | Any MCP client | Real and proven |

## herdr plugin surface

`__herdr-plugin` is a private CLI shim between nexus3 and the in-repo herdr plugin (`plugins/herdr/`). It is **not part of the versioned `--json` machine contract** exposed to external callers. The ABI version is an integer (`herdrPluginABIVersion = "1"`) probed by `build.sh` to detect skew between the installed plugin manifest and the binary.

Subcommands implemented today:

| Subcommand | Purpose |
|---|---|
| `abi` | Print ABI version integer |
| `context-cwd` | Return the current working directory context |
| `workspaces` | List all sandboxes |
| `attach` | Attach to an existing sandbox |
| `create` | Create a sandbox record |
| `logs` | Stream sandbox logs |
| `doctor` | Run substrate health checks |
| `open-pane` | Open an interactive pane in a workspace |
| **`launch [--agent-egress] <image-ref> <command> [args...]`** | **Agent sandbox path — boots a sandbox and launches command** |
| `space-create <ref>` | Workspace path — wire herdr space onto an existing sandbox |
| `space-create-from-file <ref>` | Dockerfile-based workspace path |
| `space-open-pane` / `space-pause` / `space-resume` / `space-remove` / `space-list` | Space lifecycle |

### Agent launch path

`__herdr-plugin launch [--agent-egress] <image-ref> <command> [args...]`

- `<command>` must be an absolute path (e.g. `/usr/local/bin/claude`).
- `--agent-egress` sets `AllowedHosts` to `AgentEgressHosts()` (`api.anthropic.com` + `platform.claude.com`) and enables credential seeding. Without this flag, the sandbox runs in AllowAll mode with no MITM and no credentials.
- The launch boots the sandbox, seeds guest credentials if `--agent-egress`, and exec's the command in-guest.

## Orca integration

### What works

Two Orca-adjacent assets are real and CLI-proven:

1. **CLI recipe** (`nexus3 recipe`): `vm recipe doctor --provision` was proven green against the real Orca AppImage (v1.4.179) on 2026-08-10. This is a CLI operation, not a GUI one.

2. **Direct-SSH fallback**: `cmd_orca.go:129` (`buildOrcaConnectionJSON`) wires SSH over vsock so that a workspace opened via direct SSH reaches the guest. This is CLI-accessible and functions independently of the GUI composer.

### What was abandoned

The **GUI composer path** is abandoned as the primary integration. Root cause: the Orca remote client's "Run on" picker does not show the nexus3 VM recipe, and the host is headless to the operator. The GUI-composer sourcing rule (origin/main vs open-folder branch HEAD) was never GUI-verified and remains unverified.

The herdr plugin (`__herdr-plugin space-create[-from-file]`) is the current primary workspace creation path.

### What git hosts Orca can reach

Verified from source (`internal/cli/cmd_orca.go`).

Orca builds `AllowedHosts` as `AgentEgressHosts()` plus `gitHostsFromURL(env.RepoURL)`.
`gitHostsFromURL` returns the repo hostname for non-GitHub forges and **nil for
any GitHub host** (D-PD-23 / N-AC1). An orca sandbox pointed at a GitHub repo
therefore reaches only `api.anthropic.com` and `platform.claude.com`. GitHub
egress is reserved for a future `--secret` bind on a human/git sandbox.

## MCP surface

The MCP server (`internal/mcp/server.go`) exposes 7 tools to any MCP client. These map directly to sandbox lifecycle operations.

| Tool | Description |
|---|---|
| `sandbox_create` | Mint a new sandbox record (project, name, remove_on_exit) |
| `sandbox_list` | List all sandboxes (no args) |
| `sandbox_start` | Start a created or stopped sandbox |
| `sandbox_stop` | Stop a running sandbox |
| `sandbox_pause` | Pause a running sandbox |
| `sandbox_resume` | Resume a paused sandbox |
| `sandbox_remove` | Remove a sandbox record |

### MCP gaps (not yet built)

The MCP surface cannot do today:

- Start a sandbox with a custom `AllowedHosts` list via MCP (creation args are limited to project/name/remove_on_exit).
- Seed agent credentials on MCP-created sandboxes.
- Stream logs or attach an interactive pane.

## Not yet built

### I1 — Canonical creation surface

A single `nexus3 agent launch` public command that wraps `__herdr-plugin launch` with a stable, versioned interface for external orchestrators. Currently, external callers must use the private `__herdr-plugin` shim or the MCP tools.

### I2 — MCP parity backfill

MCP tools for the full `__herdr-plugin` subcommand surface: logs, pane attachment, workspace create-from-file, space lifecycle verbs.

### In-guest claude authentication via MCP

The broker can deliver credentials over vsock; the guest does not yet have an MCP handler that exposes token refresh to an in-guest Claude Code instance. This unblocks the path where the in-guest agent requests a fresh token independently.

### Orca workspace-creation GUI path

The Orca GUI composer path for creating a nexus3-backed workspace was abandoned (see above). Re-enabling it would require the Orca plugin to appear in the "Run on" picker, which requires GUI-side changes outside this repo.

## Summary: real vs planned

| Item | Real | Proven live |
|---|---|---|
| `__herdr-plugin launch` | Yes | Yes (agent sandbox) |
| `__herdr-plugin space-create[-from-file]` | Yes | Yes |
| `nexus3 recipe` CLI | Yes | Yes (Orca AppImage v1.4.179) |
| Direct-SSH Orca fallback | Yes | CLI-proven |
| MCP 7-tool surface | Yes | Yes |
| Orca GUI composer | No | Abandoned |
| `nexus3 agent launch` public command | No | — |
| MCP log streaming / pane attach | No | — |
| In-guest claude auth via MCP | No | — |
