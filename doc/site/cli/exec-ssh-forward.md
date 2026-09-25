---
title: "Exec, SSH and Forward"
description: "Reference for exec, attach, cp, log, forward, ssh, and ssh config commands"
---

# Exec, SSH and Forward

> Run commands inside sandboxes and move data in and out.

These verbs operate against a running sandbox. `exec` and `attach` route through the in-guest agent. `cp`, `forward`, `ssh`, and `ssh config` use direct vsock connections. `log` reads the sandbox's supervisor log directly from host state — no guest round-trip.

## nexus exec

Run a command in a sandbox via the in-guest agent.

```
nexus exec <sandbox-ref> [-- <command> [args...]]
```

`<sandbox-ref>` is a sandbox ID, prefix, or `project/name`.

**Auto-TTY** <Badge type="danger" text="not built" /> — the target behavior: when `exec` is invoked without a trailing command, or when stdin is a terminal, `exec` auto-detects the TTY and opens an interactive PTY session (shell or REPL). No flags needed. Today `exec` is non-interactive by default; pass `--pty` explicitly to allocate a PTY.

## shell — built today, retired in target

`shell` is built today but is not part of the target surface. Use `exec` instead: the target `exec` auto-detects TTY and subsumes the interactive-shell use case. See above.

::: info Deliberately excluded
`shell` is on the deliberately-excluded list in the [CLI reference](/cli/). Orchestrators and scripts should migrate to `exec`.
:::

## nexus attach

Reattach to an existing guest session by session ID.

```
nexus attach <sandbox-ref> <session-id>
```

## nexus cp

Copy files between host and guest. Prefix the guest path with `guest:` to identify which side is the guest.

```
nexus cp <sandbox-ref> <src> <dst>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dir` | bool | false | Copy a directory recursively |

Examples:

```
# host → guest
nexus cp myproject/w1 ./local-file.txt guest:/workspace/local-file.txt

# guest → host
nexus cp myproject/w1 guest:/workspace/output.tar ./output.tar
```

## nexus log <Badge type="tip" text="built" />

Print a sandbox's supervisor log (captured stdout+stderr).

```
nexus log <sandbox-ref> [--tail <N>] [--follow]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--tail` | int | 0 | Print only the last N lines (0 = whole log); `-n` is a short alias for the same flag |
| `--follow` | bool | false | Stream appended lines until interrupted (Ctrl-C); `-f` is a short alias; refused under `--json` |

Examples:

```
nexus log myproject/w1
nexus log myproject/w1 --tail 50
nexus log myproject/w1 --follow
```

## nexus forward

Forward a host TCP port to a guest TCP port over vsock. Blocks until interrupted.

```
nexus forward <sandbox-ref> <hostPort>:<guestPort>
```

### Auto port-forward (herdr ≥ 0.9) <Badge type="tip" text="built" />

When running inside a herdr workspace with the nexus plugin version ≥ 0.9, guest TCP ports in the range 1024–11023 are **auto-discovered and forwarded** — no manual `nexus forward` needed.

nexus polls `/proc/net/tcp` inside the sandbox on a short interval. When a new listener appears, nexus opens a supervised forward so the port is reachable at `127.0.0.1:<port>` on the nexus host and, for remote herdr clients, at `127.0.0.1:<port>` on the laptop (same port number both sides — the invariant is preserved to keep OAuth redirect URIs and Vite HMR WebSocket URLs working without reconfiguration).

**Requirement:** herdr ≥ 0.9 on the remote client. nexus declares `min_herdr_version = "0.9.0"` and plugin ABI `"3"` in the herdr-plugin manifest. herdr versions below 0.9 fail the ABI probe at install time with a clear version message rather than silently skipping auto-forward.

Port state is persisted under `~/.config/herdr/portfwd/` on the herdr host and appears in the workspace overlay. When the guest listener closes, the forward is cancelled and the port disappears from the overlay within the reconcile interval.

**Remote-client focus scoping:** on a laptop running `nexus-client`, forwards are scoped to the sandbox bound to the currently focused herdr workspace. Unfocusing a workspace removes its forwards from `127.0.0.1` within one reconcile tick (≤ 5 s); focusing it again restores them. When no workspace is focused, or the focused workspace has no bound sandbox, no forwards are active. Forwards of unfocused sandboxes are never applied. Host-side listeners are unaffected by focus — the scoping is client-side only.

herdr 0.9.0 maintains one session-wide focus shared across all connected clients; the daemon reads focus from the host session's `focus.state` file. Focus-scoped forwarding is accurate only when `nexus-client` is the **sole interactive client** on the session. When both a host TUI and a remote client are attached, the host session's last workspace-focus event is the one the daemon reads.

## nexus ssh

Dial a sandbox's sshd over vsock. With `--stdio`, behaves as an SSH `ProxyCommand`, allowing standard `ssh` tooling to reach the sandbox.

```
nexus ssh [--stdio] <sandbox-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--stdio` | bool | false | Pipe mode for use as an SSH `ProxyCommand` |

## nexus ssh config <Badge type="warning" text="partial" />

Print a `Host` block for `~/.ssh/config` that uses `nexus ssh --stdio` as a `ProxyCommand`.

```
nexus ssh config <sandbox-ref>
```

Today's spelling is `nexus config-ssh <sandbox-ref>` — the target moves this to a subverb of `ssh`.
