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

**What the tool does** (five steps, all driven by the nexus3 MCP plugin process):

1. Resolves the herdr binary (`HERDR_BIN_PATH` env, else `herdr` on PATH).
2. Runs `herdr workspace list` to find the workspace whose `checkout_path` is `repo_path` (fallback: `HERDR_WORKSPACE_ID` env).
3. Runs `herdr worktree create --workspace <parent> --branch <branch> [--base <base>] --no-focus` — herdr creates the linked git worktree (under `~/.herdr/worktrees/<repo>/<branch>`) and its workspace.
4. Runs `nexus3 herdr worktree-sandbox <newWorkspaceID>` — the same verb the herdr `worktree.created` hook runs. It derives the handle `<repo>/<branch-slug>`, resolves the image from `.nexus/config.yaml`, converts `egress.policy`/`egress.secrets` into policy flags, mounts the checkout at `/workspace` plus the main repo's `.git` and `.groundwork` dirs, attaches named volumes (`<slug>-docker` only with a Containerfile; `<slug>-agentcfg`, `<slug>-gocache`, `<slug>-gopath` always), writes the herdr↔sandbox binding, and opens the guest pane.
5. Runs `nexus3 herdr list` and returns the result.

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
~/.local/bin/nexus3 herdr worktree-sandbox <new-workspace-id>
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

Returns `{ok, data:{output}}` once the brief is **confirmed delivered** to the
agent's input — not when the work is done. A non-zero / error result means the
brief was NOT submitted; the VM is up but the agent has not read it.

**CLI equivalent:**

```bash
~/.local/bin/nexus3 herdr agent --autonomous --no-focus <handle> "<brief>"
```

See `delegate-briefs.md` for what a brief must contain.

## 3. Poll for completion

**MCP tool — `delegate_agent_poll`**

```json
{ "ref": "project/name" }
```

Returns `{ok, data:{git_log, git_status, branch_name}}`. Poll every 30 seconds.
Declare done when `git_log` is non-empty AND `git_status` is empty. Give up at
45 minutes and surface the last poll response as evidence.

**CLI equivalent:**

```bash
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

**MCP tool — `delegate_teardown`** removes the sandbox and guest disk, but NOT
the host git worktree:

```json
{ "ref": "project/name" }
```

After teardown (or instead, if something is wrong), close the worktree via CLI:

```bash
herdr worktree remove --workspace <ws-id>
```

**CLI equivalent for teardown:**

```bash
herdr worktree remove --workspace <ws-id>   # first — while workspace still exists
~/.local/bin/nexus3 rm <handle>             # then the sandbox and its disks
```

Order matters: `nexus3 rm` closes the herdr workspace as a side effect. Running
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
~/.local/bin/nexus3 stop <handle>
```

This preserves the disk and record. The sandbox can be restarted later. The
worktree and herdr workspace stay intact.
