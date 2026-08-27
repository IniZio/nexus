---
title: "Auth, MCP and Reap"
description: "Reference for auth, mcp, reap, recover, and doctor commands"
---

# Auth, MCP and Reap

> Credential management, the MCP server, and substrate maintenance.

## nexus3 auth login

<Badge type="warning" text="partial" /> — today, only Claude Code credentials are supported; the `--agent` flag for selecting a provider profile is not yet built.

Authenticate your coding agent and persist a credential for in-guest use. Sandboxes host any coding agent — Claude Code, Codex, opencode, and others. Each agent has a provider profile: placeholder env vars, allowed egress hosts, and an optional OAuth refresh path. Credentials stay host-side; the MITM proxy swaps the placeholder for the real bearer on the wire.

**Target interface:**

```
nexus3 auth login --agent <name>
```

where `<name>` selects a provider profile (`claude`, `codex`, `opencode`, …). <Badge type="danger" text="not built" /> — other provider profiles (Codex, opencode) are not yet wired.

**Built today — Claude Code:**

```
nexus3 auth login [--from <path>] [--force]
```

Imports credentials from a dedicated Claude Code session. The dedicated session must be a separate `claude` login from your main one (rotating it logs out the main session). The source file defaults to nexus3's dedicated-session store (`~/.config/nexus3/claude-dedicated/.credentials.json`) — distinct from your main Claude login at `~/.claude/.credentials.json`. Override with `--from` only if you placed the dedicated session file elsewhere.

| Flag | Type | Default | Description |
|---|---|---|---|
| `--from <path>` | string | `~/.config/nexus3/claude-dedicated/.credentials.json` | Source Claude Code `.credentials.json` path (nexus3 dedicated-session store) |
| `--force` | bool | false | Allow overwriting an existing complete credential store |

The imported credential is **never injected into a sandbox**. It stays host-side, held by the perimeter supervisor's credential broker. A sandbox that requests agent egress (via `--agent-egress`) receives a *placeholder* string in its guest environment; the host-side MITM proxy swaps that placeholder for the real bearer token on the wire, per request. The guest never holds a credential that is valid off-host.

## nexus3 mcp

Run the nexus3 MCP server over stdio. The server exposes the full sandbox lifecycle as MCP tool calls.

```
nexus3 mcp
```

Connect a host MCP client to this process over stdio. The server exposes exactly 7 lifecycle tools: `sandbox_create`, `sandbox_list`, `sandbox_start`, `sandbox_stop`, `sandbox_pause`, `sandbox_resume`, `sandbox_remove`. Response shape: `{"ok": true|false, "data": ..., "truncated": null}`. For the full envelope and MCP scope rationale, see [Response envelopes](/cli/#response-envelopes).

## nexus3 reap

Report orphaned host resources left behind by crashed or abandoned sandboxes. With `--apply`, deletes them.

```
nexus3 reap [--apply]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--apply` | bool | false | Delete the reported orphaned resources |

## nexus3 recover

Reconcile persisted sandbox records against the live substrate. Use after a host crash or unexpected restart to bring the persisted state back in sync.

```
nexus3 recover
```

## nexus3 doctor

Report substrate availability and capability checks: KVM access, network namespace creation, vsock support, buildkitd reachability.

```
nexus3 doctor
```

## Named secrets <Badge type="danger" text="not built" /> {#named-secrets}

A named secret store lets you bind secrets to sandboxes by name rather than by env var value, so rotation happens in one place.

**What works today:** the `--secret ENV@host[,host...]` flag on `nexus3 create` reads the value of the named host environment variable and injects it as a per-host credential. The binding is evaluated at creation time — the env var must be set when you run `create`. To attach a GitHub token, pass `--repo owner/name` (scopes the push allowlist) together with `--secret GH_TOKEN@github.com,api.github.com,uploads.github.com`. Sandboxes with no `--repo` receive no GitHub credential (fail-closed).

**Target interface (not built):**

```
nexus3 secret set <name>     # value read from $<NAME> or stdin — never argv
nexus3 secret ls             # list stored secret names
nexus3 secret rm <name>      # remove a stored secret
```

The `--secret` flag on `create` will accept both forms:

```
# existing form — reads env var at creation time
nexus3 create --secret MY_TOKEN@api.example.com myproject/task-1

# target form — references the store by name; rotation applies to new sandboxes
nexus3 create --secret my-token@api.example.com myproject/task-1
```

Rotating a named secret (`nexus3 secret set <name>`) takes effect for any sandbox created after the rotation. Sandboxes already running hold the placeholder minted at their start time; restart them to pick up the new value.
