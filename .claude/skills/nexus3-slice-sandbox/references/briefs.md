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

**The testing bar, with the failure it defends against.** This repo's most
reliable bug shape is a checker sharing the broken mechanism it checks. Ask for
mutation proofs — break the code, show RED, restore, show GREEN, record both —
and say why. Ask for hermetic tests where the mechanism allows, and for an
explicit statement where a live VM is genuinely required, so you get a stated
limitation instead of a test that passes vacuously.

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
