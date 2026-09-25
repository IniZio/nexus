# Delegating work into a worktree sandbox

Create a worktree-bound sandbox, dispatch a brief to an in-guest agent, poll
for completion, collect the result, and reclaim. Also covers tearing down a
finished sandbox, counting running sandboxes and their RAM cost, and diagnosing
a stalled delegate workflow.

Four MCP tools cover the core of the workflow. CLI fills the two gaps where no
tool exists. Every step below names which is which.

## Tool map

| Step | Method | Tool / command |
|---|---|---|
| Create worktree-bound sandbox | **MCP tool** | `delegate_worktree_create` |
| Dispatch brief to in-guest agent | **MCP tool** | `delegate_agent_dispatch` |
| Poll for completion | **MCP tool** | `delegate_agent_poll` |
| Teardown sandbox | **MCP tool** | `delegate_teardown` |
| Remove host git worktree | **CLI only** | `herdr worktree remove --workspace <ws-id>` |
| Read diff directly | **CLI only** | `git -C <worktree-path> log --oneline HEAD` |

Use the MCP tools; fall back to CLI equivalents only when the MCP server is
unavailable (see `delegate-loop.md` for CLI spellings of every step).

## Reference files

| Topic | File |
|---|---|
| Full step-by-step loop with MCP and CLI spellings | `delegate-loop.md` |
| Brief authoring — standing orders (prepended to every dispatched brief), what to include, what to require in the report | `delegate-briefs.md` |

## Push rule

A sandbox may push **exactly one ref**: the branch checked out in its bound
worktree at creation time. `delegate_worktree_create` derives the allowlist from
the worktree; the `allowed_branches` parameter **must not be set** (the call is
rejected with an error). Create the worktree on the intended push branch before
creating the sandbox. A detached-HEAD worktree at create time produces a sandbox
that cannot push anything.

## Egress reality

Worktree sandboxes run with `open_egress: true`. The `egress.policy.allow`
allowlist from `.nexus/config.yaml` is stored but NOT enforced as a gate for these
sandboxes. What IS enforced: secret brokering (the guest holds a 64-hex
placeholder, not the real credential) and per-path policy on secret hosts
(cross-repo API paths → 403, GraphQL → 403). Defer to `egress.md`
for policy authoring.

## Completion heuristic

`delegate_agent_poll` returns `{done_via, marker_content, git_log, git_status,
branch_name, agent_status, state_change_seq, settled, question,
agent_state_reason}`. Completion is decided by marker or git — `agent_status`
is context, not proof.

Work is done when one of these holds:

1. **`done_via: "marker"`** — agent wrote `/run/nexus/delegate-done`; content
   in `marker_content`. Preferred; declare done immediately.
2. **`done_via: "git"`** — marker absent; `git_log` non-empty AND `git_status`
   empty.

`agent_status: "done"` supports the case but never decides it alone.

Use `agent_status` to drive the poll loop (see `delegate-loop.md § 3` for the
full table). Short version: `working` → wait; `blocked` → read `question` and
answer; `unknown` → fall back to screen/movement heuristics.

**Do not infer done from `delegate_agent_dispatch` returning success.** That
tool blocks only until the brief is confirmed delivered — not until the work
finishes.

Poll every 30 seconds. Give up after 45 minutes (90 polls) and surface the last
`git_log` and `git_status` as evidence. On a give-up, read the diff directly
(`git -C <worktree> diff <base>...HEAD`) — pane output may lag or be truncated
by the Claude UI; the diff is never truncated. See `delegate-loop.md` for the
full polling posture.

**No-op case.** If the agent determines no change is needed and commits nothing,
`git_log` stays empty and the heuristic never fires — the timeout at 45 minutes
is the first signal, indistinguishable from a stuck agent. The fix is in the
brief: require a commit in every outcome, including "no change needed". A
REPORT.md commit saying why no change was made satisfies the heuristic and
surfaces the conclusion. See `delegate-briefs.md`.

## Teardown order and RAM cost

`delegate_teardown` (and its CLI equivalent `~/.local/bin/nexus rm <handle>`)
removes the sandbox and its guest disk — **but NOT the host git worktree**. Always
follow with `herdr worktree remove --workspace <ws-id>` to reclaim the worktree
and the herdr space.

Each running sandbox holds memfd-backed guest RAM that is resident and
unswappable. Sandboxes left running after their work has landed are the ordinary
cause of host memory pressure. Reclaim promptly; use `~/.local/bin/nexus stop
<handle>` if the work is blocked and you intend to return.
