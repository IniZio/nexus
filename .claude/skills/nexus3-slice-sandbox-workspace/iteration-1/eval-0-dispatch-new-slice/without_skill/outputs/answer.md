# Commands: worktree sandbox for nexus3-buildkit-poisoned-snapshot-on-vm-death

## Step 0 — anchor the two variables

```bash
REPO=$(git -C $HOME/nexus3 rev-parse --show-toplevel)
MAIN_WS=w8   # nexus3 main workspace, confirmed from `herdr workspace list`
```

Check after: `echo $REPO $MAIN_WS` — must print the repo root and `w8`, not empty strings.

---

## Step 1 — create the worktree + workspace

```bash
herdr worktree create \
  --workspace "$MAIN_WS" \
  --branch nexus3/nexus3-buildkit-poisoned-snapshot-on-vm-death \
  --base develop \
  --no-focus
```

Read the JSON response for two values and capture them:

```bash
# from the JSON output of the command above:
SLICE_WS=<workspace_id from response>
WORKTREE=<checkout_path from response>
```

Check after:

```bash
herdr workspace list
```

Find the new workspace entry and confirm:
- `worktree.is_linked_worktree` is `true`
- `worktree.repo_root` matches `$REPO`
- `label` is `nexus3:nexus3/nexus3-buildkit-poisoned-snapshot-on-vm-death`

If `is_linked_worktree` is false or missing, the workspace was created wrong (plain
workspace, not a worktree-linked one). Do not proceed — delete it and re-run using
`herdr worktree create`, not `herdr workspace create --cwd`.

---

## Step 2 — wait for the auto-provisioned sandbox

The auto-provision hook fires on workspace create. Do not race it with a manual
`nexus3 herdr worktree-sandbox` call; that will collide and report "already bound
(concurrent create race), reusing existing sandbox".

```bash
nexus3 herdr list
```

Check after: an entry appears whose workspace id matches `$SLICE_WS`. Note the
sandbox handle (e.g. `bk-poison-1` or whatever nexus3 assigned).

```bash
HANDLE=<sandbox handle from nexus3 herdr list>
```

If no entry appears after ~30 s, the hook may not have fired. Force it:

```bash
nexus3 herdr worktree-sandbox "$SLICE_WS"
```

Then re-run `nexus3 herdr list` to confirm binding.

---

## Step 3 — confirm the sandbox is reachable

```bash
nexus3 exec "$HANDLE" -- bash -lc 'uname -r && pwd'
```

Check after: prints a kernel version and `/root` (or the WORKDIR if the image sets
one). A failure here (timeout, "sandbox not found") means the VM did not boot
cleanly — check `nexus3 sandbox logs "$HANDLE"` for the boot error before
continuing.

This work does not require nested virtualisation (it is about disk flush
consistency, not booting inner VMs), so skip the `/dev/kvm` check.

---

## Step 4 — confirm the worktree is mounted inside the guest

```bash
nexus3 exec "$HANDLE" -- bash -lc 'git -C /workspace status'
```

Check after: output is `On branch nexus3/nexus3-buildkit-poisoned-snapshot-on-vm-death`
with a clean tree. If it says "not a git repository", the virtiofs mount did not
come up — check host dmesg for virtiofsd errors and whether the sandbox binding
actually points to `$WORKTREE`.

---

## Step 5 — dispatch the agent

```bash
nexus3 herdr agent --autonomous --no-focus "$HANDLE" \
  "Motive: nexus3-buildkit-poisoned-snapshot-on-vm-death. \
Work on the nexus3 repo at /workspace (branch nexus3/nexus3-buildkit-poisoned-snapshot-on-vm-death, \
base develop). \
\
The motive charter is at $HOME/nexus3/.groundwork/motives/nexus3-buildkit-poisoned-snapshot-on-vm-death/motive.md — \
read it first; the open items (TBD-1, TBD-2, TBD-3) define the work. \
\
Key files: internal/core/agent/builder_role_linux.go (flush only on success path), \
internal/core/builder/cachedisk.go (cache disk management). \
\
Build rule: use 'make build' and 'make test' — never bare go commands; see /workspace/CLAUDE.md. \
The guest has no make/gcc by default: run 'apt-get install -y build-essential' first. \
\
Do not push unless the perimeter is configured to allow it. \
Push target is refs/heads/nexus3/nexus3-buildkit-poisoned-snapshot-on-vm-death only — \
the sandbox git perimeter allowlists refs/heads/nexus3/** exclusively. \
\
.groundwork/ is gitignored and not in the worktree; it is at its host absolute path \
$HOME/nexus3/.groundwork/ and is mounted into the guest at that path. \
\
Commit early and often with 'wip:' prefix. Report what you finish, what is half-done, \
and any premise in the motive you found wrong."
```

Check after: find the agent pane id:

```bash
herdr workspace show "$SLICE_WS"
```

Then read the pane to confirm the agent started (not an error or blank):

```bash
# substitute the pane-id from the workspace show output
herdr pane read <pane-id> --source recent-unwrapped --lines 30
```

Expected: the agent is reading the motive charter or responding to it — not a shell
prompt sitting idle, not an auth error, not a "command not found".

---

## Notes

- In-guest agents are not addressable by `herdr agent prompt`. Drive them through
  `herdr pane run <pane-id> "<text>"` and read with `herdr pane read`.
- The agent's `~/.claude` session lives on tmpfs inside the guest. A VM rebuild
  destroys it. Before any rebuild, send a wip-commit prompt via `herdr pane run`
  and wait for the sha to appear in `git -C "$WORKTREE" log` before touching the
  sandbox.
- There is already a `nexus3-buildkit-crash` worktree at workspace w68. That is a
  different slice. This new worktree is for the poisoned-snapshot motive specifically.
