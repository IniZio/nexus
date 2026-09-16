---
name: nexus3-slice-sandbox
description: "Run a unit of nexus3 development inside a nexus3 VM: create a git worktree, open it as a herdr workspace, bind a sandbox (optionally with nested KVM), dispatch an in-guest Claude agent with a brief, then checkpoint, verify, merge, and reclaim. Use this whenever work on the nexus3 repo is going to be delegated to an agent in a sandbox, whenever a motive slice needs its own isolated environment, whenever someone says to fan out slices, run a slice in a VM, spin up a worktree sandbox, or dispatch an in-guest agent, and whenever a change needs to be proven against a real booted VM rather than unit tests. Also use it when a slice is finished and its sandbox should be cleaned up or torn down, when asking which sandboxes are still needed, or when sandboxes are piling up and eating host memory. Also use it when diagnosing why a worktree sandbox came up wrong — missing /dev/kvm, a missing .groundwork, a stale binary, an unexpected 'already bound', or duplicate herdr tabs."
---

# nexus3 slice sandboxes

Working pattern for nexus3 development: each unit of work gets its own git
worktree, herdr workspace, and nexus3 VM, with a Claude agent running inside.
The repo develops itself in its own product. Most failures below arise because
the host side succeeded while the guest silently got something else — verify
inside the guest, not from the host's intentions.

## Variables

Derive the two you need at the start, and use them everywhere including in
commands you hand to a person:

```bash
REPO=$(git rev-parse --show-toplevel)   # main checkout
MAIN_WS=$HERDR_WORKSPACE_ID             # or find it in `herdr workspace list`
```

A recipe that bakes in one machine's `/home/<user>/...` works exactly once.

## 1. Worktree and workspace

**Preferred path when the nexus3 MCP server is available:** `delegate_worktree_create`
from `plugins/claude/skills/nexus3/references/delegate-loop.md` handles create,
boot, and ID plumbing in one tool call.

**CLI path:**

```bash
herdr worktree create --workspace "$MAIN_WS" --branch nexus3/<name> --base develop --no-focus
```

Read the path and workspace id from the JSON response — do not predict them.

The sandbox git perimeter derives its push allowlist from the worktree's branch
at create time. Create the worktree on the exact push branch; a detached-HEAD
worktree produces a sandbox that can push nothing.

For an existing worktree: `herdr worktree open --workspace "$MAIN_WS" --path <path> --no-focus`.

**Do not use `herdr workspace create --cwd`** — it produces a plain workspace with
no worktree association; `is_linked_worktree` is unset and `SourceWorkspaceID`
is empty, which changes how the auto-create predicates behave. Verify:

```bash
herdr workspace list   # new workspace must show worktree.is_linked_worktree = true
```

See `references/gotchas.md` → **Wrong workspace verb**.

## 2. Sandbox (auto-provision)

Creating the workspace fires an auto-provision hook that binds a sandbox. No
explicit call is usually needed. To force it or pass flags:

```bash
nexus3 herdr worktree-sandbox [--nested] <workspace-id>
```

**You cannot win a race against the auto-provision hook.** It runs on workspace
create; a concurrent explicit call reports `already bound (concurrent create
race), reusing existing sandbox`. And you cannot undo it afterwards, because
`nexus3 sandbox rm` closes the herdr workspace — every remaining pane is
guest-backed, so they exit with the VM and the workspace dies. "Create workspace
→ remove sandbox → recreate with different flags" is therefore impossible.

