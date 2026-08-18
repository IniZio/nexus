---
title: "CLI Reference"
description: "nexus3 command surface: invocation, global flags, and verb index"
---

# CLI Reference

> One binary, one MCP server — both thin adapters over a single service layer.

nexus3 exposes one binary (`nexus3`) and one MCP server (`nexus3 mcp`). Every operation reachable from the CLI is backed by `internal/core/service`; CLI and MCP share the same service layer, so capabilities are identical.

::: warning Help <Badge type="warning" text="partial" />
The target is help on every verb and group, on stdout, exiting zero.
Today: running `nexus3` with no arguments lists all commands with one-line descriptions, and verbs that parse flags with Go's `flag` package (`exec`, `attach`, `ssh`, …) respond to `--help`. Three things fall short:

- `nexus3 --help`, `nexus3 help` and `nexus3 -h` are all rejected as unknown (they still print the command list, but exit non-zero).
- Commands with hand-rolled parsing have no help: the groups `sandbox`, `snapshot`, `image`, `auth` (at any depth — including `create --help`, `ps --help`), and the leaf verbs `fork`, `restore`, `forward`. `nexus3 mcp --help` starts the stdio server instead of printing anything.
- The command list goes to **stderr** and exits **2**, so `nexus3 | less` shows nothing.

For per-flag detail, generate the extractor inventory: `scripts/docs/extract-surface.sh`.
:::

## Invocation

```
nexus3 [global-flags] <verb> [verb-flags] [args...]
```

Global flags precede the verb:

