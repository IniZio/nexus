# Worktree sandbox setup — nexus3-buildkit-poisoned-snapshot-on-vm-death

Ordered commands and what to verify after each.

---

## Step 1 — Worktree + workspace

```bash
herdr worktree create \
  --workspace w8 \
  --branch nexus3/buildkit-poisoned-snapshot \
  --base develop \
  --no-focus
```

**What to check:**
Read the JSON response for `checkout_path` and `workspace_id` — do not predict them.
Then confirm the workspace is properly linked:

```bash
herdr workspace list
```

The new entry must show `worktree.is_linked_worktree = true`. If it does not, the
workspace was created wrong (a plain `workspace create --cwd` was used somewhere) —
delete and redo via `herdr worktree create`, not `herdr workspace create`.

---

## Step 2 — Sandbox

The auto-provision hook fires on workspace create and binds a sandbox automatically.
Do not race it. Just confirm it landed:

```bash
nexus3 herdr list
```

Look for a row whose `handle` is `nexus3/buildkit-poisoned-snapshot`. Note the
`sandbox_id`, `pane_id`, and `workspace_id` — you will need them in later steps.

If the row is missing, wait 15 s and check again. If it is still absent, force it:

```bash
nexus3 herdr worktree-sandbox <new-workspace-id>
```

**Note on nested KVM:** This motive (TBD-1/TBD-3) requires proving behavior against
a real booted builder VM, so nested virtualisation is needed. The auto-provision hook
reads `sandbox.nested` from `origin/main` (not the worktree branch), and that flag is
not set on main today. The practical option is to pass `--nested` manually — but that
call will lose the race against the auto-provision hook. The only reliable path is:

1. Check if the auto-provisioned sandbox has `/dev/kvm` (step 3 below).
2. If not, note the limitation for the agent brief: the agent must skip any test
   that requires a live nested VM and record it as a gap.

Alternatively, if the motive warrants it, set `sandbox.nested: true` in `nexus3.yaml`
on main first, then create the workspace.

---

## Step 3 — Verify the guest environment

```bash
nexus3 exec nexus3/buildkit-poisoned-snapshot -- bash -lc \
  'ls -l /dev/kvm 2>&1; grep -m1 ^flags /proc/cpuinfo | tr " " "\n" | grep -E "^(vmx|svm)$"; echo "PATH=$PATH"'
```

**What to check:**
- `/dev/kvm` present → nested VM tests are available; tell the agent.
- `/dev/kvm` absent → nested tests are not available; tell the agent to record
  anything that requires a live builder VM as an unverified claim.
- `vmx` (Intel) or `svm` (AMD) must appear — checking only for `vmx` on this host
  gives a false negative if it is AMD.
- PATH must include `/usr/local/go/bin` or wherever `go` lives. A non-login shell
  lacks it; always use `bash -lc` for guest probes.

---

## Step 4 — Confirm the worktree is on the right base

```bash
git -C $HOME/.herdr/worktrees/nexus3/buildkit-poisoned-snapshot \
  log --oneline develop..HEAD
```

Expect zero commits (clean branch off develop). If you see commits that do not belong
here, the branch base was wrong.

Also confirm the motive charter is reachable from inside the guest:

```bash
nexus3 exec nexus3/buildkit-poisoned-snapshot -- bash -lc \
  'ls $HOME/nexus3/.groundwork/motives/nexus3-buildkit-poisoned-snapshot-on-vm-death/'
```

`.groundwork/` is gitignored and absent from the worktree. It is mounted at its host
absolute path — the path above, not a `/workspace`-relative one. If `ls` fails, the
mount is missing and the brief must tell the agent not to use that path.

---

## Step 5 — Dispatch the agent

