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

### Tools <Badge type="tip" text="built" />

The server exposes 13 tools: nine covering sandbox lifecycle and execution, and four covering delegation into a worktree-bound sandbox.

| Tool | Description |
|---|---|
| `sandbox_create` | Mint a new sandbox record (project, name, remove_on_exit) |
| `sandbox_list` | List all sandboxes |
| `sandbox_start` | Start a created or stopped sandbox |
| `sandbox_stop` | Stop a running sandbox |
| `sandbox_pause` | Pause a running sandbox |
| `sandbox_resume` | Resume a paused sandbox |
| `sandbox_remove` | Remove a sandbox record |
| `sandbox_exec` | Run a command inside an existing sandbox; returns `{exit_code, stdout, stderr, stdout_bytes, stderr_bytes}` with truncation metadata for output exceeding 64 KiB |
| `sandbox_run` | Ephemeral create+boot+exec+remove in one call; args `{image, argv, memory?, vcpus?, project?, name?}`; the sandbox is removed unconditionally on completion |
| `delegate_worktree_create` | Create a worktree-bound sandbox for a host repo path; `allowed_branches` is rejected if set — branch policy is derived from the worktree's current branch |
| `delegate_agent_dispatch` | Submit a brief to the in-guest agent; blocks until the brief is delivered, not until the work is done |
| `delegate_agent_poll` | Read the guest worktree's `git log`, `git status`, and branch name to detect progress |
| `delegate_teardown` | Remove the sandbox and its herdr space; the host git worktree is not removed |

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

For higher throughput, the herdr plugin's `launch` path (below) boots the sandbox and execs the agent in a single call, and wires the credential mount automatically.

---

## Choosing an agent: `--agent` <Badge type="tip" text="built" />

`nexus3 create --agent <name>` records which agent profile a sandbox is for. The profile is not a label — it decides the credential delivery, the egress allowlist, and the guest environment the sandbox gets:

```sh
nexus3 create myproject/task-42 --agent claude-code --image nexus3-agent-base
```

The chosen name is persisted on the record and shown in the `AGENT` column of `nexus3 ps` and the herdr overlay, so a sandbox never disagrees with itself about which agent is running inside it.

### Registered profiles

| Name | Credential delivery | Reachable hosts |
|---|---|---|
| `claude-code` (default) | host `~/.claude` live-mounted read-write at `/root/.claude` | `api.anthropic.com`, `platform.claude.com` |

::: warning One profile is registered today
`claude-code` is the only entry in the registry, and it is the default when `--agent` is omitted. The mechanism is deliberately declarative — adding an agent means adding one `AgentProfile` value, and no call site branches on the name — but until a second profile exists, `--agent` selects from a set of one.

An unregistered name is **refused**, never silently defaulted: a typo must not be answered with the wrong credential delivery path.
:::

### What a profile carries

| Field | Effect |
|---|---|
| `CredDirLiveMount` | when true, the host credential directory is mounted read-write into the guest instead of using placeholder seeding |
| `EgressHosts` | the entire allowlist for that sandbox — everything else is denied |
| `CACertEnvVars` | how the agent is told to trust the MITM CA (`NODE_EXTRA_CA_CERTS` for Node-based agents) |
| `GuestEnv` | extra guest environment, e.g. disabling telemetry that would retry against a default-deny perimeter |

For `claude-code`, no placeholder is seeded and no broker swap occurs — the guest reads and refreshes its own real credential. See [egress and perimeter](/security/egress-and-perimeter).

---

## Permission mode

Claude Code sandboxes run in `auto` permission mode. No process in a sandbox carries `--dangerously-skip-permissions`, and no seeded file sets `bypassPermissions` or `skipDangerousModePermissionPrompt`. The host's `~/.claude/settings.json` is mounted live and already declares `permissions.defaultMode = "auto"`.

