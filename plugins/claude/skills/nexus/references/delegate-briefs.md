# Brief authoring

A brief is what the in-guest agent reads cold. It has no session history and
no access to context outside the guest. Include everything it cannot discover.

## Standing orders

`delegate_agent_dispatch` prepends the block below to every brief it sends into
the guest, so briefs dispatched through the tool get it for free. When you
start an in-guest agent by hand (`nexus herdr agent ...`, `nexus shell`), paste
it at the top of the brief yourself. The canonical text lives in
`internal/mcp/delegate.go` (`standingOrders`); keep this copy identical.

```
STANDING ORDERS (nexus sandbox)
You are running inside a dedicated, isolated nexus microVM created for this task.
This VM is yours: you are root, it has its own kernel, disk and network, and CPU and
memory grow automatically under load. Use it fully.
The baseline for any work here is the project's full local stack running (e.g.
`docker compose up`) and tests executed against it. A mock, a stub, or a partial
setup is not acceptable evidence.
You are expected to unblock yourself: install tools and packages, pull images (use
a mirror if a registry denies you), fix env files, fix the code. Never stop at the
first obstacle and never ask the operator for something you can do yourself.
Record any friction you hit — what happened, the evidence, the workaround — in your
report so the platform can be fixed.
Egress is policy-gated. A 403 from the proxy names the policy that denied you:
report it, do not route around it.
Containers built or run by docker inside this VM already trust the sandbox TLS perimeter (CA at /etc/nexus/ca, SSL_CERT_FILE and friends pre-set); a 403 from a TLS-intercepted host is egress policy, not a certificate problem — report it, do not work around it.
```

Why it exists: in-guest agents dispatched without it tended to skip bringing up
the full stack, work around obstacles with mocks, or stop and ask for things
they could do themselves — they had no way to know the VM was theirs.

## Required content

- **Repo and branch** — where the worktree is checked out in the guest and which
  branch to commit and push to.
- **Task** — what to build, fix, or verify. One clear objective.
- **Acceptance criteria** — how the agent knows it is done. Observable, not
  subjective.
- **File scope** — which files to touch. Listing files the agent must NOT touch
  is as important as listing the ones it should.
- **Report file** — require the agent to write its narrative to a file (e.g.
  `REPORT.md`) before committing. Pane output may be truncated or hidden by the
  Claude UI; a file is always reachable via `git diff`.

## What to omit

Do not include paths or context that exist only on the host (e.g. host-absolute
paths to `.groundwork/`, host build instructions). The guest sees the worktree
contents and the in-guest environment only.

## Commit discipline

Tell the agent explicitly:

> Commit all changes in ONE commit. Prefix the message with `wip:` if the work
> is partial. The commit body must state what is finished, what is incomplete,
> and any premise from the brief that turned out to be wrong.

**Always require a commit, even for no-op outcomes.** The completion heuristic
(`delegate_agent_poll`) declares done when `git_log` is non-empty. If the agent
decides no change is needed and makes no commit, the heuristic runs for the
full 45-minute timeout before giving up, and the outcome is then
indistinguishable from a stuck agent. Require the brief to mandate: "If you
determine that no change is needed, commit a REPORT.md explaining why and
push it." A no-op with a commit is distinguishable; a no-op with no commit
is not.

## Minimal example

```
You are working in a nexus sandbox. The repo is checked out at /workspace
on branch feature/my-feature.

TASK: Add a null check to the Validate function in pkg/validator/validator.go
(around line 42) so it returns an error when input.Name is empty.

ACCEPTANCE CRITERIA:
- The function returns a non-nil error when input.Name == "".
- Existing tests still pass (run: make test or go test ./pkg/validator/...).

FILE SCOPE: touch only pkg/validator/validator.go and its test file.

When done, write a two-sentence summary to REPORT.md at the repo root, commit
everything to feature/my-feature, and push.
```

---

## nexus repo additions

These sections override or extend the template above for briefs sent into
nexus worktree sandboxes.

### Where the charter is

`.groundwork/` is gitignored and absent from the worktree. It is mounted at
its host absolute path. A brief saying "read `.groundwork/motives/<slug>/`"
points at nothing. Say `<main-repo-abs-path>/.groundwork/motives/<slug>/`
with the path expanded (`git rev-parse --show-toplevel`), and say explicitly
that it is NOT under `/workspace`.

### Build rule — state the reason

`go test ./...` has OOM-killed the whole login session in this repo. State
that, not just "use make" — an agent that understands the stakes will
install `make` when it is missing rather than fall back to bare `go`. Also
instruct `apt-get install -y build-essential` first if the image may predate
commit `2ff72fc` (no `make`/`gcc` in guest).

### Bans on shared-state mutation

No `git stash`, `git reset --hard`, touching `.groundwork/runs/` or the
journal. Parallel slices share git plumbing; an agent tidying up can destroy
a sibling's work.

### Branch ownership — state the reason

```
You own branch <name> in <worktree>. Commit there. Do not merge, push,
rebase, delete, or move any branch, and do not run any git command against
<main-repo-path> — a gate runs on your branch after you finish, and merging
before it is what your work is being protected from.
```

The reason is what makes it hold: an agent given "do not merge" as a
preference merged anyway at a commit still carrying gate findings.

### Testing bar

Ask for mutation proofs: break the code, show RED output, restore, show
GREEN, record both. Ask for hermetic tests where the mechanism allows, and
for an explicit statement where a live VM is genuinely required.

### Fail-closed rail

When a slice adds a check, guard, or validation, say:

> If this check cannot obtain what it needs to decide, it must refuse, not
> proceed. Wiring the missing input removes the refusal — never deleting the
> check. Prove the refusal by mutation like any other behaviour.

Also name the compat variant: an agent will add a skip-when-absent path "for
old records" without verifying any such record exists. Tell it to confirm
the legacy case is real before writing a path for it.

### Permission to contradict

End the brief with: "report anything you found that contradicts this brief —
a corrected premise is a valuable result, not a failure." Without this,
agents tend to work around a wrong premise silently.

### What to ask for in the report

Name explicitly:
- what changed, by file
- each mutation proof with its actual RED output
- the answer to any open question the slice was meant to resolve
- live-proof results with concrete values
- premises found to be wrong
- every input a guard can fail to obtain, and what it does when it cannot
- the final narrative written to `.slice-report.md` at the worktree root
  (gitignored; survives transcript scroll truncation; read with
  `cat <worktree>/.slice-report.md`)

Treat the report as a claim, not a result. Re-run at least one mutation per
branch yourself on a different field or code path.

### Checkpoint briefs

When a VM must be rebuilt under a running agent, ask for exactly one `wip:`
commit whose body records what is finished, half-written, about to do next,
and any wrong premise. Say plainly: do not tidy, do not finish anything, do
not run the full suite — capture state for a successor.