```bash
nexus3 herdr agent --autonomous --no-focus \
  nexus3/buildkit-poisoned-snapshot \
  "$(cat <<'EOF'
Motive: nexus3-buildkit-poisoned-snapshot-on-vm-death.
Charter: $HOME/nexus3/.groundwork/motives/nexus3-buildkit-poisoned-snapshot-on-vm-death/motive.md
This file is NOT under /workspace — it is mounted at its host absolute path.

Your working tree is at /workspace (the worktree checkout), on branch
nexus3/buildkit-poisoned-snapshot, based off develop.

## Environment
- /dev/kvm: [FILL IN FROM STEP 3 — present or absent]
- make and gcc may not be installed; run `apt-get install -y build-essential` before
  any make invocation.
- `make build` runs `go build ./...`, which type-checks but writes no binary.
  To install a binary use: go build -o /usr/local/bin/nexus3 ./cmd/nexus3
- The build rule is `make build`, `make vet`, `make test`. Do NOT run bare
  `go test ./...` — it has OOM-killed the host login session by exhausting RAM
  across package-parallel test binaries. Use `make test GOTEST_P=1 GOTEST_PARALLEL=1`
  for a single-package run.

## What to investigate and implement

Focus on TBD-1 from the motive: making the builder cache disk crash-consistent.

Relevant files:
- internal/core/agent/builder_role_linux.go — step 4 is the success-only fsync path
- internal/core/builder/cachedisk.go — cache disk lifecycle
- internal/core/agent/rootfs_verify.go — verifyAgentIntegrity (TBD-2 reference)

The root cause: builder_role_linux.go syncs the virtio-blk backend only on the
SUCCESS path. An unclean death (SIGKILL, host OOM, context cancel) commits
buildkitd's boltdb record while snapshot data never reached the disk.

Approach for TBD-1: a host-side dirty marker written before the builder VM boots and
cleared only after a clean sync. In-guest code cannot cover a SIGKILL; the dirty
marker must be host-side. Explore whether an existing fdatasync call or a dirty-marker
written at cachedisk.go mount time is the better seam.

## File ownership
You own: internal/core/agent/builder_role_linux.go, internal/core/builder/cachedisk.go
Do not modify: internal/core/agent/rootfs_verify.go (TBD-2 is out of scope for this slice)
If you need a cross-boundary change, record it as a note in your report.

## Out of scope
- TBD-2 (non-agent layer detector) — do not touch rootfs_verify.go
- TBD-3 (builder memory sizing) — do not modify vmcfg.go

## Shared-state bans
No git stash, no git reset --hard, no git clean. No writes to
.groundwork/runs/ or .groundwork/journal/. Parallel slices share git plumbing.

## Testing bar
For every guard you add: break the code (comment out the line), run the narrowed
test, confirm RED output, restore, confirm GREEN. Record both outputs verbatim.
A claim of "mutation-proven" without the actual outputs is not accepted.

Where a live nested builder VM is required and /dev/kvm is absent, state the test
as "requires nested VM — not run, live proof deferred" rather than skipping silently.

## Report back
1. What changed, by file and line range.
2. Each mutation proof with its actual RED terminal output.
3. The answer to the core question: does the chosen mechanism survive a SIGKILL to
   the guest? With your reasoning.
4. Any wrong premise you found in this brief.

Report anything that contradicts this brief — a corrected premise is a valuable
result, not a failure.
EOF
)"
```

**What to check:**
Confirm the agent pane started:

```bash
herdr pane read <pane-id-from-step-2> --source recent-unwrapped --lines 20
```

You should see the agent acknowledging the brief within ~30 s. If the pane is silent
after 60 s, the agent did not start — check `nexus3 herdr list` to confirm the sandbox
is running, then retry the dispatch.

---

## Ongoing monitoring

Poll progress without interrupting the agent:

```bash
herdr pane read <pane-id> --source recent-unwrapped --lines 60
```

A `pane run` sent while the agent is working queues as its next prompt rather than
interrupting. Send it only when you want to add an instruction, not to check status.

---

## Before tearing down — checkpoint

If the VM must be rebuilt for any reason, send this first:

```bash
herdr pane run <pane-id> "Commit everything you have to nexus3/buildkit-poisoned-snapshot now as ONE commit prefixed 'wip:' whose body states what is finished, what is half-written, what you were about to do next, and any premise from the brief you found wrong. Do not tidy, do not finish anything. Reply with the sha."
```

Wait on the commit appearing in the worktree, not on the pane:

```bash
until [ -n "$(git -C $HOME/.herdr/worktrees/nexus3/buildkit-poisoned-snapshot log --oneline develop..HEAD)" ]; do sleep 10; done
```
