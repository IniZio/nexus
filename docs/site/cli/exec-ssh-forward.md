---
title: "Exec, SSH and Forward"
description: "Reference for exec, attach, cp, log, forward, ssh, and ssh config commands"
---

# Exec, SSH and Forward

> Run commands inside sandboxes and move data in and out.

These verbs operate against a running sandbox. `exec` and `attach` route through the in-guest agent. `cp`, `forward`, `ssh`, and `ssh config` use direct vsock connections. `log` reads the sandbox's supervisor log directly from host state — no guest round-trip.

## nexus3 exec

Run a command in a sandbox via the in-guest agent.

```
nexus3 exec <sandbox-ref> [-- <command> [args...]]
```

`<sandbox-ref>` is a sandbox ID, prefix, or `project/name`.

**Auto-TTY** <Badge type="danger" text="not built" /> — the target behavior: when `exec` is invoked without a trailing command, or when stdin is a terminal, `exec` auto-detects the TTY and opens an interactive PTY session (shell or REPL). No flags needed. Today `exec` is non-interactive by default; pass `--pty` explicitly to allocate a PTY.

## shell — built today, retired in target

`shell` is built today but is not part of the target surface. Use `exec` instead: the target `exec` auto-detects TTY and subsumes the interactive-shell use case. See above.

::: info Deliberately excluded
`shell` is on the deliberately-excluded list in the [CLI reference](/cli/). Orchestrators and scripts should migrate to `exec`.
:::

## nexus3 attach

Reattach to an existing guest session by session ID.

```
nexus3 attach <sandbox-ref> <session-id>
```

## nexus3 cp

Copy files between host and guest. Prefix the guest path with `guest:` to identify which side is the guest.

```
nexus3 cp <sandbox-ref> <src> <dst>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dir` | bool | false | Copy a directory recursively |

Examples:

```
# host → guest
nexus3 cp myproject/w1 ./local-file.txt guest:/workspace/local-file.txt

# guest → host
nexus3 cp myproject/w1 guest:/workspace/output.tar ./output.tar
```

## nexus3 log <Badge type="tip" text="built" />

Print a sandbox's supervisor log (captured stdout+stderr).

```
nexus3 log <sandbox-ref> [--tail <N>] [--follow]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--tail` | int | 0 | Print only the last N lines (0 = whole log); `-n` is a short alias for the same flag |
| `--follow` | bool | false | Stream appended lines until interrupted (Ctrl-C); `-f` is a short alias; refused under `--json` |

Examples:

```
nexus3 log myproject/w1
nexus3 log myproject/w1 --tail 50
nexus3 log myproject/w1 --follow
```

## nexus3 forward

Forward a host TCP port to a guest TCP port over vsock. Blocks until interrupted.

```
nexus3 forward <sandbox-ref> <hostPort>:<guestPort>
```

## nexus3 ssh

Dial a sandbox's sshd over vsock. With `--stdio`, behaves as an SSH `ProxyCommand`, allowing standard `ssh` tooling to reach the sandbox.

```
nexus3 ssh [--stdio] <sandbox-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--stdio` | bool | false | Pipe mode for use as an SSH `ProxyCommand` |

## nexus3 ssh config <Badge type="warning" text="partial" />

Print a `Host` block for `~/.ssh/config` that uses `nexus3 ssh --stdio` as a `ProxyCommand`.

```
nexus3 ssh config <sandbox-ref>
```

Today's spelling is `nexus3 config-ssh <sandbox-ref>` — the target moves this to a subverb of `ssh`.
