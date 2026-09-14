---
title: "Auth, MCP and Reap"
description: "Reference for auth, mcp, reap, recover, and doctor commands"
---

# Auth, MCP and Reap

> Credential management, the MCP server, and substrate maintenance.

## nexus3 auth login

Manage credentials for the coding agent running inside sandboxes.

### Claude Code (default)

`nexus3 auth login` is **no longer needed for Claude Code**. claude-code sandboxes receive the host's `~/.claude` directory as a live read-write virtiofs mount. The guest uses the host's real `.credentials.json` and refreshes its own token without any host-side brokering.

To authenticate for use in sandboxes, authenticate on the **host**:

```sh
claude login
```

The next sandbox you create picks up the credentials automatically.

Running sandboxes are not updated — virtiofs mounts are create-time state. Recreate a sandbox to switch it to the live-mount credential model (see [Egress and perimeter: recreate rule](/security/egress-and-perimeter#recreate-rule-r-7)).

```
nexus3 auth login
```

Prints a notice explaining that auth login is not needed for claude-code, then exits zero.

### Other agent profiles <Badge type="warning" text="partial" />

For agent profiles that still use the placeholder+broker credential model, `auth login --agent <name>` imports or verifies the credential:

```
nexus3 auth login --agent <name> [--from <path>] [--force]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent <name>` | string | (see below) | Agent profile to authenticate |
| `--from <path>` | string | agent-specific | Source credential file path |
| `--force` | bool | false | Allow overwriting an existing complete credential store |

`--agent` selects which provider profile to authenticate. Without `--agent`, the command prints the "no longer needed" notice for claude-code. <Badge type="warning" text="partial" /> — only one additional profile (`claude-code`) is registered today; the multi-profile path (`--agent codex`, `--agent opencode`) is not yet wired for other agents.

The imported credential is **never injected into a sandbox as a real value**. It stays host-side, held by the perimeter supervisor's credential broker. A sandbox that requests agent egress receives a *placeholder* string in its guest environment; the host-side MITM proxy swaps that placeholder for the real bearer token on the wire, per request.

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
