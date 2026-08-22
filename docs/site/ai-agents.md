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

<Badge type="tip" text="built" /> — the flat verbs shown above are the real CLI surface; `nexus3 sandbox create` remains an equivalent alias. See [CLI sandbox commands](/cli/sandbox-commands). <!-- cli-spelling-exempt -->

For higher throughput, the herdr plugin's `launch` path (below) boots the sandbox and execs the agent in a single call, and wires the placeholder credential seed automatically.

---

## Choosing an agent: `--agent` <Badge type="tip" text="built" />

`nexus3 create --agent <name>` records which agent profile a sandbox is for. The profile is not a label — it decides the credential seed, the egress allowlist, and the guest environment the sandbox gets:

```sh
nexus3 create myproject/task-42 --agent claude-code --image nexus3-agent-base
```

The chosen name is persisted on the record and shown in the `AGENT` column of `nexus3 ps` and the herdr overlay, so a sandbox never disagrees with itself about which agent is running inside it.

### Registered profiles

| Name | Placeholder env var | Reachable hosts |
|---|---|---|
| `claude-code` (default) | `CLAUDE_CODE_OAUTH_TOKEN` | `api.anthropic.com`, `platform.claude.com` |

::: warning One profile is registered today
`claude-code` is the only entry in the registry, and it is the default when `--agent` is omitted. The mechanism is deliberately declarative — adding an agent means adding one `AgentProfile` value, and no call site branches on the name — but until a second profile exists, `--agent` selects from a set of one.

An unregistered name is **refused**, never silently defaulted: a typo must not be answered with the wrong credential seed.
:::

### What a profile carries

| Field | Effect |
|---|---|
| `PlaceholderEnvVar` | the variable the guest sees; holds a placeholder, never a real token |
| `EgressHosts` | the entire allowlist for that sandbox — everything else is denied |
| `APIKeyEnvVar` | the variable the MITM proxy swaps host-side, on the wire |
| `CACertEnvVars` | how the agent is told to trust the MITM CA (`NODE_EXTRA_CA_CERTS` for Node-based agents) |
| `GuestEnv` | extra guest environment, e.g. disabling telemetry that would retry against a default-deny perimeter |

The guest never holds a real credential. See [egress and perimeter](/security/egress-and-perimeter).

---

## herdr plugin launch path

`herdr` is the plugin-private command group between nexus3 and the herdr workspace plugin. The `launch` subcommand is the primary path for booting an agent sandbox from an orchestrator:

```
nexus3 herdr launch --agent-egress \
  nexus3-base:20260807 \
  /usr/local/bin/claude
```

- `<command>` must be an absolute path (e.g. `/usr/local/bin/claude`).
- `--agent-egress` hands the booted VM to a detached perimeter supervisor (`nexus3 __supervisor`, ephemeral mode) which owns the whole zero-credential perimeter: the egress allowlist (`api.anthropic.com`, `platform.claude.com`), the MITM proxy, the credential broker, the CA seed, and the guest placeholder seed. The guest receives a placeholder; the proxy swaps it for the real bearer token host-side, on the wire.
- Without the flag no supervisor is started, and therefore no perimeter process pumps the guest's network device — the sandbox has **no egress at all**, not open egress.
- The command boots the sandbox, hands it to the supervisor if `--agent-egress`, verifies the guest received both the placeholder credential and the CA cert, then execs the command in-guest with the seeded credential sourced from `/run/nexus3/cred.env`.
- Teardown stops the supervisor and waits for it to exit; a parent-watchdog pipe tears the VM down even if the caller is `SIGKILL`ed.

**Worktree-native parallel flow**: create a `git worktree` on the host per sandbox, pass it via `--mount`, and the agent commits directly into the mounted worktree. No extraction step; teardown calls `git worktree remove`. Each task gets its own branch and its own mount — sandboxes are created independently (not forked) so mounts are never shared between concurrent VMs.

### Public agent launch surface <Badge type="danger" text="not built" />

A single `nexus3 agent launch` public command will wrap `nexus3 herdr launch` with a stable, versioned interface for external orchestrators. Today, external callers use the `herdr` group or the MCP tools.

### In-guest credential refresh <Badge type="danger" text="not built" />

The host credential broker can deliver tokens to the guest over vsock. An in-guest Claude Code instance cannot yet request a refresh independently — there is no MCP handler in the guest for token rotation. This blocks the pattern where the in-guest agent acquires a fresh token mid-task without host intervention.

---

## What's built

| Surface | Built | Live-proven |
|---|---|---|
| MCP 7-tool surface | Yes | Yes |
| `nexus3 herdr launch` | Yes | Yes |
| `--agent-egress` perimeter handoff (MITM + placeholder swap) | Yes | Yes |
| `nexus3 herdr space-create` / `herdr create-from-file` | Yes | Yes |
| `nexus3 recipe` CLI (Orca) | Yes | Yes |
| `nexus3 herdr launch -v` | No | — |
| `nexus3 agent launch` public command | No | — |
| MCP log streaming / pane attach | No | — |
| In-guest credential refresh via MCP | No | — |