| Flag | Type | Default | Description |
|---|---|---|---|
| `--json` | bool | false | Emit machine-readable output (see [Response envelopes](#response-envelopes)) |

## Verb index

| Verb | Reference | Purpose |
|---|---|---|
| `create/ps/rm/start/stop/pause/resume` <Badge type="warning" text="partial" /> | [Lifecycle commands](/cli/sandbox-commands) | Sandbox lifecycle management (built today as `sandbox <verb>`) |
| `run` | [Lifecycle commands](/cli/sandbox-commands) | One-shot ephemeral exec; sandbox removed on exit |
| `exec` | [Exec, SSH and forward](/cli/exec-ssh-forward) | Run a command inside a sandbox; auto-detects TTY for interactive use |
| `attach` | [Exec, SSH and forward](/cli/exec-ssh-forward) | Reattach to an existing guest session |
| `cp` | [Exec, SSH and forward](/cli/exec-ssh-forward) | Copy files between host and guest |
| `forward` | [Exec, SSH and forward](/cli/exec-ssh-forward) | Forward a host TCP port to a guest port over vsock |
| `ssh` | [Exec, SSH and forward](/cli/exec-ssh-forward) | Dial a sandbox's sshd over vsock |
| `ssh config` <Badge type="warning" text="partial" /> | [Exec, SSH and forward](/cli/exec-ssh-forward) | Print an SSH `ProxyCommand` configuration snippet (built today as `config-ssh`) |
| `snapshot create/list/rm` | [Snapshot, fork and restore](/cli/snapshot-fork-restore) | Manage retained snapshots |
| `fork` | [Snapshot, fork and restore](/cli/snapshot-fork-restore) | Fork a running sandbox into a new copy-on-write child |
| `restore` | [Snapshot, fork and restore](/cli/snapshot-fork-restore) | Restore a retained snapshot into a new running sandbox |
| `image build/ls/prune` | [Image commands](/cli/image-commands) | Build and manage guest images |
| `auth` | [Auth, MCP and reap](/cli/auth-mcp-reap) | Authenticate your coding agent (Claude Code, Codex, opencode, …) |
| `secret` <Badge type="danger" text="not built" /> | [Auth, MCP and reap](/cli/auth-mcp-reap) | Named secret store: `set`, `ls`, `rm` |
| `mcp` | [Auth, MCP and reap](/cli/auth-mcp-reap) | Run an MCP server over stdio |
| `reap` | [Auth, MCP and reap](/cli/auth-mcp-reap) | Report (and optionally delete) orphaned host resources |
| `recover` | [Auth, MCP and reap](/cli/auth-mcp-reap) | Reconcile persisted sandbox records against the live substrate |
| `doctor` | [Auth, MCP and reap](/cli/auth-mcp-reap) | Report substrate availability and capability checks |
| `version` | — | Print version and build information |
| `--context` config <Badge type="warning" text="partial" /> | [Configuration](/cli/configuration) | Self-contained Dockerfile-based image build (built today as `--file`) |

::: warning Command grouping <Badge type="warning" text="partial" />
Today's implementation groups the lifecycle verbs under a `sandbox` noun: `sandbox create`, `sandbox list`, `sandbox start`, and so on. The target spells them flat: `create`, `ps`, `rm`, `start`, `stop`, `pause`, `resume`. The [Lifecycle commands](/cli/sandbox-commands) page carries the full target-to-implementation mapping.
:::

## Response envelopes

There are two envelope shapes, deliberately kept separate.

**CLI `--json`** emits newline-delimited JSON, one object per event:

```json
{ "schema_version": 1, "kind": "sandbox.created", "data": {  } }
{ "schema_version": 1, "kind": "error", "error": { "code": "sandbox_not_found", "message": "" } }
```

`kind` identifies the event (`sandbox.created`, `exec.done`, `reap.report`). Error `code` values are stable within a `schema_version`; changes inside a version are additive only.

**MCP** uses a uniform shape across all tools:

```json
{ "ok": true, "data": {  }, "truncated": null }
```

`truncated` is non-null only when a response was cut; currently wired for `sandbox_list` only (64 KiB cap). The two shapes are kept separate because shell and CI pipelines need `kind` routing and forward-compatible versioning, while MCP tool-call handlers need a minimal success/failure shape with truncation signalling.

**MCP scope is deliberately lifecycle-only.** The MCP server exposes exactly 7 tools: `sandbox_create`, `sandbox_list`, `sandbox_start`, `sandbox_stop`, `sandbox_pause`, `sandbox_resume`, and `sandbox_remove`. Snapshot, fork, restore, exec, forward, image build, and auth operations are CLI-only. The `sandbox_` prefix is kept across all tool names because MCP tool names live in a single flat namespace shared with every other MCP server an orchestrator may load; namespacing by noun avoids collisions without requiring a per-server prefix convention.

## Target surface gaps

These capabilities have no implementation today. An unbadged entry in the verb index means built-and-matching.

| Surface | State | What the target is |
|---|---|---|
| `logs` | <Badge type="danger" text="not built" /> | Read a sandbox's captured output without opening a shell. |
| `metrics` | <Badge type="danger" text="not built" /> | Report CPU and memory as `effective / max`. The data exists — auto-resize computes it — but nothing surfaces it. |
| Volumes | <Badge type="danger" text="not built" /> | Named storage that persists across sandboxes and can be shared between them. Mounts and shadow disks are neither named nor shareable. |
| Label mutation | <Badge type="danger" text="not built" /> | Add and remove labels on an existing sandbox. |
| Fleet lifecycle selectors | <Badge type="danger" text="not built" /> | `--label` as a selector for `stop` and `rm`, not only for `list`. |
| Full help | <Badge type="warning" text="partial" /> | Help on every verb and group, on stdout, exit zero. See the note at the top of this page for exactly what is missing. |

Eleven things are deliberately excluded from the target:

- **Fleet `exec`** — retracted. Fan-out is a host-side loop.
- **A `pr` verb** — pushing pull requests stays host-side so no GitHub credential ever needs to reach a guest.
- **`harvest`** — superseded by live virtiofs mounts; the verb exists in source but is slated for removal once the mounts path ships.
- **`up`** — removed from the target. Bringing up N sandboxes is a host-side loop over `create`.
- **`fork --count`/`restore --count`** — built today, removed from the target. Each invocation creates one child; fan-out is the orchestrator looping the verb.
- **`create --workspace <host-path>`** — working-tree capture superseded by live virtiofs mounts (`-v`/`--volume`). Use `--volume` to mount a host directory into the sandbox instead.
- **`create --capture-max <size>`** — capture size limit; removed alongside `--workspace`.
- **`shell`** — built today, retired in the target. `exec` subsumes it: without a trailing command, or when stdin is a terminal, `exec` opens an interactive PTY session automatically.
- **Reserved-label convention and git-driven branch naming** — nexus3 is git-unaware. `--label` carries arbitrary key-value metadata only; no label key has special semantics, and branch names are chosen by the user or orchestrator, not by nexus3.
- **Bundle-export and host-side push by nexus3** — removed. In-guest `git push` via the MITM GitHub credential path (placeholder-swap + per-repo allowlist) is the supported flow; the host user pushes from the host with their own tools.
- **Preview-release publisher** — building and distributing release artifacts is outside the nexus3 target surface.
