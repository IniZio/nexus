# Agent in a sandbox

## Projecting the host agent setup into a sandbox

An in-guest agent starts with **none** of the operator's configuration — no `CLAUDE.md`, no skills, no commands. It works, but blind to every operating rule the host agent follows. Project the config in explicitly.

### Never mount `~/.claude` wholesale

`~/.claude` contains `.credentials.json`. Mounting the directory carries real credential material into the guest and breaks the zero-cred-in-guest invariant (AC-7). The guest is supposed to hold only the placeholder token that the host-side MITM proxy swaps per request.

Project a **curated subset** instead:

```sh
nexus3 create <project>/<name> --image <digest> --agent claude-code \
  --mount /path/to/checkout:/work \
  --mount ~/.claude/skills:/root/.claude/skills:ro \
  --mount ~/.claude/commands:/root/.claude/commands:ro \
  ...

# --mount is directory-only, so single files go via cp:
nexus3 cp <project>/<name> ~/.claude/CLAUDE.md guest:/root/.claude/CLAUDE.md
```

Mount these **read-only**. The guest has no reason to write to the operator's configuration, and `:ro` makes that structural rather than a convention.

| Path | Project? | Why |
|------|----------|-----|
| `CLAUDE.md` | yes (~4K) | the operating rules the agent should follow |
| `skills/` | yes (~724K) | skills the agent needs |
| `commands/` | yes (~12K) | small, harmless |
| `plugins/` | **no** (~424M) | far too large; ships binaries |
| `.credentials.json` | **never** | real credential material |
| `sessions/`, `projects/` | no | host history, no value in-guest |

After creating a sandbox this way, confirm the boundary held:

```sh
nexus3 exec <ref> -- /usr/bin/bash -lc 'ls /root/.claude/.credentials.json'
# MUST report: No such file or directory
```

Treat that check as mandatory, not optional. It is the one assertion that distinguishes a projected config from a leaked credential.

---

## Workspace trust

A fresh guest has no `~/.claude.json`, so `claude` blocks on the workspace-trust prompt before doing any work. Seed it:

```json
{
  "projects": { "/work": { "hasTrustDialogAccepted": true } },
  "hasCompletedOnboarding": true
}
```

Running `claude --permission-mode auto` as root (the guest default user) additionally requires `IS_SANDBOX=1`.

---

## Known limitation: new tabs in a nexus3 herdr space open a HOST shell

Opening a new tab in a nexus3-created herdr space launches a shell on the **host**, not in the sandbox — confusing, since the workspace represents a sandbox.

This cannot be fixed in nexus3. The herdr plugin ABI has no per-workspace default entrypoint: `entrypoint` exists only on `PluginPaneOpenParams` (a per-open argument), while `WorkspaceCreateParams` and `TabCreateParams` carry `{cwd, env, focus, label}` only. Closing it needs an upstream herdr feature.

To get another **guest** shell, open a plugin pane rather than a plain tab:

```sh
nexus3 herdr space-open-pane <sandbox-ref>
```

---

## Starting an agent inside a sandbox

One command does the whole thing:

```
nexus3 herdr agent [--autonomous] <sandbox-ref> "<brief>"
```

It starts the sandbox, creates or reuses its herdr space, opens the guest pane, launches claude, waits for the prompt, and types the brief. It is also a herdr action (**nexus3: launch Claude agent in sandbox**), which prompts for the ref, the brief, and whether to run autonomously.

The sandbox must have source mounted. `herdr agent` refuses one that does not, because an agent with nothing to work on looks identical to a healthy agent.

`--autonomous` launches the agent in auto permission mode (`--permission-mode auto`) so it acts without asking approval per tool call. It is off by default and always asked, never assumed.

---

## Driving an agent by hand

`herdr agent start` **cannot** drive an in-guest agent. It validates the *host-side* pane foreground process, which for a guest pane is the `nexus3 exec --pty` wrapper, so it refuses with `agent_pane_busy`. This is a boundary, not a bug. Use the pane API: `send-text`, `send-keys`, `read`, `wait-output`.

Three traps, each of which produces something that looks like a working agent:

- **`herdr pane run` is wrong for a TUI.** It sends the text and Enter in one call. Against a shell that is fine; against claude the text lands in the input box and *sits there unsubmitted*. Send the text, pause, then send `Enter` separately.
- **There is no single "agent is ready" token.** The footer differs by permission mode — `? for shortcuts` in the default mode, `auto mode on` under `--permission-mode auto`. Match the one for the mode you launched. Do **not** match the prompt glyph `❯`: it is also every wizard's selector glyph, so it reports ready mid-dialog.
- **`send-keys` key names**: `ctrl+c` and `C-c` work; `ctrl-c` and `^C` are rejected as invalid.

---

## First-run wizards

A guest claude walks up to **four** wizards before reaching its prompt. nexus3 seeds past all of them, but if you build a guest by hand, these are the keys:

| wizard | file | key |
|---|---|---|
| theme picker | `~/.claude.json` | `theme` |
| login method | `~/.claude.json` | `hasCompletedOnboarding` |
| folder trust | `~/.claude.json` | `projects[<dir>].hasTrustDialogAccepted` |
The login wizard is the deceptive one: it appears when onboarding is incomplete *even though the credential is present and correct*. Reaching "Select login method" is not evidence of a credential problem. Check `bash -lc 'env | grep CLAUDE_CODE_OAUTH_TOKEN'` in the guest before concluding anything about credentials.

---

## Creating a pull request from inside a sandbox

`gh pr create` uses a GraphQL mutation and returns 403 — the perimeter denies GraphQL by default. In-guest agents must not attempt it. Use the REST form instead:

```sh
gh api -X POST repos/{owner}/{repo}/pulls \
  -f title="<title>" -f head="<branch>" -f base="<base-branch>" -f body="<body>"
```

`gh api` expands `{owner}` and `{repo}` from the local git remote automatically. A successful call returns HTTP 201.

---

## What the guest image has

`node` is present (claude is a node program). `python3`, `python` and `jq` are **absent** — write guest-side JSON manipulation in node.
