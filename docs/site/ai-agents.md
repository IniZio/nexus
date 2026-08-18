---
title: "AI Agents"
description: "Drive nexus3 sandboxes from an AI agent via MCP or the herdr plugin launch path"
---

# AI Agents

> One sandbox per task — isolated Linux VMs driven by a single MCP call.

nexus3 exposes its full sandbox lifecycle over an MCP server. Any MCP-compatible agent can create, start, exec, and remove sandboxes without shelling out to the CLI. The CLI and MCP server share the same underlying service — capabilities are identical.

```bash
claude mcp add --transport stdio nexus3 -- nexus3 mcp
```

---

## MCP server

Start the server manually to verify it is working:

```
nexus3 mcp
```

Register it with Claude Code once; it persists across sessions:

```bash
claude mcp add --transport stdio nexus3 -- nexus3 mcp
```

### Tools

The server exposes 7 tools that map directly to sandbox lifecycle operations:

| Tool | Description |
|---|---|
| `sandbox_create` | Mint a new sandbox record (project, name, remove_on_exit) |
| `sandbox_list` | List all sandboxes |
| `sandbox_start` | Start a created or stopped sandbox |
| `sandbox_stop` | Stop a running sandbox |
| `sandbox_pause` | Pause a running sandbox |
| `sandbox_resume` | Resume a paused sandbox |
| `sandbox_remove` | Remove a sandbox record |

### MCP gaps <Badge type="danger" text="not built" />

The MCP surface cannot do today:

- Set a custom `AllowedHosts` list at creation time (creation args are limited to project/name/remove_on_exit).
- Seed agent credentials on MCP-created sandboxes.
- Stream logs or attach an interactive pane.

---

## Per-task sandbox orchestration

The canonical pattern: one sandbox per task, labelled for fleet selection and teardown.

```
# create a dedicated sandbox for this task
nexus3 create myproject/task-42 \
  --image nexus3-base:20260807 \
  --label task-id=42 \
  --memory 4096

# run the agent inside it
nexus3 exec myproject/task-42 -- /usr/local/bin/claude --task "fix the flaky test"

# tear down when done
nexus3 rm myproject/task-42
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

For higher throughput, the herdr plugin's `launch` path (below) boots the sandbox and execs the agent in a single call and wires credential seeding automatically.

---

## herdr plugin launch path

`__herdr-plugin` is the private CLI shim between nexus3 and the herdr workspace plugin. The `launch` subcommand is the primary path for booting an agent sandbox from an orchestrator:

```
__herdr-plugin launch --agent-egress \
  nexus3-base:20260807 \
  /usr/local/bin/claude
```

- `<command>` must be an absolute path (e.g. `/usr/local/bin/claude`).
- `--agent-egress` scopes egress to `api.anthropic.com` and `platform.claude.com`, and seeds guest credentials from the host broker. Without this flag the sandbox runs in AllowAll mode with no MITM and no credential injection.
- The command boots the sandbox, seeds credentials if `--agent-egress`, then execs the command in-guest.

**Worktree-native parallel flow** <Badge type="danger" text="not built" />: the target pattern creates a `git worktree` on the host per sandbox, passes it via `-v`, and the agent commits directly into the mounted worktree. No extraction step; teardown calls `git worktree remove`. Each task gets its own branch and its own mount — sandboxes are created independently (not forked) so mounts are never shared between concurrent VMs.

### Public agent launch surface <Badge type="danger" text="not built" />

A single `nexus3 agent launch` public command will wrap `__herdr-plugin launch` with a stable, versioned interface for external orchestrators. Today, external callers use the private `__herdr-plugin` shim or the MCP tools.

### In-guest credential refresh <Badge type="danger" text="not built" />

The host credential broker can deliver tokens to the guest over vsock. An in-guest Claude Code instance cannot yet request a refresh independently — there is no MCP handler in the guest for token rotation. This blocks the pattern where the in-guest agent acquires a fresh token mid-task without host intervention.

---

## What's built

| Surface | Built | Live-proven |
|---|---|---|
| MCP 7-tool surface | Yes | Yes |
| `__herdr-plugin launch` | Yes | Yes |
| `--agent-egress` credential seeding | Yes | Yes |
| `__herdr-plugin space-create[-from-file]` | Yes | Yes |
| `nexus3 recipe` CLI (Orca) | Yes | Yes |
| `__herdr-plugin launch -v` | No | — |
| `nexus3 agent launch` public command | No | — |
| MCP log streaming / pane attach | No | — |
| In-guest credential refresh via MCP | No | — |
