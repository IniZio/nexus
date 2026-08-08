# 09 — Surfaces

*Purpose: what nexus3 exposes to clients — CLI + MCP only, the `nexus3 attach` pane path, SSH-endpoint-ness, the herdr plugin, agent selection, and `nexus3 cp`.*

## nexus3 owns CLI + MCP only

nexus3 owns exactly two surfaces: **CLI and MCP** (ticket 11). The TUI/multiplexer is offloaded to **herdr**; there is **no nexus3-owned SSH gateway**, **no bubbletea/Bubbles/x-vt/Wish**.

Crucially, **there is no surface-to-core RPC seam**: the core is a **library** the surfaces link (doc 02). This is what makes no-central-daemon true **by construction** — no long-lived surface process mediates access to sandboxes.

## The CLI is the machine contract

The **CLI is the machine contract** (copied from crabbox): a **versioned `--json` control plane** plus a **raw-stdio attach** path. Everything a client or plugin needs is expressible as CLI invocations. The `--json` contract must carry (ticket 19 obligations):

- `pause` / `resume` as user commands;
- the **stop-reason qualifier** (`clean` / `memory_lost`) on the sandbox object;
- and it must **never report a `removed` state** (removal deletes the record).

The CLI also carries `pause` / `resume` / `cp` (below).

## `nexus3 attach` — retained and load-bearing

`nexus3 attach` is **retained and load-bearing** (ticket 11; confirmed ticket 32): a herdr **plugin cannot own the pane's PTY** — herdr always owns the pane PTY and forks a child, so durability must live at the far end of the argv herdr hands the plugin. `nexus3 attach` is the primary **durable** interactive path (the agent owns the session; doc 04), distinct from SSH.

> The old map framing leaned on a herdr "do not merge panes/PTYs" non-goal quote. **That quote does not exist** (map Correction) — the word "merge" appears nowhere in herdr's repo or docs. The conclusion survives on **stronger** evidence: herdr's `PaneDied` fires on `child.wait()` and `handle_pane_died` *removes the pane* (dropping the workspace if it was the last); the exit status is logged, never branched on; there is no `remain_on_exit`/`auto_restart`/`keep_alive`; and plugins are structurally forced to hand herdr an argv. So a plugin cannot supply a durable pane substrate — `nexus3 attach` must.

## A Sandbox IS an SSH endpoint

A Sandbox **is an SSH endpoint** (ticket 11 amended; research 31): the guest runs upstream OpenSSH bound to **`AF_VSOCK`**, reached via **`nexus3 ssh --stdio`** as an SSH `ProxyCommand`, with **`nexus3 config-ssh`** writing the user's SSH config. This serves **editors (VS Code Remote-SSH), sshfs, and git-over-ssh** — the tools herdr does not supply.

SSH is a **TOOL path, not the session path** (ticket 32): the discriminating constraint is that a mid-task agent process must stay alive and reattachable in each forked child, which rules out sshd-parented sessions. So:

- **The agent owns durable sessions**; SSH owns tools. **No nexus3 command depends on SSH** — `nexus3 exec` and `nexus3 attach` both go over the agent.
- **SSH sessions are connection-scoped and die on restore** (an sshd-parented shell gets `SIGHUP`; verified ticket 37). This is documented plainly, never hedged.
- vsock is not a network listener, so §6's no-network-listener clause survives; the dropped SSH *gateway* stays dropped. Only the "gains no third protocol" clause was amended.

The guest therefore carries an sshd it exercises for tools only. (Whether that is the right long-term posture vs clawk's zero-sshd guest is noted open in doc 10 / the map, but is not a live v1 question.)

## herdr plugin

The TUI is offloaded to **herdr** via an in-repo, out-of-tree `plugins/herdr/` (ticket 26):

- a `herdr-plugin.toml` manifest + `build.sh` + two pane scripts calling a hidden **`nexus3 __herdr-plugin`** shim, path-pinned at install time. No new language, no daemon, no new public CLI surface.
- Entrypoints are keyed by **argv, not placement** (correcting an inherited premise: `plugin.pane.open` can override manifest placement at runtime).
- Workspace selected via `--env NEXUS3_WORKSPACE`.
- `pane.report_agent` is driven by **the pane process itself as broker** (herdr injects `HERDR_BIN_PATH`/`HERDR_PANE_ID`), reporting only host-observable state, with **no `idle` state** — so no guest ever sees a herdr socket path and the agent proto is not widened.
- **No `[[startup]]`/`[[events]]` hooks** — an event stopping a workspace on pane close would be exactly the automatic lifecycle transition ticket 20 forbids.
- herdr is **v0.8.0, Apache-2.0** (map Correction — *not* the v0.7.5/AGPL that ticket 26's `min_herdr_version` recorded). A version ceiling is a runtime warning in `doctor`, never a refusal.
- **Security:** herdr's socket has no caller authorization, so a guest must never reach it (research 24). The plugin runs host-side only.

## Agent selection — no inference, ever

Agent selection copies OpenShell's **`attach` / `detach`**, with **no inference ever** (user ruling γ; ticket 11): selection is always explicit. `--agent` is **sugar for attach** against a host-side **agent catalog** (which also defines ticket 20 §8's "known at boot"). The post-creation-mutability half of the bar is preserved; only the zero-friction/inference half is traded away (no shipped product delivers both at once — OpenShell retired inference exactly when it gained mutability).

## MCP

MCP = **`modelcontextprotocol/go-sdk`**, **stdio only, no network listener** (ticket 11). Guest-side (inbound) MCP is **out of scope** — it would hand ticket 20's adversary an RPC channel to the policy core and need a second capability model. Remote MCP (any HTTP/SSE listener) is likewise declined; remote callers go via herdr's remote attach or ssh-to-host plus the CLI.

## `nexus3 cp` — the sole nexus3-owned escape verb

`nexus3 cp` is the **sole nexus3-owned escape verb** (ticket 22): first-class, bidirectional, `docker cp`/`kubectl cp`-shaped (`nexus3 cp <sandbox>:/src ./dst` and reverse).

- Transport is the native agent **`Copy`** capability (apple-containerization's split-plane shape) over **agent/vsock, never SSH** (doc 04). The agent archives directories itself; no guest `tar`.
- `cp` is **host-initiated with no inbound channel** — the guest cannot invoke it, so the adversary gains no host-mutation vector. Only "who may run nexus3 here" authz applies (no credential, no egress; ticket 20's vocabulary does not apply to a local vsock transfer).
- The **push-in** direction is an explicit operator-initiated post-seed host→guest contribution (amends ticket 20 §3, whose no-live-share intent stands).
- `cp` produces **plain client-side files, never a nexus3 durable entity**. Extracting "the good branch" of a fan-out is `cp` from the chosen child.
- **Guest-outbound egress (`git push`, `curl`, `npm publish`) gets NO nexus3 verb** — it is ordinary agent egress wholly under doc 08. The SSH endpoint is a third de-facto escape path (`scp`/`rsync`/`sshfs` by hand), supported but not a nexus3 verb.

---

*Sources: tickets 11 (CLI+MCP only, no surface-to-core RPC, CLI-as-contract, attach load-bearing, agent selection γ, MCP stdio-only), 32 (SSH is tools not sessions, connection-scoped), 26 (herdr plugin shape), 24 (herdr socket security), 22 (`nexus3 cp`), 34 (crabbox provider alongside), 19 (`--json` obligations). Map Corrections: herdr v0.8.0/Apache-2.0, phantom "merge" quote, SSH-endpoint amendment.*
