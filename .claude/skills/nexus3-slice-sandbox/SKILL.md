---
name: nexus3-slice-sandbox
description: "Run a unit of nexus3 development inside a nexus3 VM: create a git worktree, open it as a herdr workspace, bind a sandbox (optionally with nested KVM), dispatch an in-guest Claude agent with a brief, then checkpoint, verify, merge, and reclaim. Use this whenever work on the nexus3 repo is going to be delegated to an agent in a sandbox, whenever a motive slice needs its own isolated environment, whenever someone says to fan out slices, run a slice in a VM, spin up a worktree sandbox, or dispatch an in-guest agent, and whenever a change needs to be proven against a real booted VM rather than unit tests. Also use it when a slice is finished and its sandbox should be cleaned up or torn down, when asking which sandboxes are still needed, or when sandboxes are piling up and eating host memory. Also use it when diagnosing why a worktree sandbox came up wrong — missing /dev/kvm, a missing .groundwork, a stale binary, an unexpected 'already bound', or duplicate herdr tabs."
---

# nexus3 slice sandboxes

This is the working pattern for nexus3 development: a unit of work gets its own
git worktree, its own herdr workspace, and its own nexus3 VM, with a Claude agent
running inside that VM. The repo develops itself in its own product.

That self-hosting is the point, and also the hazard. Most of the failures below
are cases where a thing looked like it worked because the *host* side succeeded,
while the guest silently got something else. Verify inside the guest, not from
the host's intentions.

## The loop

Nothing below hardcodes a path. Derive the two you need:

```bash
REPO=$(git rev-parse --show-toplevel)        # the main checkout
MAIN_WS=$HERDR_WORKSPACE_ID                  # or find it in `herdr workspace list`
```

Keep using those variables in every command you write down, including the ones
you hand back to a person. A recipe that bakes in one machine's `/home/<user>/...`
works exactly once; the same recipe written against `$REPO` and the ids returned
by herdr works for anyone. This matters most in the places it is easiest to
forget — the `git -C <worktree>` checks in step 4 and the `.groundwork` path
inside the brief.

### 1. Worktree and workspace, in one step

```bash
herdr worktree create --workspace "$MAIN_WS" --branch nexus3/<name> --base develop --no-focus
```

This creates the git worktree, picks its path, and opens it as a workspace that
is properly associated with the repo. Read the path and workspace id from the
JSON response rather than predicting them.

