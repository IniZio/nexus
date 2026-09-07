---
title: "Claude Code plugin"
description: "Install the nexus3 plugin so any Claude Code session can discover and delegate work to microVM sandboxes"
---

# Claude Code plugin

> Install once from the nexus3 repo — every Claude Code session on this host picks up the plugin automatically.

The `nexus3` Claude Code plugin registers a skill namespace and an MCP server so a main agent working in any repo can discover nexus3, configure egress policy, and delegate a unit of work into a microVM sandbox. It lives in the nexus3 repo at `plugins/claude/` and is installed by symlink, so edits to the source are live immediately — no reinstall needed after content changes.

---

## Install

From the nexus3 repo root:

```sh
make install-plugin
```

That target:

1. Creates `~/.claude/plugins/nexus3 → <nexus3-repo>/plugins/claude` (idempotent — re-running it is safe).
2. Registers the plugin with `claude plugin marketplace add` if it is not already listed.
3. Activates it with `claude plugin install nexus3@nexus3 --yes` if it is not already installed.

Verify the MCP server is connected from any repo:

```sh
claude mcp list
# plugin:nexus3:nexus3: nexus3 mcp - ✔ Connected
```

## Uninstall

```sh
rm ~/.claude/plugins/nexus3
```

---

## What the plugin provides

### Skills

Skills live under the `nexus3:` namespace and are available in every Claude Code session after install. The plugin ships:

| Skill | Purpose |
|---|---|
| `nexus3` | Reference dispatcher — the entry point for any nexus3 question |
| `nexus3-onboard` | Walks an agent through creating `nexus3.yaml` and `.nexus/Containerfile` for a repo that does not have them yet |
| `nexus3-egress` | Egress policy authoring — allowlist construction, secret brokering, verification |
| `nexus3-delegate` | Delegation workflow — create a worktree-bound sandbox, dispatch a brief to an in-guest agent, collect the result, and tear down |

Reach for `nexus3` first; it routes to the others.

### MCP server

The plugin's `.mcp.json` wires `nexus3 mcp` as a stdio transport. The same 13-tool surface described in [AI agents](/ai-agents) is available without any manual `claude mcp add` call.

---

## `nexus3.yaml` — per-repo egress configuration

Repos that already have a `nexus3.yaml` at their root are pre-configured. A worktree sandbox created for that repo reads the file from the base branch (`git show refs/remotes/origin/HEAD:nexus3.yaml`) as its trust anchor.

Repos without one can use the `nexus3-onboard` skill to generate one. The minimum structure:

```yaml
version: 1
egress:
  policy:
    - host: api.github.com
      paths:
        - "/repos/owner/repo"
        - "/repos/owner/repo/**"
    - host: github.com
      paths:
        - "/owner/repo/**"
        - "/owner/repo.git/**"
  secrets:
    - env: GH_TOKEN
      hosts: [github.com, api.github.com]
```

::: warning Keep `policy` paths scoped to the repo
Never write `/**` at the host root. A path-scoped allowlist is what keeps a compromised guest from reaching other repos or the GraphQL write channel with the same token. See [Egress and perimeter](/security/egress-and-perimeter).
:::

---

## Existing ad-hoc MCP registration

If you registered nexus3's MCP server manually before the plugin existed — `claude mcp add --transport stdio nexus3 -- nexus3 mcp` or a direct `~/.claude.json` entry — you now have two registrations with the same name: one from the plugin's `.mcp.json` (pointing at the installed binary) and one from the manual entry (pointing at whatever binary the manual command referenced). Remove the manual entry to avoid the ambiguity.

---

## What's built

| Surface | Built | Live-proven |
|---|---|---|
| `make install-plugin` symlink + registration | Yes | Yes |
| Plugin MCP server (`plugin:nexus3:nexus3`) | Yes | Yes |
| `nexus3` dispatcher skill | Yes | Yes |
| `nexus3-onboard` skill | Yes | Yes |
| `nexus3-egress` skill | Yes | Yes |
| `nexus3-delegate` skill | Yes | Yes |
| `nexus3 plugin install` CLI verb | No | — |
