---
name: nexus3-slice-sandbox
description: "Run a unit of nexus3 development inside a nexus3 VM: create a git worktree, open it as a herdr workspace, bind a sandbox (optionally with nested KVM), dispatch an in-guest Claude agent with a brief, then checkpoint, verify and merge its work. Use this whenever work on the nexus3 repo is going to be delegated to an agent in a sandbox, whenever a motive slice needs its own isolated environment, whenever someone says to fan out slices, run a slice in a VM, spin up a worktree sandbox, or dispatch an in-guest agent, and whenever a change needs to be proven against a real booted VM rather than unit tests. Also use it when diagnosing why a worktree sandbox came up wrong — missing /dev/kvm, a missing .groundwork, a stale binary, an unexpected 'already bound', or duplicate herdr tabs."
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

Branch under `nexus3/**`. The sandbox git perimeter allowlists only
`refs/heads/nexus3/**`, so a branch outside it cannot be pushed from inside a
sandbox.

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

In-guest agents are **not addressable by `herdr agent` verbs**. `herdr agent
prompt <pane>` returns `agent_not_ready: not an active named agent`, because
herdr sees the pane but cannot bind an agent identity through the VM boundary.
Drive them through the pane surface instead:

```bash
herdr pane run <pane-id> "<text>"          # sends text + Enter
herdr pane read <pane-id> --source recent-unwrapped --lines 60
```

A `pane run` sent while the agent is working queues as its next prompt rather
than interrupting — useful, but it means "no response yet" is ambiguous between
busy and never-delivered.

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