`nexus3/<name>` here is just this repo's own branch-naming convention, not a
push requirement. The sandbox git perimeter derives its push allowlist from
the worktree's own branch at create time (D-PD-38 / TBD-1): whatever branch
is checked out in `$MAIN_WS`'s worktree when the sandbox is created is the
one ref the sandbox can push, regardless of its name or namespace. It cannot
push any other branch, including the repo's default branch — create the
worktree on the exact branch you intend to push from. If the worktree is in a
detached-HEAD state (or its branch otherwise can't be resolved) at create
time, the sandbox can push nothing at all; check out a real branch first.

For a worktree that already exists, the equivalent is
`herdr worktree open --workspace "$MAIN_WS" --path <worktree-path> --no-focus`.

**Do not reach for `herdr workspace create --cwd`.** It also produces a working
workspace, which is the trap: it is a *plain* workspace that merely happens to
sit in that directory. It carries no worktree association, so
`is_linked_worktree` is unset and the herdr UI floats it at top level instead of
nesting it under the repo. The label gets rewritten later by `worktree-sandbox`,
so a wrongly-created workspace *looks* identical in a listing while being
structurally different — and `SourceWorkspaceID`, which drives the auto-create
predicates, is empty.

Verify rather than assume:

```bash
herdr workspace list   # the new workspace must show worktree.is_linked_worktree = true
```

### 2. Sandbox

Creating the workspace fires an auto-provision hook that binds a sandbox. You
usually do not need to call anything. To force it, or to pass flags:

```bash
nexus3 herdr worktree-sandbox [--nested] <workspace-id>
```

**You cannot win a race against the auto-provision hook.** It runs on workspace
create and will report `already bound (concurrent create race), reusing existing
sandbox` to your explicit call. And you cannot fix it afterwards, because
`nexus3 sandbox rm` closes the workspace — every remaining pane is guest-backed,
so they exit with the VM and the workspace dies with them. "Create workspace →
remove sandbox → recreate with different flags" is therefore impossible.

The way through is to change what the auto-provision *reads*, not to outrace it.
See Nested virtualisation below.

### 3. Dispatch the agent

```bash
nexus3 herdr agent --autonomous --no-focus <handle> "<brief>"
```

See `references/briefs.md` for what a brief must contain — the constraints that
are not discoverable from inside the guest are the ones that matter.

**A zero exit now means the brief was CONFIRMED submitted, and a non-zero exit
means it was not.** Dispatch used to paste the brief, press Enter, and report
success — but the thing it waited on was claude's prompt APPEARING, which
happens before submission and stays true after a brief strands in the input box.
Observed rate: one stranded brief in three, all three reported as running. The
command now reads the pane back and requires positive evidence (the input box
emptied, or the pane repainting) before it says the agent is running; it retries
Enter and then fails loudly. Treat a non-zero exit as *the slice did not start* —
the VM is up and the pane is live, but nobody has read the brief. An unreadable
pane also fails, deliberately: a check that cannot decide must refuse.

In-guest agents are **not addressable by `herdr agent` verbs**. `herdr agent
prompt <pane>` returns `agent_not_ready: not an active named agent`, because
herdr sees the pane but cannot bind an agent identity through the VM boundary.
Drive them through the pane surface instead:

```bash
herdr pane run <pane-id> "<text>"          # sends text + Enter
herdr pane read <pane-id> --source recent-unwrapped --lines 60
```

`--source recent-unwrapped` returns EMPTY on a pane that has not scrolled yet —
the normal state of a freshly dispatched agent. Fall back to `--source visible`
rather than reading the empty string as an empty pane; empty compares equal to
empty, which reads as "no movement", which reads as a stop.

A `pane run` sent while the agent is working queues as its next prompt rather
than interrupting — useful, but it means "no response yet" is ambiguous between
busy and never-delivered.

### 3b. Subscribe to the agent before you do anything else

Dispatching is not the end of the step. An in-guest agent will stop and wait for
a reply — it hits a question, an approval, a scope decision it cannot settle
alone — and **nothing tells you when that happens.** herdr has no event bus, the
agent is not addressable by `herdr agent` verbs, and the VM boundary means no
notification reaches the orchestrator. A dispatched agent that stopped after
ninety seconds looks exactly like one working hard for an hour.

So start a watcher in the same turn you dispatch, as a background task, and let
the harness re-invoke you when it exits:

```bash
scripts/watch-pane.sh <pane-id>      # run_in_background: true
```

The whole mechanism is `herdr pane read` in an until-loop, and all of its
difficulty is in the exit condition.

**Detect work by movement, not by matching a marker string.** A working agent
repaints — the spinner animates and its elapsed timer ticks every second — so
the pane text CHANGES between two reads a few seconds apart. A stopped agent
renders a static pane. That signal holds no matter which indicator the current
UI happens to use, which is what makes it survive the next layout change.

This was learned the expensive way: four false stops in one session, each from a
different pane layout, each "fixed" by a pattern the next layout defeated.

1. A slash-command overlay repaints the footer, dropping `esc to interrupt`
   *and* rendering `Enter to select` — a working agent read as idle, an
   autocomplete list read as a question.
2. An agent blocked on its own background subagent is working but is not itself
   running a tool, so the footer loses `esc to interrupt` permanently. No amount
   of re-sampling helps; the marker moved to the body.
3. The start-grace loop held its own inlined copy of the working check, so it
   still reported `AGENT_NEVER_STARTED` after `sample_state` learned that state.
4. The background-agent roster switched from `Waiting for N background agents`
   to a live subagent row — matching no marker at all.
5. A pane SCROLLED UP puts the ticking spinner outside the read window, so a
   busy agent reads as static and movement itself stops working. `herdr pane get
   <pane-id>` reports `scroll.offset_from_bottom`; an indeterminate answer —
   herdr unreachable, field absent, non-numeric — must read as SCROLLED, and
   therefore as WORKING. Do NOT reach for `herdr workspace list` here: it reports
   only each workspace's ROOT pane, so it cannot see a guest pane like `w7M:p2`.

Every individual fix was correct. The *approach* was the defect. If you find
yourself adding a fifth pattern, add it as a fast path only, and let movement
remain the thing that actually decides.

Two rules that fall out of this:

- One definition of "working", called by every loop. Two copies drift the moment
  one learns something new — that is bug 3 above.
- Movement cannot tell a question from a stop, since both are static. Strings
  are still the QUESTION discriminator, applied only once the pane has settled.

Note the diagnostic asymmetry: a false `AGENT_IDLE` or `AGENT_NEVER_STARTED`
sends you to review work that does not exist yet, while a missed stop only costs
waiting. Bias every ambiguous case toward WORKING. And when you change this
detection, test it against a known-working pane AND a known-idle one — "always
WORKING" passes every positive test there is.

`herdr pane wait-output <pane-id> --regex '<pattern>'` is the sharper primitive
when you know the string you are waiting for: it blocks indefinitely, searches
existing output first, then polls. It is what `nexus3 herdr agent` itself uses to
wait for the guest shell and the Claude prompt. Reach for it when you are waiting
on one specific marker, and the poll loop when you are waiting for "any stop at
all", since idleness is an absence and a regex cannot match one.

Watch the pane, not the clock. Do not sit in a foreground `sleep` and do not
promise to "check back later" — a blocked agent burns a live VM's worth of
resident guest RAM while it waits, and the slice makes no progress at all.

When the watcher wakes you on a question, answer through the pane surface:

```bash
herdr pane send-keys <pane-id> 2      # numbered menu: picks option 2
herdr pane run <pane-id> "<text>"     # free-text reply
```

Then **restart the watcher** — it exits on each stop, so an unreplaced watcher
means the next question hangs silently just like the first.

Treat a scope correction as a result, not an interruption. Briefs are written
from the orchestrator's model of the code, which is most often wrong precisely
where it matters; an agent that stops to say "your premise is false" has done the
most valuable thing it can do, and it can only do it if someone is listening.
Verify the correction against the code yourself before answering — `grep` for the
call sites it claims are missing — because the answer you give redefines the
slice.

### 4. Checkpoint before you tear anything down

Worktrees are host-side, so a sandbox rebuild never loses committed files. It
*does* destroy the agent's session — the in-guest `~/.claude` upperdir is tmpfs.
Before recreating a VM, have the agent commit:

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

### 5. Verify, then merge

Read the diff and re-run the agent's own mutation proofs yourself. Agents report
tests as proven that are not — see Verification below.

### 6. Finish: reclaim the sandbox once the work has landed

A slice sandbox is disposable. It is not free while it sits there: each one holds
a live VM with memfd-backed guest RAM that is resident and unswappable, plus a
docker disk and a herdr workspace. Sandboxes left running after their slice
landed are the ordinary cause of host memory pressure here, and the reason a
later `make test` trips the global OOM killer.

The existing reapers do **not** cover this. Both are bound to herdr pane and
workspace liveness — the detached reaper fires when the last pane closes, and
`space-prune` is its backstop — so a sandbox whose work is finished but whose
workspace is still open is invisible to both. Finishing is a step you take, not
one that happens to you.

nexus3 itself stays a primitive and takes no view on when work is "done": the
verbs are `nexus3 rm` and `herdr worktree remove`, and the completion rule
belongs to the repo being worked on. **For nexus3 the convention is: merged to
`develop`.** Check it against git, not against the agent's report:

```bash
git -C "$WORKTREE" rev-list --count develop..HEAD   # 0 = every commit has landed
git -C "$WORKTREE" status --porcelain               # empty = nothing uncommitted
git -C "$WORKTREE" ls-files --others --exclude-standard   # empty = nothing untracked
```

All three must come back empty or zero. Both gates matter and they fail in
opposite directions: unmerged commits mean the work is not done, and a dirty or
untracked tree means work exists that was never committed at all — which no
amount of merging will have saved. Reclaim only when every check passes:

```bash
herdr worktree remove --workspace <ws-id>            # workspace + worktree, FIRST
nexus3 rm <handle>                                   # then the VM and its disks
git -C "$REPO" branch -d nexus3/<name>               # -d, never -D
```

**The order matters.** `nexus3 rm` closes the herdr workspace as a side effect,
so running it first leaves `herdr worktree remove` failing with
`workspace_not_found` and the git worktree still on disk — you then have to
finish by hand with `git worktree remove <path>`. Take the workspace down while
it still exists.

Use `git branch -d` deliberately: it refuses a branch that is not fully merged,
so it is a second, independent check on the same claim — a `-d` that fails means
the reclaim was wrong and something is about to be lost. Never reach for `-D` to
get past it.

Leave the sandbox alone when any check fails, and say which one and why. A slice
that is blocked, half-finished, or waiting on review still needs its VM. If you
only want the RAM back and intend to return, `nexus3 stop <handle>` keeps the
disk and the record so the sandbox can be restarted.

## Nested virtualisation

Needed whenever the work must be proven against a real booted VM: supervisor
lifecycle, adoption, egress perimeter, anything where a unit test can only
assert the shape of a call. Without it an agent silently downgrades to hermetic
tests and reports success.

Two opt-in channels, both default-off (D-N3N-02 — nested widens the isolation
perimeter):

- `sandbox.nested: true` in `nexus3.yaml`, read **only** from the trusted ref
  (`refs/remotes/origin/HEAD`, i.e. `origin/main`) so a worktree branch cannot
  grant itself `/dev/kvm`. This is what the auto-provision hook reads, so it is
  the channel that actually works end-to-end. It only takes effect once the file
  is on main.
- `--nested` on `nexus3 herdr worktree-sandbox`, for an operator. Safe because
  it is not branch-controlled — but it loses the race described above.

Always confirm in the guest, because every layer above it can succeed while the
guest gets nothing:

```bash
nexus3 exec <handle> -- bash -lc 'ls -l /dev/kvm; grep -m1 ^flags /proc/cpuinfo | tr " " "\n" | grep -E "^(vmx|svm)$"'
```

`vmx` is Intel, `svm` is AMD — checking only for `vmx` on an AMD host reads as
failure when nested is working perfectly.

## Verification

Two habits, both learned from this repo biting back.

**Use a login shell for guest probes.** `nexus3 exec <h> -- sh -c 'command -v
cloud-hypervisor'` reports MISSING for tools that are installed, because the
non-login shell has a different PATH. Use `bash -lc`. A false negative here sends
you rebuilding an image that was already correct.

**In-guest, `ok` can mean "skipped entirely".** `internal/cli` and
`internal/core/service` have a `TestMain` that exits 0 when
`/proc/1/comm == "nexus3-agent"` — true inside every sandbox. `go test`
suppresses buffered output for passing packages, so the skip line never prints
and the package reports a clean `ok`. A guest suite run is therefore blind to
those two packages while looking fully green.

Confirm before trusting an in-guest run: `go test ./internal/cli/... -v` prints
the skip line. Better, gate on the **host** worktree, where nothing skips — that
is where a regression like a missing surface-contract entry actually surfaces.
An agent that needs a real run inside the guest can use
`unshare --pid --mount-proc --fork`, which changes `/proc/1/comm` without
touching the skip check.

**Read the real exit code, never one through a pipe.** `make test | grep FAIL`
reports *grep's* status, so a failing suite reads as success. Capture it
directly (`make test > log 2>&1; echo $?`) or use `${PIPESTATUS[0]}`. This
produced a confidently wrong "the repo's gate can't fail" finding in one session.

**Re-run the mutation proof yourself on anything security- or
correctness-critical.** A subagent reporting "mutation-proven" is a claim, not
evidence. Twice in one session a reported-proven default-off guard turned out to
have no test at the layer that decided it — flipping the default left the whole
package green. The check is cheap: break the line, run the narrowed test, confirm
RED, restore, confirm GREEN.

## Answering a person, not a machine

When you use this skill to answer someone, give them the finding and the fix.
Do not cite this skill's own files back at them — `references/gotchas.md` is not
a path they have, and naming it turns a clear answer into a puzzle. Cite things
they can actually open: a repo file and line, a commit, a command they can run.

## Traps

Full symptom-first catalogue in `references/gotchas.md`. Read it when something
behaves oddly — each entry names how the trap *presents*, which is rarely how it
reads in the code. The ones that cost the most time:

- **`make build` writes no binary.** It runs `go build ./...`, which type-checks
  and discards binaries. `./nexus3` in the repo root can be arbitrarily stale, so
  installing it ships old code and your change appears not to work. Use
  `go build -o nexus3 ./cmd/nexus3`.
- **The guest has no `make` or `gcc`** until the image is rebuilt, so agents
  cannot follow the repo's own build rule and quietly fall back to bare `go` with
  `-race` off. Tell them to `apt-get install -y build-essential` first.
- **`.groundwork/` is gitignored**, so a worktree never contains it. It is mounted
  at its host absolute path — point agents at
  `$REPO/.groundwork/...` expanded to its real host path, never a
  `/workspace`-relative one.
- **herdr reuses workspace IDs**, and nexus3 bindings are not expired, so a new
  workspace can inherit a closed one's binding to a completely different repo's
  sandbox. Check `nexus3 herdr list` before believing an "already bound" refusal.

## Sequencing slices

Parallel fan-out is only correct for slices with disjoint files *and* no API
dependency. A slice that must consume a seam another slice is still building
cannot run beside it — it will report the seam missing and leave its own
deliverable unbuilt, which looks like partial success rather than a scheduling
error. Check for a consumes-relationship, not just file overlap, before fanning
out; sequence dependents behind the slice that defines the seam.

State each slice's file ownership in its brief, and tell a slice that needs
something outside its territory to record the requirement for the owning slice
rather than reaching across.
