# Developing nexus inside nexus

Each unit of nexus work gets its own git worktree, herdr workspace, and VM
with an in-guest Claude agent. Most failures arise because the host side
succeeded while the guest silently got something else — verify inside the
guest, not from host intentions.

## Variables

Derive at the start; use everywhere including commands handed to a person:

```bash
REPO=$(git rev-parse --show-toplevel)   # main checkout
MAIN_WS=$HERDR_WORKSPACE_ID             # or from `herdr workspace list`
```

A recipe baked to one machine's `/home/<user>/...` works exactly once.

## Worktree and workspace

**Preferred:** `delegate_worktree_create` from `references/delegate-loop.md`
handles create, boot, and ID plumbing in one call.

**CLI path:**

```bash
herdr worktree create --workspace "$MAIN_WS" --branch nexus/<name> --base develop --no-focus
```

Read workspace id and path from the JSON response — do not predict them.

**Do NOT use `herdr workspace create --cwd`** — it makes a plain workspace
with no worktree association. `is_linked_worktree` is unset and
`SourceWorkspaceID` is empty, which changes auto-create predicates. Verify:

```bash
herdr workspace list   # must show worktree.is_linked_worktree = true
```

## Nested virtualisation

Required when work must be proven against a real booted VM (supervisor
lifecycle, adoption, egress perimeter). Without it an agent silently
downgrades to hermetic tests.

