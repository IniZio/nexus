# Delegation loop — step by step

Nothing below hardcodes a path or handle. Read workspace IDs and worktree paths
from the JSON responses of each step rather than predicting them.

## 1. Create the worktree-bound sandbox

**MCP tool — `delegate_worktree_create`**

```json
{
  "repo_path": "/abs/path/to/repo",
  "handle": "project/branch-name",
  "memory_mib": 1024
}
```

`repo_path` must be an absolute host path to the git repo or an existing
worktree. `handle` sets the sandbox project/name. `memory_mib` defaults to 512
if omitted or 0. `allowed_branches` must not be set — the call is rejected.

The tool creates a git worktree on a new branch derived from the handle name,
opens it as a herdr worktree workspace, and creates and boots the sandbox. Read
the returned `handle` and `output` fields; the `output` contains the workspace
ID needed for teardown.

**CLI equivalent** (when MCP server unavailable):

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
inside a builder VM. Three failure patterns to know:

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

**Bypass with an existing image.** `delegate_worktree_create` accepts an
optional `image_ref` field naming an image already in the server's image
cache. Pass it to skip the build entirely when a suitable image exists:

```json
{
  "repo_path": "/abs/path/to/repo",
  "handle": "project/name",
  "image_ref": "nexus3-agent-base"
}
```

CLI equivalent: `~/.local/bin/nexus3 create <handle> --image <ref> --workspace <worktree-path>`

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
