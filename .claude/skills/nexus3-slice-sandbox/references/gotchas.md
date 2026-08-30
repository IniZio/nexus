# Traps, by symptom

Organised by what you actually see, because none of these read like their cause.
Each entry: the symptom, what is really happening, and what to do.

- [My change has no effect / a new flag "doesn't parse"](#stale-binary)
- ["already bound" but no such sandbox exists](#reused-workspace-ids)
- [Tool reports MISSING but is installed](#non-login-shell-probe)
- [Guest has no /dev/kvm despite --nested](#nested-dropped-at-the-supervisor)
- [Agent can't find the motive charter](#groundwork-not-in-the-worktree)
- [Agent used bare `go test`, or `-race` was off](#no-make-or-gcc-in-the-guest)
- [Workspace vanished](#rm-kills-the-workspace)
- [Workspace doesn't nest under the repo](#wrong-workspace-verb)
- [Two or three tabs per sandbox](#duplicate-panes)
- [`herdr agent prompt` says not an active named agent](#in-guest-agents-arent-addressable)
- [A test fails only in the full suite](#parallelism-flake-hiding-a-real-defect)
- [Subagent said "mutation-proven" but wasn't](#unverified-mutation-proofs)

---

## Stale binary

**Symptom:** you add a CLI flag, rebuild, and the flag behaves as if it does not
exist — a leading `--newflag` gets consumed as a positional argument, or a config
key is ignored. Re-reading the parser shows nothing wrong.

**Cause:** `make build` runs `go build -p N ./...`. Building multiple packages
type-checks and **discards the binaries**. The `./nexus3` file in the repo root is
a leftover from some earlier explicit build and can be arbitrarily old. The
natural move — `make build`, then copy `./nexus3` — silently installs stale code.
CLAUDE.md carries this rule for every session; it is repeated here because the
symptom presents as a parser bug, not a build problem.

**Do:**
```bash
go build -o nexus3 ./cmd/nexus3
cp nexus3 ~/.local/bin/nexus3.new && mv -f ~/.local/bin/nexus3.new ~/.local/bin/nexus3
```
A plain `cp` over the live path fails with `Text file busy` because running
supervisors execute that binary. The atomic rename leaves already-running
supervisors on their old inode, which is correct. `make vet` and `make test` are
still the right way to check and test.

---

## Reused workspace IDs

**Symptom:** `nexus3 herdr worktree-sandbox <ws>` refuses with `workspace <id>
already bound`, but `nexus3 ls` shows no such sandbox — or shows one belonging to
a different repo entirely.

**Cause:** herdr reuses workspace IDs after a workspace closes. nexus3's binding
store keys on `workspace_id` and never expires the record, so a brand-new
workspace inherits a dead one's binding.

**Do:** `nexus3 herdr list` and look up the id. If it points somewhere stale,
`herdr workspace close <id>` and re-open to get a fresh id.

---

## Non-login shell probe

**Symptom:** `nexus3 exec <h> -- sh -c 'command -v make cloud-hypervisor'` prints
nothing, suggesting a broken image. Reinstalling changes nothing.

**Cause:** the non-login shell has a different PATH; the tools live in
`/usr/local/bin` and `/root/.local/bin`.

**Do:** always `bash -lc` for guest probes. This one produces false negatives
that send you rebuilding images that were already correct.

---

## Nested dropped at the supervisor

**Symptom:** created with `--nested`, but the guest has no `/dev/kvm` and no
`vmx`/`svm`.

**Cause (historical, fixed):** every sandbox boots through the detached
`__supervisor`, which rebuilds its own `cloudhypervisor.Config`. `NestedVirt` was
absent from `supervisor.Config`, so it never crossed the argv/`spawn.json`
boundary. The driver then sends `CpusConfig.nested=false` explicitly — it must
never omit the field, since CH v53 treats an absent value as nested-ON.

**Generalise:** any CLI-side driver-config field is suspect until proven to cross
the supervisor boundary. The supervisor *reconstructs* config rather than
receiving it, so a field can be set perfectly on the CLI side and still never
reach the VM.

**Do:** check a live `~/.local/state/nexus3/supervisors/<id>/spawn.json` rather
than trusting the flag. Also check for `svm`, not just `vmx`, on AMD hosts.

---

## .groundwork not in the worktree

**Symptom:** an agent reports it cannot find the motive charter, or silently works
without it.

**Cause:** `.groundwork/` is gitignored with zero tracked files, so a linked
worktree never contains it. It is mounted into the sandbox at its host absolute
path, not under `/workspace`.

**Do:** point agents at `<main-repo-abs-path>/.groundwork/motives/<slug>/`, with
the path expanded — `git rev-parse --show-toplevel` on the host gives it.
If the mount is missing entirely, the sandbox predates the
`herdrWorktreeGroundworkMount` fix — recreate it.

---

## No make or gcc in the guest

**Symptom:** an agent's report mentions "manually-guarded go build/vet/test", or
two slices disagree about whether `-race` was on.

**Which image is the guest?** Worktree sandboxes build from the repo's
**`.nexus/Containerfile`**. `herdrResolveWorktreeImage`
(`internal/cli/cmd_herdr_plugin.go`) resolves in this order: a `nexus3.yaml`
anywhere up to the repo root wins; otherwise the presence of
`.nexus/Containerfile` alone is enough, since it is a complete build definition;
only with neither does it fall back to `--image <default>`, the minimal
`images/base/Containerfile`. Do not confuse the two — `images/base/Containerfile`
documents "NO gcc / build-essential" and ships none of the inner-VM substrate
(cloud-hypervisor, virtiofsd, buildkitd, Go) that a nexus3 worktree sandbox
plainly has. If your guest has cloud-hypervisor, it came from
`.nexus/Containerfile`.

**Cause:** `.nexus/Containerfile` did not install `build-essential` until
commit `2ff72fc`, and an image built before that has neither `make` nor `gcc`.
`make test` runs `-race`, which needs cgo and a C toolchain, so without them an
agent either apt-installs them itself or silently falls back to bare `go` — which
also drops the memory-safety guards described in CLAUDE.md. Two sandboxes built
from the same Containerfile then end up with different toolchains and validate
against different configurations. That divergence is the real damage: the
results are no longer comparable, and neither agent reports the discrepancy.

**Do:** `apt-get install -y build-essential` as the first instruction in the
brief. It takes effect immediately in a running VM, with no image rebuild.

---

## rm kills the workspace

**Symptom:** after `nexus3 sandbox rm`, `herdr worktree list --workspace <id>`
returns `workspace_not_found`.

**Cause:** every remaining pane is guest-backed, so they exit with the VM and the
workspace is destroyed with its last pane.

**Consequence:** "create workspace → remove the auto-provisioned sandbox →
recreate with different flags" cannot work. Change what the auto-provision reads
(the trusted-ref config) instead of trying to outrace or undo it.

---

## Wrong workspace verb

**Symptom:** the workspace does not nest under the repo in the herdr UI, though
its label looks right.

**Cause:** `herdr workspace create --cwd` makes a plain workspace with no worktree
association. `worktree-sandbox` renames it afterwards, so it *looks* correct in a
list while `is_linked_worktree` is unset and `SourceWorkspaceID` is empty — which
also changes how the auto-create predicates behave.

**Do:** `herdr worktree open --workspace <main-ws> --path <worktree>`, then
confirm `worktree.is_linked_worktree` is true in `herdr workspace list`.

---

## Duplicate panes

**Symptom:** a worktree sandbox has two or three tabs — an unused host shell, and
sometimes two "nexus3 guest shell" tabs where the agent is in the last one.

**Cause:** the worktree bind path opens a guest pane but does not close the root
host pane, unlike `space-create`'s fresh path which explicitly does. And
`space-create`'s reuse branch prints "reusing space" but opens a *new* guest pane
unconditionally, overwriting `GuestPaneID` and orphaning the previous pane — the
workspace is reused, the pane is not.

**Do:** on a current build this is fixed. On an older one, the agent is in the
pane the binding names (`nexus3 herdr list`), not necessarily the last tab.

---

## In-guest agents aren't addressable

**Symptom:** `herdr agent prompt <pane> "..."` → `agent_not_ready: <pane> is not
an active named agent`, even though the agent is plainly running there.

**Cause:** herdr detects the pane but cannot bind an agent identity across the VM
boundary. `pane report-agent` gives list visibility only.

**Do:** use the pane surface — `herdr pane run <pane> "<text>"` and `herdr pane
read <pane> --source recent-unwrapped`. Note text sent to a busy agent queues as
its next prompt.

---

## Parallelism flake hiding a real defect

**Symptom:** a test fails in the full suite and passes in isolation. Tempting to
call it flaky and move on.

**Cause seen here:** `exportAndUnpack` closed its pipe with the *unwrapped* error,
so the blocked writer got the same error back and both goroutines raced to the
errgroup's single error slot. Half the time the caller saw the error stripped of
its context. The flake was a real defect in error reporting, not a test problem.

**Do:** reproduce with `-count=400` under the same conditions before dismissing
it. A test that fails only under load is often reporting a genuine race.

---

## Unverified mutation proofs

**Symptom:** a subagent reports every guard mutation-proven; the tests pass.

**Reality:** twice in one session the reported-proven default-off guard had no
test at the layer that actually decided it. Flipping the flag default from false
to true left the entire package green — a supervisor spawned without the flag
would have booted with nested virt ON, with nothing to catch it.

**Do:** for anything security- or correctness-critical, re-run the mutation
yourself. Break the line, run the narrowed test, confirm RED, restore, confirm
GREEN. It costs a minute. Pay attention to *which layer* the existing test covers
— a codec test and a flag-default test are different hops.
