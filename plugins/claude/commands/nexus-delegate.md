# /nexus:nexus-delegate

Delegate a unit of work from the current repo into a nexus worktree sandbox:
create the sandbox, dispatch a brief to an in-guest agent, poll for completion,
collect the diff, and reclaim.

## When to use it

Run `/nexus:nexus-delegate` when you want to hand a self-contained task to an
agent running inside an isolated VM with its own branch, its own push allowlist,
and brokered credentials.

Preconditions:
- The current repo has a `.nexus/config.yaml` (run `/nexus:nexus-init`
  first if it does not).
- `~/.local/bin/nexus` is installed and the MCP server is connected (run
  `/nexus:nexus-doctor` to confirm).

## What this command does

Loads the `nexus:nexus` skill, opens `references/delegate.md` and `references/delegate-loop.md`, and walks through the delegation loop:

1. **Create** — calls `delegate_worktree_create` with the repo path and a handle
   you provide, booting a worktree-bound sandbox.
2. **Dispatch** — calls `delegate_agent_dispatch` with the brief, confirmed
   delivered when the tool returns.
3. **Poll** — calls `delegate_agent_poll` every 30 seconds. Uses `agent_status`
   to drive the loop: `working` → wait; `blocked` → surfaces `question` for
   answering; `done`/`idle` → checks marker then git. Declares done only when
   `done_via: "marker"` or `done_via: "git"` (not on `agent_status` alone).
   Gives up at 45 minutes.
4. **Collect** — reads the diff from the worktree directly.
5. **Reclaim** — calls `delegate_teardown`, then `herdr worktree remove`.

## Invocation

```
/nexus:nexus-delegate
```

After invoking, the skill asks for:
- The handle for the new sandbox (`project/name`, e.g. `example-app/EX-999`).
- The base branch to branch from.
- The brief for the in-guest agent.

---

Load skill: nexus:nexus — then open `references/delegate.md`.
