# Analyst Notes — iteration-1

## Pass counts per run

| Eval | Run | Assertions | Pass | Fail |
|---|---|---|---|---|
| eval-0 dispatch-new-slice | with_skill | 7 | 5 | 2 |
| eval-0 dispatch-new-slice | without_skill | 7 | 6 | 1 |
| eval-1 diagnose-missing-make | with_skill | 5 + 1 extra | 4 | 2 |
| eval-1 diagnose-missing-make | without_skill | 5 + 1 extra | 5 | 1 |
| eval-2 diagnose-stale-binary | with_skill | 5 | 5 | 0 |
| eval-2 diagnose-stale-binary | without_skill | 5 | 2 | 3 |

## Non-discriminating assertions (passed in BOTH with_skill and without_skill)

- **eval-0**: assertions 1, 3, 4, 5, 6 — both runs correctly use `herdr worktree create`, verify `is_linked_worktree`, recognise auto-provision, point at absolute .groundwork path, and include build-essential in the brief.
- **eval-0**: assertion 7 (hardcoded /home/<user> paths) **FAILS in both** — both answers embed the user's real home directory either in the REPO variable assignment or in the agent brief / git command. Non-discriminating failure.
- **eval-1**: assertions 1, 2, 3 — both runs correctly say it is NOT ok, identify -race/cgo loss, and explain the guest image lacks the toolchain.
- **eval-2**: assertion 5 (does not send user back to re-read parser) — both pass.

## Where the skill actually won

- **eval-2** is the clearest win. with_skill: 5/5; without_skill: 2/5. The skill correctly names the repo-root `./nexus3` as the stale artifact, gives the exact `go build -o nexus3 ./cmd/nexus3` form, and covers the atomic-rename / "Text file busy" detail. without_skill identifies the correct root cause (make build writes no binary) but sends the user to install directly over their PATH binary, missing both the repo-root framing and the rename step.
- **eval-1 assertion 5** (recommend apt-get install -y build-essential): with_skill PASSES; without_skill FAILS. The skill adds the actionable one-liner fix; without_skill explains the cause and references a Containerfile commit but leaves the user to figure out the fix.

## Where without_skill beat with_skill

- **eval-0 assertion 2**: without_skill PASSES (derives SLICE_WS and CHECKOUT_PATH from command JSON); with_skill FAILS (hardcodes the worktree checkout path `$HOME/.herdr/worktrees/...` in step 4 instead of reading it from the create command's output).
- **eval-1 assertion 4** (toolchain divergence between sandboxes): without_skill PASSES; with_skill FAILS. The without_skill frames the risk as sandboxes at different image versions producing different test configurations. The with_skill stays within a single-sandbox framing and never names cross-sandbox inconsistency.
- **eval-1 extra** (correctly identifies .nexus/Containerfile): without_skill PASSES; with_skill FAILS. The with_skill answer asserts "boots from `images/base/Containerfile`" — this is factually wrong. images/base/Containerfile is the minimal base image that explicitly omits gcc and build-essential; worktree sandboxes build from .nexus/Containerfile. The without_skill correctly names .nexus/Containerfile.

## Skill-internal detail leaking to end users

- **eval-2 with_skill** references `references/gotchas.md` by name: "This is documented in `references/gotchas.md` under 'Stale binary'." This is a skill-internal reference file — the user has no such file, and citing it in a user-facing answer is confusing and unhelpful.

## Three most important observations

1. **eval-2 is the skill's strongest signal.** without_skill gets only 2/5 on a concrete diagnostic question where the skill has specific institutional knowledge (repo-root ./nexus3 as the stale artifact, atomic-rename for Text-file-busy). The delta is large and the skill's answer is clearly better.

2. **with_skill has a factual error in eval-1 that without_skill avoids.** Naming `images/base/Containerfile` as the worktree sandbox's guest image is wrong, and it compounds the error by treating it as load-bearing for the rest of the explanation (the image "explicitly excludes" the toolchain — which is true, but for the wrong Containerfile). without_skill correctly traces the cause to `.nexus/Containerfile` and even cites the commit that fixed it. The skill is giving the wrong mental model here.

3. **Both runs fail the hardcoded-path assertion in eval-0.** The skill does not prevent the agent from embedding `$HOME/...` paths in user-typed commands and agent briefs. This is a portability defect in both — but the with_skill version is worse (hardcodes the checkout path AND the .groundwork path in the agent brief, while without_skill at least uses a variable for the downstream paths and only hardcodes REPO).
