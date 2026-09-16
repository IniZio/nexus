# Brief authoring

A brief is what the in-guest agent reads cold. It has no session history and
no access to context outside the guest. Include everything it cannot discover.

## Standing orders

`delegate_agent_dispatch` prepends the block below to every brief it sends into
the guest, so briefs dispatched through the tool get it for free. When you
start an in-guest agent by hand (`nexus3 herdr agent ...`, `nexus3 shell`), paste
it at the top of the brief yourself. The canonical text lives in
`internal/mcp/delegate.go` (`standingOrders`); keep this copy identical.

```
STANDING ORDERS (nexus3 sandbox)
You are running inside a dedicated, isolated nexus3 microVM created for this task.
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
You are working in a nexus3 sandbox. The repo is checked out at /workspace
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
