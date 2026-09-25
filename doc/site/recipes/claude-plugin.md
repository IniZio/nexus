---
title: "Claude Code plugin"
description: "Install the nexus plugin so any Claude Code session can discover and delegate work to microVM sandboxes"
---

# Claude Code plugin

> Install once from the nexus repo — every Claude Code session on this host picks up the plugin automatically.

The `nexus` Claude Code plugin registers a skill and an MCP server so a main agent working in any repo can discover nexus, configure egress policy, and delegate a unit of work into a microVM sandbox. It requires the `nexus` binary on `PATH` — see [Install](/quickstart#install).

---

## Install

```sh
claude plugin marketplace add IniZio/nexus
claude plugin install nexus@nexus
```

Verify the MCP server is connected from any repo:

```sh
claude mcp list
# plugin:nexus:nexus: nexus mcp - ✔ Connected
```

## Uninstall

```sh
claude plugin uninstall nexus@nexus
claude plugin marketplace remove nexus
```

---

## What the plugin provides

### Skills

One skill, `nexus:nexus`, is available in every Claude Code session after install. It is a dispatcher: its routing table opens the reference that matches the question — sandbox lifecycle, named volumes, guest setup, running an agent in a sandbox, creating a PR from inside a sandbox, first-run onboarding (`.nexus/config.yaml` and `.nexus/Containerfile`), egress policy authoring, and delegating a unit of work into a worktree sandbox.

Three slash commands ship alongside it: `/nexus:nexus-init`, `/nexus:nexus-delegate`, and `/nexus:nexus-doctor`.

### MCP server

The plugin's `.mcp.json` wires `nexus mcp` as a stdio transport. The same 13-tool surface described in [AI agents](/ai-agents) is available without any manual `claude mcp add` call.

---

## `.nexus/config.yaml` — per-repo egress configuration

Repos that already have a `.nexus/config.yaml` are pre-configured. A worktree sandbox reads the file from its own checkout — the same file `nexus create --file` builds from — so a change takes effect on the next worktree-sandbox create for that checkout; no push to the default branch is needed.

Repos without one can run `/nexus:nexus-init` to generate one. The minimum structure:

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

If you registered nexus's MCP server manually before the plugin existed — `claude mcp add --transport stdio nexus -- nexus mcp` or a direct `~/.claude.json` entry — you now have two registrations with the same name: one from the plugin's `.mcp.json` (pointing at the installed binary) and one from the manual entry (pointing at whatever binary the manual command referenced). Remove the manual entry to avoid the ambiguity.

---

## What's built

| Surface | Built | Live-proven |
|---|---|---|
| Marketplace install (`claude plugin marketplace add IniZio/nexus`) | Yes | No |
| Plugin MCP server (`plugin:nexus:nexus`) | Yes | Yes |
| `nexus` dispatcher skill (onboard, egress, delegate references) | Yes | Yes |
| `/nexus:nexus-init`, `-delegate`, `-doctor` commands | Yes | Yes |
| `nexus plugin install` CLI verb | No | — |
