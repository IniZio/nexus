# Writing a brief for an in-guest agent

A brief is the whole contract. The agent cannot ask a follow-up question, cannot
see your terminal, and cannot discover the environment's oddities before it has
already tripped on them. Everything it needs has to be in the text you send.

## What a brief must carry

**Where the charter is, as an absolute path.** `.groundwork/` is gitignored and
absent from the worktree; it is mounted at its host path. A brief that says "read
`.groundwork/motives/<slug>/motive.md`" points at nothing. Say
`<main-repo-abs-path>/.groundwork/motives/<slug>/` with the path expanded
(`git rev-parse --show-toplevel` gives it), and say explicitly that it is *not*
under `/workspace`.

**Environment facts it cannot infer.** Whether `/dev/kvm` is present. That
`make` and `gcc` need installing first. That `make build` writes no binary. These
are cheap to state and expensive to discover.

**The build rule, with its reason.** Bare `go test ./...` has OOM-killed the whole
login session here. Say that, not just "use make" — an agent that understands the
stakes will not improvise around the rule when `make` turns out to be missing;
it will install `make`.

**File ownership, when slices run concurrently.** Name the files this slice owns
and the files another slice owns. Then give it somewhere to put a cross-boundary
need: "if you need something in <other file>, record it in your ticket as a note
for slice N rather than making the change."

**What is deliberately out of scope**, with the reason. "Do not touch
`fork.go:483` — that is TBR-5, deferred" stops an agent from helpfully fixing
something that was left broken on purpose.

**Bans on shared-state mutation.** No `git stash`, no `git reset --hard`, no
touching `.groundwork/runs/` or the journal. Parallel slices have shared git
plumbing and an agent tidying up can destroy a sibling's work.

**Branch ownership, stated as a boundary rather than an instruction.** An agent
owns exactly one branch in exactly one worktree: it commits there and nowhere
else. Integration — merging, pushing, deleting or moving any ref, and touching
`develop` at all — belongs to the orchestrator, because it is the only party
that has seen the gate result.

Write this as a boundary, not as a step it should skip. "Do not merge into
develop and do not push" has been read as a preference and overridden: on
2026-08-30 an agent given exactly that line merged its branch into `develop`
anyway, at a commit that still carried three unfixed gate findings, then staged
a revert it never committed and left the main tree dirty. Nothing was lost, but
`develop` briefly shipped defects that a gate had already caught. Say instead:

> You own the branch `<name>` in `<worktree>`. Commit there. Do not merge, push,
> rebase, delete, or move any branch, and do not run any git command against
> `<main-repo-path>` — a gate runs on your branch after you finish, and merging
> before it is what your work is being protected from.

The reason is what makes it hold: an agent that knows a gate is coming has no
motive to integrate early.

**The testing bar, with the failure it defends against.** This repo's most
reliable bug shape is a checker sharing the broken mechanism it checks. Ask for
mutation proofs — break the code, show RED, restore, show GREEN, record both —
and say why. Ask for hermetic tests where the mechanism allows, and for an
explicit statement where a live VM is genuinely required, so you get a stated
limitation instead of a test that passes vacuously.

**A fail-closed rail on anything that guards.** When a slice adds a check, a
guard, an attestation or a validation, say that its degraded path must be the
loud one: a missing, zero, absent or unreadable input REFUSES, and never means
"skip the check". Ask for the refusal to be mutation-proven like any other
mechanism.

This is the single most repeated defect in this repo, and agents reintroduce it
while believing they are being careful — it arrives dressed as backward
compatibility, as best-effort, or as an unchecked error on a path that "cannot
really fail". Three separate instances landed in one session on 2026-08-30:

- a pid-reuse guard that treated a zero starttime as "skip", which is precisely
  the value that appears when the identity was lost — the one moment the guard
  existed for
- a `guestSync` that discarded the guest exit code, so a `sync` failing with EIO
  (the data-did-not-reach-the-host case) read as a clean flush and cleared the
  crash-consistency marker
- a supervisor handoff that confirmed and detached while transferring a payload
  its own doc comment described as incomplete

Each shipped reported as done. The shape to name in the brief:

> If this check cannot obtain what it needs to decide, it must refuse, not
> proceed. Wiring the missing input is what removes the refusal — never deleting
> the check. Prove the refusal by mutation like any other behaviour.

The compat variant is worth calling out by name, because it is the one with a
plausible-sounding excuse: an agent will add a skip-when-absent path "for old
records" without checking whether any such record exists. Tell it to verify that
the legacy case is real before writing a path for it.

**Permission to contradict you.** End with something like: "report anything you
found that contradicts this brief — a corrected premise is a valuable result,
not a failure." Briefs are written from the orchestrator's model of the code,
which is often wrong in exactly the places that matter. Without this line agents
tend to work around a wrong premise silently.

## What to ask for in the report

Name the outputs you need, or you will get a narrative:

- what changed, by file
- each mutation proof with its actual RED output
- the answer to whichever open question the slice was meant to resolve, with the
  reasoning and what was ruled out
- live-proof results with the concrete values (a `boot_id` before and after beats
  "the VM survived")
- premises found to be wrong
- every input the slice's guards can fail to obtain, and what each one does when
  it cannot — asking for this list surfaces a fail-open path that the narrative
  would have described as "handled"

Treat the report as a claim, not a result. Every gate run on 2026-08-30 — four
for four — found a real defect in work an agent had reported as complete and
mutation-proven, including proofs that did not bite. Re-run at least one
mutation per branch yourself, on a different field or code path than the one the
agent chose; an agent that mutated the one case its test covers has shown you
nothing about the other four.

## Checkpoint briefs

When a VM must be rebuilt under a running agent, send a checkpoint prompt first.
Ask for exactly one `wip:` commit whose body records what is finished, what is
half-written, what it was about to do next, and any wrong premise. Say plainly:
do not tidy, do not finish anything, do not run the full suite — capture state
accurately for a successor. An agent asked to "wrap up" will try to finish and
leave a worse mess than one asked to freeze.

## Tone

State constraints as constraints and reasons as reasons. Heavy shouting does not
raise compliance and crowds out the reasoning that lets an agent make a good call
when reality differs from the brief — which, in this repo, it usually does.
