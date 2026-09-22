---
title: "Auth, MCP and Reap"
description: "Reference for auth, mcp, reap, recover, and doctor commands"
---

# Auth, MCP and Reap

> Credential management, the MCP server, and substrate maintenance.

## nexus auth login

Manage credentials for the coding agent running inside sandboxes.

### Claude Code (default)

`nexus auth login` is **no longer needed for Claude Code**. claude-code sandboxes receive the host's `~/.claude` directory as a live read-write virtiofs mount. The guest uses the host's real `.credentials.json` and refreshes its own token without any host-side brokering.

To authenticate for use in sandboxes, authenticate on the **host**:

```sh
claude login
```

The next sandbox you create picks up the credentials automatically.

Running sandboxes are not updated — credential configuration is create-time state. Recreate a sandbox to receive the broker/placeholder credential model; `nexus auth login` is required again for claude-code after recreation (see [Egress and perimeter: recreate rule](/security/egress-and-perimeter#recreate-rule-r-7)).

```
nexus auth login
```

Prints a notice explaining that auth login is not needed for claude-code, then exits zero.

### Other agent profiles <Badge type="warning" text="partial" />

For agent profiles that still use the placeholder+broker credential model, `auth login --agent <name>` imports or verifies the credential:

```
nexus auth login --agent <name> [--from <path>] [--force]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent <name>` | string | (see below) | Agent profile to authenticate |
| `--from <path>` | string | agent-specific | Source credential file path |
| `--force` | bool | false | Allow overwriting an existing complete credential store |

`--agent` selects which provider profile to authenticate. Without `--agent`, the command prints the "no longer needed" notice for claude-code. <Badge type="warning" text="partial" /> — only one additional profile (`claude-code`) is registered today; the multi-profile path (`--agent codex`, `--agent opencode`) is not yet wired for other agents.

The imported credential is **never injected into a sandbox as a real value**. It stays host-side, held by the perimeter supervisor's credential broker. A sandbox that requests agent egress receives a *placeholder* string in its guest environment; the host-side MITM proxy swaps that placeholder for the real bearer token on the wire, per request.

## nexus mcp

Run the nexus MCP server over stdio. The server exposes the full sandbox lifecycle as MCP tool calls.

```
nexus mcp
```

Connect a host MCP client to this process over stdio. The server exposes exactly 7 lifecycle tools: `sandbox_create`, `sandbox_list`, `sandbox_start`, `sandbox_stop`, `sandbox_pause`, `sandbox_resume`, `sandbox_remove`. Response shape: `{"ok": true|false, "data": ..., "truncated": null}`. For the full envelope and MCP scope rationale, see [Response envelopes](/cli/#response-envelopes).

## nexus reap

Report orphaned host resources left behind by crashed or abandoned sandboxes. With `--apply`, deletes them.

```
nexus reap [--apply]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--apply` | bool | false | Delete the reported orphaned resources |

## nexus disk usage

Report what nexus owns on disk under its state directory (`~/.local/state/nexus`), by category: how much is on disk, how much of it nothing references any more, free space against the builder floor, and what to run next.

```
nexus disk usage [--json]
```

```text
CATEGORY           COUNT  ON DISK   RECLAIMABLE  NOTE
image cache        4      9.8 GiB   4.1 GiB      2 unreferenced image(s); reclaim with nexus image prune
builder templates  3      2.4 GiB   1.6 GiB      2 stale template(s) from previous agent builds; reclaim with nexus image prune
sandbox disks      7      18.2 GiB  3.0 GiB      1 disk(s) with no sandbox record; run nexus reap; shadow disks and .intent markers are reaper-managed; see nexus reap
build caches       2      6.1 GiB   0 B          buildkit cache disks; kept while any build can reuse them
named volumes      1      1.2 GiB   0 B          user data; remove with nexus volume rm
snapshots          0      0 B       0 B
supervisor logs    12     3.4 MiB   0 B          includes builder-supervisors
other              9      1.1 MiB   0 B          store records, sockets, netns, locks, image-cache leftovers
Total: 37.7 GiB   Reclaimable: 8.7 GiB   Free: 11.3 GiB (floor 15.0 GiB)
Free space is below the builder floor; builds will fail until space is reclaimed.
Next: nexus image prune; nexus reap
```

Sizes are **allocated** bytes, not apparent size: every disk image nexus writes is sparse, so `ls -l` overstates what the filesystem has given up. `--json` reports both (`bytes` and `apparent_bytes`).

| Category | What it holds | Reclaimable means |
|---|---|---|
| image cache | Content-addressed images under `images/sha256/` | Entries no sandbox record references and no pinned base ref covers |
| builder templates | `images/nexus-builder-*.ext4`, one per agent binary build | Templates built for an agent binary other than the one installed now |
| sandbox disks | `disks/`: root and workspace disks keyed by sandbox ID, plus shadow disks and `.intent` markers | Disks whose sandbox ID has no store record; shadow disks and markers are reaper-managed and never counted |
| build caches | `caches/`: buildkit cache disks | Never counted; builds reuse them |
| named volumes | `volumes/<name>/` | Never counted; user data (`nexus volume rm`) |
| snapshots | `snapshots/` | Never counted |
| supervisor logs | `supervisors/`, `builder-supervisors/` | Never counted |
| other | Store records, sockets, netns state, locks, image-cache leftovers | Never counted |

Reclaimable is an estimate. `nexus image prune` and `nexus reap` apply their own keep rules at prune time: an image with a lease held, a template a VMM still has open or one modified in the last ten minutes, and any disk of an in-flight sandbox are kept even when this report counts them. The floor is the builder free-space floor (`image.free_space_floor_gib`, default 15 GiB); below it, `sandbox create --file` prunes first and then refuses to build if space is still short.

## nexus recover

Reconcile persisted sandbox records against the live substrate. Use after a host crash or unexpected restart to bring the persisted state back in sync.

```
nexus recover
```

## nexus doctor

Report substrate availability and capability checks: KVM access, network namespace creation, vsock support, buildkitd reachability.

```
nexus doctor
```

## Named secrets <Badge type="danger" text="not built" /> {#named-secrets}

A named secret store lets you bind secrets to sandboxes by name rather than by env var value, so rotation happens in one place.

**What works today:** the `--secret ENV@host[,host...]` flag on `nexus create` reads the value of the named host environment variable and injects it as a per-host credential. The binding is evaluated at creation time — the env var must be set when you run `create`. To attach a GitHub token, pass `--repo owner/name` (scopes the push allowlist) together with `--secret GH_TOKEN@github.com,api.github.com,uploads.github.com`. Sandboxes with no `--repo` receive no GitHub credential (fail-closed).

**Target interface (not built):**

```
nexus secret set <name>     # value read from $<NAME> or stdin — never argv
nexus secret ls             # list stored secret names
nexus secret rm <name>      # remove a stored secret
```

The `--secret` flag on `create` will accept both forms:

```
# existing form — reads env var at creation time
nexus create --secret MY_TOKEN@api.example.com myproject/task-1

# target form — references the store by name; rotation applies to new sandboxes
nexus create --secret my-token@api.example.com myproject/task-1
```

Rotating a named secret (`nexus secret set <name>`) takes effect for any sandbox created after the rotation. Sandboxes already running hold the placeholder minted at their start time; restart them to pick up the new value.
