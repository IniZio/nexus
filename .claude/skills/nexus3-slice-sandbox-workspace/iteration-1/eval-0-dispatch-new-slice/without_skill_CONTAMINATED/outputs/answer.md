# Worktree sandbox setup — buildkit poisoned-snapshot slice

Motive: `nexus3-buildkit-poisoned-snapshot-on-vm-death`
Branch: `nexus3/buildkit-poisoned-snapshot`
Main nexus3 workspace: `w8`

---

## Step 1 — Create the worktree

```bash
git -C $HOME/nexus3 worktree add \
  $HOME/.herdr/worktrees/nexus3/buildkit-poisoned-snapshot \
  -b nexus3/buildkit-poisoned-snapshot \
  develop
```

**Check after:** confirm it appears in the list and is on the right base:

```bash
git -C $HOME/nexus3 worktree list
```

Expected: a new row for `$HOME/.herdr/worktrees/nexus3/buildkit-poisoned-snapshot` at the current develop tip, with `[nexus3/buildkit-poisoned-snapshot]`.

---

## Step 2 — Open it as a linked herdr workspace

```bash
herdr worktree open \
  --workspace w8 \
  --path $HOME/.herdr/worktrees/nexus3/buildkit-poisoned-snapshot \
  --no-focus
```

Do NOT use `herdr workspace create --cwd`. That produces a plain workspace with no worktree association — it looks identical in a list but `is_linked_worktree` will be false and the auto-provision hook will not fire correctly.

**Check after:**

```bash
herdr workspace list
```

Find the new workspace entry (it will be labelled `nexus3:nexus3/buildkit-poisoned-snapshot`). Confirm:
- `is_linked_worktree: true`
- A sandbox binding has appeared (the auto-provision hook fires on workspace create — give it ~15 seconds)

---

## Step 3 — Confirm the sandbox binding

```bash
nexus3 herdr list
```

**Check after:** a sandbox entry for the new workspace should appear. Note the sandbox handle (e.g. `buildkit-poisoned-snapshot-<suffix>`). If you see `already bound (concurrent create race), reusing existing sandbox` that is fine — it means the auto-provision won the race and the binding exists.

**Do not** try `nexus3 sandbox rm` + recreate. The sandbox is already bound to workspace panes, so removing it closes the workspace.

---

## Step 4 — Verify nested KVM in the guest

`nexus3.yaml` already carries `nested: true`, and the auto-provision hook reads from `origin/main`. Confirm that config is on main:

```bash
git -C $HOME/nexus3 show origin/main:nexus3.yaml | grep -A2 nested
```

Then probe the guest:

```bash
nexus3 exec buildkit-poisoned-snapshot -- bash -lc \
  'ls -l /dev/kvm; grep -m1 ^flags /proc/cpuinfo | tr " " "\n" | grep -E "^(vmx|svm)$"'
```

**Check after:** `/dev/kvm` must exist and either `vmx` (Intel) or `svm` (AMD) must appear. If `/dev/kvm` is absent and the config is not on main yet, the auto-provision created a non-nested sandbox — you would need to get `nested: true` onto main first, then destroy and recreate the workspace (not just the sandbox).

---

## Step 5 — Dispatch the agent

```bash
nexus3 herdr agent --autonomous --no-focus buildkit-poisoned-snapshot "$(cat <<'BRIEF'
Motive: nexus3-buildkit-poisoned-snapshot-on-vm-death
Charter: $HOME/nexus3/.groundwork/motives/nexus3-buildkit-poisoned-snapshot-on-vm-death/motive.md
That path is NOT under /workspace — .groundwork/ is gitignored and host-mounted at its absolute host path.

Your working tree is at /workspace (the worktree), checked out from develop on
branch nexus3/buildkit-poisoned-snapshot.

Environment facts:
- /dev/kvm is present (nested KVM enabled); verify with: ls /dev/kvm
- make and gcc are NOT installed by default — run: apt-get install -y build-essential
- 'make build' runs go build ./... and writes NO binary. To build the CLI:
    go build -o nexus3 ./cmd/nexus3
- Do NOT run bare go test ./... — it has OOM-killed the host login session.
  Use: make test GOTEST_P=1 GOTEST_PARALLEL=1
  The make targets carry cgroup memory guards the bare go command does not.

Bans on shared-state mutation:
- No git stash, no git reset --hard, no git clean -f
- Do not touch .groundwork/runs/ or the groundwork journal
- Do not modify files outside /workspace

Testing bar:
- This repo's most reliable bug shape is a checker sharing the broken mechanism it checks.
  For any correctness guard you add or modify, provide a mutation proof:
  break the guarded condition, show the test goes RED, restore it, show GREEN.
  Record both outputs in your report.
- State explicitly whether a test requires a live VM or runs hermetically.
  Do not silently downgrade a VM-required test to a hermetic one and report success.

Report when done:
- Files created or modified (absolute paths)
- Mutation proof for each correctness guard (RED sha + output, GREEN sha + output)
- Any premise in this brief you found to be wrong — a corrected premise is a
  valuable result, not a failure.

Permission to contradict: report anything you found that contradicts this brief.
BRIEF
)"
```

**Check after:** confirm the agent pane appeared and has started:

```bash
# Get the pane ID from herdr workspace list for the new workspace, then:
herdr pane read <pane-id> --source recent-unwrapped --lines 40
```

You should see the agent's opening reasoning or first tool call within ~30 seconds of dispatch. If the pane shows nothing after 60 seconds, it likely did not launch — check `nexus3 herdr list` to confirm the sandbox is running and retry dispatch.

---

## Driving the agent (after dispatch)

Send prompts through the pane, not through `herdr agent prompt` — in-guest agents are not addressable by the agent verbs:

```bash
herdr pane run <pane-id> "your message here"
herdr pane read <pane-id> --source recent-unwrapped --lines 60
```

---

## Before tearing anything down — checkpoint commit

```bash
herdr pane run <pane-id> "Commit everything you have to nexus3/buildkit-poisoned-snapshot now as ONE commit prefixed 'wip:' whose body states what is finished, what is half-written, what you were about to do next, and any premise from the brief you found wrong. Do not tidy, do not finish anything. Reply with the sha."
```

Wait on the commit, not the pane:

```bash
until [ -n "$(git -C $HOME/nexus3 log --oneline develop..nexus3/buildkit-poisoned-snapshot)" ]; do sleep 10; done
git -C $HOME/nexus3 log --oneline develop..nexus3/buildkit-poisoned-snapshot
```