**Channel that works:** set `sandbox.nested: true` in the worktree's
`.nexus/config.yaml` before creating the workspace — this is what the
auto-provision hook reads. Do NOT rely on `--nested` on
`nexus herdr worktree-sandbox`: it loses the auto-provision race (see
[Sandbox auto-provision race](#sandbox-auto-provision-race)).

Confirm in the guest (both Intel `vmx` and AMD `svm`):

```bash
nexus exec <handle> -- bash -lc 'ls -l /dev/kvm; grep -m1 ^flags /proc/cpuinfo | tr " " "\n" | grep -E "^(vmx|svm)$"'
```

## Monitoring the pane

**Primary signal: herdr agent state.**
`delegate_agent_poll` returns `agent_status` (`idle|working|blocked|done|unknown`)
and `state_change_seq` from `herdr agent get` on the guest pane. Re-poll when
`state_change_seq` changes — it increments on every state transition. A settle
guard (~1.5 s, up to 3 re-polls) prevents a transient `done` from masking a
following `blocked`; poll returns `settled: false` when the status kept moving.

Poll loop:
- **`working`** — leave it; re-poll in 30 s.
- **`blocked`** — read `question` from the poll response; answer in the pane
  (or surface to the human); re-poll immediately.
- **`done` / `idle`** — check marker and git signals (see `delegate-loop.md § 3`);
  these decide completion, not `agent_status` alone.
- **`unknown`** — herdr cannot read the state (`agent_state_reason` explains why);
  fall back to the screen/movement heuristics below.

**Fallback: screen/movement heuristics (use when `agent_status` is `unknown`).**

Dispatch an agent and start a watcher in the same turn:

```bash
scripts/watch-pane.sh <pane-id>      # run_in_background: true
```

A working agent repaints every second (spinner, elapsed timer). A stopped agent
renders a static pane. Movement holds across layout changes; marker strings do not.

When the watcher fires on a question, answer through the pane surface, then
restart the watcher — it exits on each stop.

**Six documented false-stop modes** (screen/movement fallback only):

1. **Slash-command overlay** — autocomplete footer drops `esc to interrupt`;
   a working agent reads as idle.
2. **Agent blocked on background subagent** — footer loses `esc to interrupt`;
   working marker moves to the body.
3. **Duplicated working definition** — start-grace loop held an inlined copy
   that diverged from `sample_state`. One definition, called by every loop.
4. **Background-agent roster format change** — roster switched from
   `Waiting for N background agents` to a live subagent row, matching nothing.
5. **Pane scrolled up** — spinner is above the visible window. Check
   `herdr pane get <pane-id>` → `scroll.offset_from_bottom`; non-zero means
   WORKING. `esc to interrupt` present also proves WORKING regardless of scroll.
6. **Claude app withholds content** — terminal at bottom but app shows
   `N new messages (ctrl+End) ↓`. Read the diff directly:
   `git -C <worktree> diff develop...HEAD`. Require agents to write reports
   to `.slice-report.md` (gitignored) so it is always reachable.

Bias every ambiguous case toward WORKING. Strings are the question
discriminator only, applied after the pane settles.

## Checkpoint before rebuild

The in-guest `~/.claude` upperdir is tmpfs — a sandbox rebuild destroys the
agent's session. Before recreating:

```bash
herdr pane run <pane-id> "Commit everything to <branch> now as ONE commit
prefixed 'wip:' whose body states what is finished, what is half-written,
what you were about to do next, and any wrong premise from the brief.
Do not tidy, do not finish anything. Reply with the sha."
```

Wait on the commit, not the pane:

```bash
until [ -n "$(git -C <worktree> log --oneline develop..HEAD)" ]; do sleep 10; done
```

## Verify in-guest

For MITM CA, git ownership, Go PATH, and the `go test` skip trap, see
`references/guest-setup.md`.

**nexus-specific habits:**

**Login shell for all guest probes.** `nexus exec <h> -- sh -c 'command -v make'`
reports missing for installed tools. Always use `bash -lc`.

**In-guest `ok` can mean zero tests ran.** `internal/cli`,
`internal/core/service`, `internal/core/recovery`,
`internal/core/perimeter/netstack`, and `internal/mcp` exit 0 when
`/proc/1/comm == "nexus-agent"`. The skip line goes to stderr; `go test`
suppresses it for passing packages. Gate on the host worktree, or use
`unshare --pid --mount-proc --fork` in-guest for pure-filesystem packages.

**Read the real exit code, never through a pipe.** `make test | grep FAIL`
reports grep's status. Use `${PIPESTATUS[0]}` or redirect: `make test > log 2>&1; echo $?`.

**Re-run mutation proofs yourself** on security- or correctness-critical code.
Break the line, confirm RED, restore, confirm GREEN. An agent's
"mutation-proven" is a claim, not evidence.

## Reclaim

**Completion convention: merged to `develop`.** Verify against git, not
the agent's report:

```bash
git -C "$WORKTREE" rev-list --count develop..HEAD   # must be 0
git -C "$WORKTREE" status --porcelain               # must be empty
git -C "$WORKTREE" ls-files --others --exclude-standard  # must be empty
```

For the generic teardown sequence, see `references/delegate-loop.md § 5. Reclaim`.

**Order matters:** `nexus rm` closes the herdr workspace as a side effect.
Running it first leaves `herdr worktree remove` with `workspace_not_found`:

```bash
herdr worktree remove --workspace <ws-id>   # first
nexus rm <handle>                          # then
git -C "$REPO" branch -d nexus/<name>      # -d, never -D
```

`git branch -d` refuses an unmerged branch — an independent check the
reclaim is safe. A `-d` failure means work is about to be lost.

## Sequencing slices

Parallel fan-out is only correct for slices with disjoint files AND no API
dependency. Check for a consumes-relationship, not just file overlap. State
each slice's file ownership in its brief; tell a slice that needs something
outside its territory to record the requirement for the owning slice rather
than reaching across.

---

## Traps

### Sandbox auto-provision race

Creating a workspace fires an auto-provision hook that binds a sandbox.
A concurrent explicit call reports `already bound (concurrent create race),
reusing existing sandbox`. "Create workspace → remove sandbox → recreate
with different flags" is impossible: `nexus sandbox rm` closes the herdr
workspace as a side effect, destroying it. Change what the auto-provision
reads (`.nexus/config.yaml`) before creating the workspace.

### Reused workspace IDs

`nexus herdr worktree-sandbox <ws>` refuses with `already bound`, but
`nexus ls` shows nothing or a sandbox for a different repo. herdr reuses
workspace IDs after close; nexus's binding store never expires them.
Run `nexus herdr list`, look up the id. If stale, `herdr workspace close <id>`
and re-open to get a fresh id.

### Non-login shell probe

`nexus exec <h> -- sh -c 'command -v make'` prints nothing for installed
tools. Tools live in `/usr/local/bin` and `/root/.local/bin` — non-login
shell PATH differs. Always use `bash -lc`.

### Nested dropped at the supervisor

Guest has no `/dev/kvm` despite `--nested`. The detached supervisor
reconstructs `cloudhypervisor.Config` from `spawn.json`; a CLI-side flag
is suspect until proven to cross that boundary. Check
`~/.local/state/nexus/supervisors/<id>/spawn.json`. Also check for `svm`,
not just `vmx`, on AMD hosts.

### .groundwork not in the worktree

`.groundwork/` is gitignored with zero tracked files; a linked worktree
never contains it. It is mounted at its host absolute path.  
Point agents at `<main-repo-abs-path>/.groundwork/motives/<slug>/` with
the path expanded. If the mount is missing entirely the sandbox predates the
`herdrWorktreeGroundworkMount` fix — recreate it.

### No make or gcc in the guest

`.nexus/Containerfile` did not install `build-essential` until commit
`2ff72fc`. Without it, `-race` silently falls off. Put
`apt-get install -y build-essential` first in the brief; it takes effect
immediately without an image rebuild.

### rm kills the workspace

Every remaining pane is guest-backed; they exit with the VM and the
workspace dies. "Create → remove auto-provisioned sandbox → recreate with
different flags" cannot work. Change `.nexus/config.yaml` before creating.

### Re-bake does not update running sandboxes

The guest rootfs is baked into the ext4 image at create time. A running
sandbox retains the rootfs it booted from; no subsequent rebuild touches it.
`sandbox agent-upgrade` hot-swaps only `nexus-agent`. After a re-bake,
create a fresh sandbox. Compare build digests from the two `create`
invocations to confirm the rebuild produced a new image.

### How to identify the binary a guest is running

Version strings are unreliable (shell wrappers can answer). Resolve to an
absolute path and hash:

```bash
readlink -f $(command -v claude)
sha256sum $(readlink -f $(command -v claude))
sha256sum /proc/1/exe          # agent process
sha256sum ~/.local/bin/nexus-agent   # compare against host
```

The builder image filename embeds a fingerprint of the injected Alpine
toolchain package set and the first 16 hex of the agent binary's sha256
(`nexus-builder-...-tc<hash8>-agent<hash16>.ext4`); changing either re-bakes.

### Static binary trap (builder VM kernel panic)

A bare `go build -o nexus-agent ./cmd/nexus-agent` produces a
glibc-linked binary. The Alpine builder carries musl only; the kernel
panics trying to exec it as PID 1. Always use `make install-agent`
(sets `CGO_ENABLED=0`):

```bash
make install-agent
ldd ~/.local/bin/nexus-agent   # must print "not a dynamic executable"
```

### Unverified mutation proofs

An agent reporting "mutation-proven" has been wrong twice in this repo — the
flipped default left the entire package green. For security- or
correctness-critical code, re-run the mutation yourself at a different field
or code path. Break the line, confirm RED, restore, confirm GREEN.

### Finished sandboxes never reaped

Both reapers key on herdr pane/workspace liveness, not on whether the work
is done. A sandbox whose slice merged but whose workspace is still open is
invisible to both. Guest RAM is memfd-backed — five idle finished sandboxes
cost as much as five busy ones. Reclaim explicitly (step above).
`space-prune --apply` will reap sandboxes with live, dirty worktrees — do
not use it as a substitute for proper reclaim.
