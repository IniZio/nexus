# Writing a brief for an in-guest agent

For the generic brief structure (task, acceptance criteria, file scope, commit
discipline, report file), see:
`plugins/claude/skills/nexus3/references/delegate-briefs.md`

The sections below are nexus3-repo additions that override or extend that
template.

## Where the charter is

`.groundwork/` is gitignored and absent from the worktree. It is mounted at its
host absolute path. A brief that says "read `.groundwork/motives/<slug>/`" points
at nothing. Say `<main-repo-abs-path>/.groundwork/motives/<slug>/` with the path
expanded (`git rev-parse --show-toplevel` gives it), and say explicitly that it
is NOT under `/workspace`.

## The build rule, with its reason

Bare `go test ./...` has OOM-killed the whole login session here. Say that, not
just "use make" — an agent that understands the stakes will not improvise around
the rule when `make` turns out to be missing; it will install `make`. Also tell
the agent to `apt-get install -y build-essential` first if the image may predate
commit `2ff72fc` (no `make`/`gcc` in guest).

## Bans on shared-state mutation

No `git stash`, `git reset --hard`, touching `.groundwork/runs/` or the journal.
Parallel slices share git plumbing; an agent tidying up can destroy a sibling's work.

## Branch ownership

State this as a boundary, not a step to skip:

> You own the branch `<name>` in `<worktree>`. Commit there. Do not merge, push,
> rebase, delete, or move any branch, and do not run any git command against
> `<main-repo-path>` — a gate runs on your branch after you finish, and merging
> before it is what your work is being protected from.

The reason is what makes it hold. On 2026-08-30 an agent given "do not merge"
as a preference merged anyway at a commit that still carried unfixed gate
findings, then staged a revert it never committed. State the why.

## Testing bar

Ask for mutation proofs: break the code, show RED output, restore, show GREEN,
record both. Ask for hermetic tests where the mechanism allows, and for an
explicit statement where a live VM is genuinely required, so you get a stated
limitation rather than a test that passes vacuously.

## Fail-closed rail

This is the single most repeated defect in this repo. When a slice adds a check,
guard, attestation, or validation, say:

> If this check cannot obtain what it needs to decide, it must refuse, not
> proceed. Wiring the missing input is what removes the refusal — never deleting
> the check. Prove the refusal by mutation like any other behaviour.

Also name the compat variant: an agent will add a skip-when-absent path "for
old records" without checking whether any such record exists. Tell it to verify
the legacy case is real before writing a path for it.

Three instances of this shipped in one session on 2026-08-30: a pid-reuse guard
that treated zero starttime as "skip", a `guestSync` that discarded the exit
code, and a handoff that confirmed while transferring an incomplete payload.

## Permission to contradict

End the brief with something like: "report anything you found that contradicts
this brief — a corrected premise is a valuable result, not a failure." Without
this, agents tend to work around a wrong premise silently.

## What to ask for in the report

Name the outputs explicitly:

- what changed, by file
- each mutation proof with its actual RED output
- the answer to any open question the slice was meant to resolve
- live-proof results with concrete values (a `boot_id` before and after beats "the VM survived")
- premises found to be wrong
- every input the slice's guards can fail to obtain, and what each does when it cannot
- the final report written to `.slice-report.md` at the worktree root (gitignored)

`.slice-report.md` survives the Claude UI's in-transcript scroll truncation.
Read it with `cat <worktree>/.slice-report.md`. It is gitignored so it cannot
reach a commit by accident.

Treat the report as a claim, not a result. Re-run at least one mutation per
branch yourself, on a different field or code path than the one the agent chose.

## Checkpoint briefs

When a VM must be rebuilt under a running agent, ask for exactly one `wip:`
commit whose body records what is finished, half-written, about to do next, and
any wrong premise. Say plainly: do not tidy, do not finish anything, do not run
the full suite — capture state for a successor. An agent asked to "wrap up" will
try to finish and leave a worse mess.
