# Delegation loop — step by step

Nothing below hardcodes a path or handle. Read workspace IDs and worktree paths
from the JSON responses of each step rather than predicting them.

## 1. Create the worktree-bound sandbox

**MCP tool — `delegate_worktree_create`**

```json
{
  "repo_path": "/abs/path/to/repo",
  "branch": "feature/my-work",
  "base": "main"
}
```

Fields:
- `repo_path` — required; absolute host path to an existing checkout already open as a herdr workspace.
- `branch` — required; name of the new branch to create.
- `base` — optional; git ref the new branch starts from (omit to use herdr's default).
- `image_ref`, `memory_mib`, `vcpus`, `allowed_branches` — **rejected with an error**; the image is derived from `<repo>/.nexus/config.yaml` (or `.nexus/Containerfile`) exactly as for any herdr worktree sandbox.

**What the tool does** (five steps, all driven by the nexus MCP plugin process):

1. Resolves the herdr binary (`HERDR_BIN_PATH` env, else `herdr` on PATH).
2. Runs `herdr workspace list` to find the workspace whose `checkout_path` is `repo_path` (fallback: `HERDR_WORKSPACE_ID` env).
3. Runs `herdr worktree create --workspace <parent> --branch <branch> [--base <base>] --no-focus` — herdr creates the linked git worktree (under `~/.herdr/worktrees/<repo>/<branch>`) and its workspace.
4. Runs `nexus herdr worktree-sandbox <newWorkspaceID>` — the same verb the herdr `worktree.created` hook runs. It derives the handle `<repo>/<branch-slug>`, resolves the image from `.nexus/config.yaml`, converts `egress.policy`/`egress.secrets` into policy flags, mounts the checkout at `/workspace` plus the main repo's `.git` and `.groundwork` dirs, attaches named volumes (`<slug>-docker` only with a Containerfile; `<slug>-agentcfg`, `<slug>-gocache`, `<slug>-gopath` always), writes the herdr↔sandbox binding, and opens the guest pane.
5. Runs `nexus herdr list` and returns the result.

**Output fields:** `workspace_id`, `worktree_path`, `branch`, `handle` (pass this to `delegate_agent_dispatch` / `delegate_teardown` as `ref`), `sandbox_id`, `output`.

**Herdr requirement:**

| herdr available? | Result |
|-----------------|--------|
| Yes, repo open as herdr workspace | git worktree + herdr workspace + sandbox + binding + guest pane all created |
| No (`herdr` not found) | nothing created; error at step 1: `herdr not found …` |
| Yes, but repo not open as herdr workspace (and `HERDR_WORKSPACE_ID` unset) | nothing created; error at step 2: `repo … is not open as a herdr workspace` |

`HERDR_ENV=1` is NOT required for the tool — it is the herdr-skill gate for agents controlling panes. The tool works from any process that can reach the herdr CLI.

**CLI equivalent** — the tool automates exactly this by-hand sequence:

```bash
herdr worktree create --workspace <main-ws-id> --branch <branch> --base <base-ref> --no-focus
# read new workspace ID from response
~/.local/bin/nexus herdr worktree-sandbox <new-workspace-id>
```

Verify the worktree's current branch is the intended push target before
creating the sandbox — detached HEAD at create time yields a sandbox that
cannot push anything.

### Builder failure modes

The build step pulls a base image and runs the repo's `.nexus/Containerfile`
inside a builder VM. Two failure patterns to know:

**Dirty-cache death loop.** If a build is killed mid-flight (by timeout, OOM,
or Ctrl-C), it marks the buildkit cache disk dirty. The next attempt wipes and
recreates the cache from scratch — costing a full cold pull — then often fails
again for the same reason. Do NOT kill a running build to retry: let it finish
or fail on its own, then investigate. A wipe line in the log (`cachedisk: slot
0 left dirty; wiping`) is always preceded by an unclean prior death, never by
a healthy run.

**`read frame: EOF` immediately after `buildkitd ready`.** The builder VM's
agent started, buildkitd came up, then the host lost the connection before any
layer was pulled. Common causes: the builder VM's network has no egress to the
image registry, host memory or disk exhaustion killed the builder VM, or a
protocol mismatch between the host binary and the cached builder image. Check
`df -h /` (the build preflight needs ≥15 GiB free) and free RAM before
concluding it is a network issue.

## 2. Dispatch the brief

**MCP tool — `delegate_agent_dispatch`**

```json
{
  "ref": "project/name",
  "brief": "..."
}
```

Returns `{ok, data:{delivered, output}}` once the brief is **confirmed
delivered** to the agent's input — not when the work is done.
`delivered: true` means herdr confirmed the state changed after the brief was
sent (with screen-classifier fallback). `delivered: false` means the brief was
NOT submitted; the VM is up but the agent has not read it.

**CLI equivalent:**

```bash
~/.local/bin/nexus herdr agent --autonomous --no-focus <handle> "<brief>"
```

See `delegate-briefs.md` for what a brief must contain.

## 3. Poll for completion

**MCP tool — `delegate_agent_poll`**

```json
{ "ref": "project/name" }
```

Optional arg:
- `wait_ms` — omit or `0` for an instant snapshot; positive value blocks via
  `herdr agent wait` until the agent reaches `idle`, `done`, or `blocked`, or
  the timeout elapses.

Returns `{ok, data:{done_via, marker_content, git_log, git_status,
branch_name, agent_status, state_change_seq, settled, question,
agent_state_reason}}`.

**New fields:**

| Field | Values | Meaning |
|---|---|---|
| `agent_status` | `idle\|working\|blocked\|done\|unknown` | herdr 0.9.0 native detection of the guest pane state |
| `state_change_seq` | integer | increments each time herdr detects a state transition; re-poll on change to avoid reading a stale snapshot |
| `settled` | bool | `false` if the status kept changing through the re-poll budget (up to 3 re-polls, ~1.5 s settle); treat an unsettled `done` or `idle` with caution |
| `question` | string | pane tail when `agent_status` is `blocked`; this is the question to answer |
| `agent_state_reason` | string | when `agent_status` is `unknown`: one of `herdr_unavailable`, `agent_not_found`, `undetected`, `parse_error`, `timeout`, `no_herdr_binding` |

Poll every 30 seconds. Give up at 45 minutes and surface the last poll
response.

**Poll loop by `agent_status`:**

- **`working`** — leave it alone; re-poll in 30 s.
- **`blocked`** — read `question`; answer in the pane (or surface to the human);
  re-poll immediately after answering.
- **`done` or `idle`** — check completion signals below; the settle guard
  (`settled: true`, ~1.5 s) prevents a transient `done` from masking a
  following `blocked`.
- **`unknown`** — fall back to the screen/movement heuristics in
  `self-hosting.md § Monitoring the pane`; do not treat `unknown` as done.

**Completion signals** (checked in order — `agent_status` alone never decides):

1. **`done_via: "marker"`** — the agent wrote `/run/nexus/delegate-done`; the
   file's content is in `marker_content`. This is the preferred signal: the agent
   writes it as its final act (one-line summary). Declare done immediately.

2. **`done_via: "git"`** — marker absent; falls back to git heuristic. Declare
   done when `git_log` is non-empty AND `git_status` is empty.

`agent_status: "done"` is supporting evidence, not proof. The marker and git
state are the sole completion proof.

**Brief requirement:** every brief dispatched via `delegate_agent_dispatch` must
instruct the agent to write the marker on completion (this is injected
automatically via `standingOrders`).

**CLI equivalent:**

```bash
# marker check
ssh <worktree-sandbox> cat /run/nexus/delegate-done

# git fallback
git -C <worktree-path> log --oneline HEAD
git -C <worktree-path> status --porcelain
```

The diff is always reachable and never truncated by the Claude UI:

```bash
git -C <worktree-path> diff <base>...HEAD
```

Read the diff as the primary deliverable, not pane output.

## 4. Collect and verify

Read the diff. Confirm the acceptance criteria from the brief are met against
the code, not against the agent's report. An agent reporting "done" or "green
tests" is a claim, not evidence — verify independently.

## 5. Reclaim

**MCP tool — `delegate_teardown`** reverses `delegate_worktree_create` in one
call: it resolves the herdr workspace bound to the sandbox (`nexus herdr
list`), runs `herdr worktree remove --workspace <ws-id>` — which closes the
workspace, removes the git worktree, and reaps the sandbox and its disks
through the `worktree.removed` hook — then verifies the sandbox is gone from
`nexus ps`. It falls back to `nexus rm <ref>` only when no workspace is
bound to the ref, or when the sandbox is still listed after the herdr remove.
If the worktree has uncommitted or untracked changes, the tool returns a
structured error listing the changed files rather than silently discarding
them; pass `force:true` to override and discard those changes.

```json
{ "ref": "project/name" }
```

To discard uncommitted changes and remove anyway:

```json
{ "ref": "project/name", "force": true }
```

Returns `{removed, workspace_id, handle, sandbox_id, output}`. No CLI step is
needed afterwards — the workspace is already closed, so a manual
`herdr worktree remove` would fail with `workspace_not_found`.

**CLI equivalent for teardown:**

```bash
herdr worktree remove --workspace <ws-id>   # closes workspace, removes worktree, reaps sandbox
~/.local/bin/nexus ps                      # confirm the handle is gone
~/.local/bin/nexus rm <handle>             # only if it is still listed
```

Order matters: `nexus rm` closes the herdr workspace as a side effect. Running
it first leaves `herdr worktree remove` failing with `workspace_not_found` and
the git worktree still on disk.

After reclaim, confirm:

```bash
git -C <repo> branch -d <branch>    # -d refuses an unmerged branch
```

Use `-d`, never `-D`. A refusal means work is not merged and the reclaim was
premature.

## When to stop instead of remove

If work is blocked but you intend to return:

```bash
~/.local/bin/nexus stop <handle>
```

This preserves the disk and record. The sandbox can be restarted later. The
worktree and herdr workspace stay intact.