The `claudeReadyMatch` detector that drives `delegate_agent_dispatch` is calibrated for the auto-mode footer (`"auto mode on"`), not the manual-mode (`"? for shortcuts"`) or bypass-mode footer. Every guest launch passes `--permission-mode auto` explicitly, because a bare `claude` starts in whatever mode the mounted host `settings.json` selects.

---

## herdr plugin launch path

`herdr` is the plugin-private command group between nexus3 and the herdr workspace plugin. The `launch` subcommand is the primary path for booting an agent sandbox from an orchestrator:

```
nexus3 herdr launch --agent-egress \
  nexus3-base:20260807 \
  /usr/local/bin/claude
```

- `<command>` must be an absolute path (e.g. `/usr/local/bin/claude`).
- `--agent-egress` hands the booted VM to a detached perimeter supervisor (`nexus3 __supervisor`, ephemeral mode) which owns the egress allowlist (`api.anthropic.com`, `platform.claude.com`), the MITM proxy, the CA seed, and the credential guardian. For claude-code, the credential guardian monitors `~/.claude/.credentials.json` and proactively refreshes it; the guest reads the file directly from the live mount.
- Without the flag no supervisor is started, and therefore no perimeter process pumps the guest's network device — the sandbox has **no egress at all**, not open egress.
- Teardown stops the supervisor and waits for it to exit; a parent-watchdog pipe tears the VM down even if the caller is `SIGKILL`ed.

**Worktree-native parallel flow**: create a `git worktree` on the host per sandbox, pass it via `--mount`, and the agent commits directly into the mounted worktree. No extraction step; teardown calls `git worktree remove`. Each task gets its own branch and its own mount — sandboxes are created independently (not forked) so mounts are never shared between concurrent VMs.

### Public agent launch surface <Badge type="danger" text="not built" />

A single `nexus3 agent launch` public command will wrap `nexus3 herdr launch` with a stable, versioned interface for external orchestrators. Today, external callers use the `herdr` group or the MCP tools.

---

## Port auto-forward <Badge type="tip" text="built" />

When a guest process binds a TCP port in the range 1024–11023, nexus3 auto-discovers it (via `/proc/net/tcp` polling inside the sandbox) and makes it available at the same port number on the host at `127.0.0.1:<port>`. A remote herdr client sees the port forwarded to `127.0.0.1:<port>` on the laptop, also at the same number.

This requires **herdr ≥ 0.9** on the remote client. nexus3 declares `min_herdr_version = "0.9.0"` and plugin ABI `"3"` in the herdr plugin manifest; herdr versions below 0.9 fail the ABI probe at install time with a clear version message.

Port-forward state is persisted under `~/.config/herdr/portfwd/` on the herdr host. Forwarded ports appear in the herdr overlay alongside the sandbox that owns them. When the guest listener closes, nexus3 cancels the forward and the port disappears from the laptop within the reconcile interval.

> Note: the same-number invariant (`127.0.0.1:5173` on the guest → `127.0.0.1:5173` on the host) is preserved end-to-end. Renumbering would break OAuth redirect URIs and Vite HMR WebSocket URLs.

---

## What's built

| Surface | Built | Live-proven |
|---|---|---|
| MCP 7-tool surface | Yes | Yes |
| `nexus3 herdr launch` | Yes | Yes |
| `--agent-egress` perimeter handoff (MITM + credential guardian) | Yes | Yes |
| `nexus3 herdr space-create` / `herdr create-from-file` | Yes | Yes |
| `nexus3 recipe` CLI (Orca) | Yes | Yes |
| `~/.claude` live-mount credential delivery for claude-code | Yes | Yes |
| auto permission mode (no `--dangerously-skip-permissions`) | Yes | Yes |
| git SSH relay to GitHub via host ssh-agent | Yes | Yes |
| Port auto-forward (herdr ≥ 0.9, ABI 3) | Yes | Yes |
| `nexus3 herdr launch -v` | No | — |
| `nexus3 agent launch` public command | No | — |
| MCP log streaming / pane attach | No | — |