The way through is to change what the auto-provision *reads*. For nested
virtualisation, set `sandbox.nested: true` in `.nexus/config.yaml` before
creating the workspace. See [Nested virtualisation](#nested-virtualisation).

See `references/gotchas.md` → **rm kills the workspace** and **Reused workspace IDs**.

## 3. Dispatch the agent

```bash
nexus3 herdr agent --autonomous --no-focus <handle> "<brief>"
```

A zero exit means the brief was confirmed delivered; a non-zero exit means it was
not — the VM is up and the pane is live but nobody has read the brief.

For agent config projection, workspace-trust seeding, pane API basics, and the
`herdr pane run` / `send-keys` traps, see:
`plugins/claude/skills/nexus3/references/agent-in-sandbox.md`

For what a brief must contain, see `references/briefs.md` (nexus3-specific
additions on top of `plugins/claude/skills/nexus3/references/delegate-briefs.md`).

In-guest agents are **not addressable by `herdr agent` verbs**. Drive them through
the pane surface:

```bash
herdr pane run <pane-id> "<text>"
herdr pane read <pane-id> --source recent-unwrapped --lines 60
```

`--source recent-unwrapped` returns EMPTY on a pane that has not scrolled yet;
fall back to `--source visible` rather than reading empty as idle.

### 3b. Watch the pane

Dispatch is not the end of the step. An in-guest agent will stop waiting for
input and nothing tells you when. Start a watcher in the same turn you dispatch:

```bash
scripts/watch-pane.sh <pane-id>      # run_in_background: true
```

**Detect work by movement, not by matching a string.** A working agent repaints
every second (spinner, elapsed timer). A stopped agent renders a static pane.
Movement holds across layout changes; marker strings do not.

When the watcher wakes you on a question, answer through the pane surface, then
**restart the watcher** — it exits on each stop.

See `references/gotchas.md` → **Watch-pane false-stop modes** for the six
documented failure patterns (scroll blindness, background-subagent footer, etc.).

Two rules:
- One definition of "working" called by every loop. Two copies drift.
- Movement cannot tell a question from a stop. Strings are the QUESTION
  discriminator, applied only after the pane settles.

Bias every ambiguous case toward WORKING. A false idle wastes time; a missed
stop only costs waiting.

## 4. Checkpoint before rebuild

The in-guest `~/.claude` upperdir is tmpfs — a sandbox rebuild destroys the
agent's session. Before recreating a VM:

```bash
herdr pane run <pane-id> "Commit everything you have to <branch> now as ONE
commit prefixed 'wip:' whose body states what is finished, what is half-written,
what you were about to do next, and any premise from the brief you found wrong.
Do not tidy, do not finish anything. Reply with the sha."
```

Then wait on the commit, not on the pane:

```bash
until [ -n "$(git -C <worktree> log --oneline develop..HEAD)" ]; do sleep 10; done
```

## 5. Verify

For the go-test-in-guest skip trap, login-shell PATH, and CA certificate setup,
see `plugins/claude/skills/nexus3/references/guest-setup.md`.

Nexus3-repo-specific habits:

**Login shell for all guest probes.** `nexus3 exec <h> -- sh -c 'command -v make'`
reports missing for installed tools. Use `bash -lc`.

**In-guest `ok` can mean zero tests ran.** `internal/cli`, `internal/core/service`,
`internal/core/recovery`, `internal/core/perimeter/netstack`, and `internal/mcp`
exit 0 when `/proc/1/comm == "nexus3-agent"`. The skip line goes to stderr; `go
test` suppresses it for passing packages. Gate on the host worktree where nothing
skips, or use `unshare --pid --mount-proc --fork` in-guest for pure-filesystem
packages only.

**Read the real exit code, never through a pipe.** `make test | grep FAIL` reports
grep's status. Use `${PIPESTATUS[0]}` or `make test > log 2>&1; echo $?`.

**Re-run mutation proofs yourself** on anything security- or correctness-critical.
Break the line, confirm RED, restore, confirm GREEN. An agent's "mutation-proven"
is a claim, not evidence.

## 6. Reclaim

For the generic teardown sequence, see `plugins/claude/skills/nexus3/references/delegate-loop.md § 5. Reclaim`.

**Completion convention for this repo: merged to `develop`.** Verify against git,
not against the agent's report:

```bash
git -C "$WORKTREE" rev-list --count develop..HEAD   # must be 0
git -C "$WORKTREE" status --porcelain               # must be empty
git -C "$WORKTREE" ls-files --others --exclude-standard  # must be empty
```

**Order matters.** Take the workspace down first; `nexus3 rm` closes it as a side
effect, leaving `herdr worktree remove` failing with `workspace_not_found`:

```bash
herdr worktree remove --workspace <ws-id>   # first
nexus3 rm <handle>                          # then VM and disks
git -C "$REPO" branch -d nexus3/<name>      # -d, never -D
```

`git branch -d` refuses an unmerged branch — an independent second check that
the reclaim is safe. A `-d` that fails means something is about to be lost.

To keep the RAM but return later: `nexus3 stop <handle>` preserves disk and record.

## Nested virtualisation

Needed when work must be proven against a real booted VM: supervisor lifecycle,
adoption, egress perimeter, anything where a unit test can only assert call shape.
Without it an agent silently downgrades to hermetic tests.

The channel that actually works end-to-end is `sandbox.nested: true` in the
worktree checkout's `.nexus/config.yaml` — this is what the auto-provision hook
reads. Set it before creating the workspace.

`--nested` on `nexus3 herdr worktree-sandbox` is an operator shortcut but loses
the race described in step 2.

Always confirm in the guest (both Intel `vmx` and AMD `svm`):

```bash
nexus3 exec <handle> -- bash -lc 'ls -l /dev/kvm; grep -m1 ^flags /proc/cpuinfo | tr " " "\n" | grep -E "^(vmx|svm)$"'
```

See `references/gotchas.md` → **Nested dropped at the supervisor**.

## Sequencing slices

Parallel fan-out is only correct for slices with disjoint files **and** no API
dependency. A slice that must consume a seam another slice is still building
cannot run beside it — it will report the seam missing and leave its deliverable
unbuilt, which looks like partial success rather than a scheduling error. Check
for a consumes-relationship, not just file overlap.

State each slice's file ownership in its brief. Tell a slice that needs something
outside its territory to record the requirement for the owning slice rather than
reaching across.

## Answering a person

Give the finding and the fix. Do not cite this skill's own files back — name
things the person can actually open: a repo file and line, a commit, a command
they can run.

## Traps index

Full symptom-first catalogue in `references/gotchas.md`. Costliest ones:

- **`make build` writes no binary** — stale binary trap; use `go build -o nexus3 ./cmd/nexus3`
- **Guest has no `make` or `gcc`** — agents fall back to bare `go` with `-race` off; `apt-get install -y build-essential` in the brief
- **`.groundwork/` is gitignored** — mounted at host abs path; point agents at `$REPO/.groundwork/...`
- **herdr reuses workspace IDs** — new workspace can inherit a dead one's sandbox binding; check `nexus3 herdr list` before believing "already bound"
