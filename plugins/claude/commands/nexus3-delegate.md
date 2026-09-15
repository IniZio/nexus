# /nexus3:nexus3-delegate

Delegate a unit of work from the current repo into a nexus3 worktree sandbox:
create the sandbox, dispatch a brief to an in-guest agent, poll for completion,
collect the diff, and reclaim.

## When to use it

Run `/nexus3:nexus3-delegate` when you want to hand a self-contained task to an
agent running inside an isolated VM with its own branch, its own push allowlist,
and brokered credentials.

Preconditions:
- The current repo has a `nexus3.yaml` at its root (run `/nexus3:nexus3-init`
  first if it does not).
- `~/.local/bin/nexus3` is installed and the MCP server is connected (run
  `/nexus3:nexus3-doctor` to confirm).

## What this command does

Loads the `nexus3:nexus3` skill, opens `references/delegate.md` and `references/delegate-loop.md`, and walks through the delegation loop:

1. **Create** — calls `delegate_worktree_create` with the repo path and a handle
   you provide, booting a worktree-bound sandbox.
2. **Dispatch** — calls `delegate_agent_dispatch` with the brief, confirmed
   delivered when the tool returns.
3. **Poll** — calls `delegate_agent_poll` every 30 seconds until `git_log` is
   non-empty and `git_status` is clean, or 45 minutes elapse.
4. **Collect** — reads the diff from the worktree directly.
5. **Reclaim** — calls `delegate_teardown`, then `herdr worktree remove`.

## Invocation

```
/nexus3:nexus3-delegate
```

After invoking, the skill asks for:
- The handle for the new sandbox (`project/name`, e.g. `example-app/EX-999`).
- The base branch to branch from.
- The brief for the in-guest agent.

---

Load skill: nexus3:nexus3 — then open `references/delegate.md`.
