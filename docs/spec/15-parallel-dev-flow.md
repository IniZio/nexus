# 15 — Parallel Development Flow

*Purpose: normative description of the motive-scoped parallel development flow — N sandboxes of
one project, each producing distinct commits in-guest, work extracted to the host, branches
pushed, PRs opened, and a preview artifact published for remote reviewers. Evidence citations
trace to T0 (walkthrough session, 2026-08-15), T1 (artifact delivery spike, 2026-08-15), and the
`nexus3-parallel-dev-pr-flow` motive charter.*

*Accuracy rule: every normative claim here names the decision ID or evidence source that grounds
it. Where an item has not been built or proven, it is marked **NOT YET BUILT** or
**UNPROVEN**.*

### How to verify citations

Code citations use the convention `file.go:NNN` where NNN is a convenience hint. To re-check:

```shell
codegraph explore "<SymbolName>"
# or
grep -n "^func.*SymbolName" internal/path/to/file.go
```

---

## 1 — Overview

The flow has five stages. Each stage names its current status (proven / proposed / not yet built)
so the document cannot be mistaken for a fully-implemented system.

| Stage | Description | Status |
|---|---|---|
| **Seed** | Host creates N sandboxes; each receives a depth-1 shallow clone of the working tree via `file://`; per-sandbox git identity configured | Shallow clone proven (T0, D-PD-19); per-sandbox identity **NOT YET BUILT** (slice G1) |
| **Work** | Agent or operator works inside each sandbox; commits accumulate locally (in-guest) | Guest commits proven (P4 milestone) |
| **Extract** | Host pulls an incremental `git bundle` from each sandbox via the existing agent copy channel | Bundle transport proven in T0 (468-byte incremental bundle, `git bundle verify` confirmed) |
| **Push** | Host applies bundle to a worktree, pushes branch using host's own credential, opens PR via `gh` | Host-side push proven (T0: real PR #832, account IniZio, SSH, repo scope); `nexus3 pr` command **NOT YET BUILT** (slice P1) |
| **Artifact** | Host runs `nexus3 preview build` inside each sandbox, harvests declared outputs, publishes pre-release on GitHub | **NOT YET BUILT** (slices A1, A2) |

---

## 2 — The load-bearing security property (D-PD-01)

**Decision D-PD-01** (motive charter): *the guest produces commits only; pushing happens on the
host.* `github.com` is **never** added to `AllowedHosts`. An in-guest `git push` fails closed
by the default-deny posture.

This is the decision that makes all others structurally safe. The alternatives were weighed
explicitly in the charter notes:

1. **MITM placeholder-swap for `github.com`** — rejected because git's HTTP transport
   authenticates with `Authorization: Basic`, not `Bearer`; `proxy.go`'s existing swap does not
   apply unmodified (`internal/core/perimeter/mitm/proxy.go:67`). Also widens egress on every
   PR-capable sandbox.
2. **ssh-agent forwarding over vsock** — rejected: grants unbounded signing authority to any
   process in the guest for the agent's lifetime, with no host-side per-request policy.
3. **Short-lived GitHub App installation tokens (1 h TTL)** brokered by the existing perimeter —
   retained as the future path (TBR-PD-3 in the charter), but requires standing up a GitHub App
   and a minting path (a milestone of its own) and still widens egress.

`AllowedHosts` is defined at `internal/core/service/create.go:124`. The agent-egress path sets
it to `AgentEgressHosts()` (`internal/core/service/create.go:246`), which contains
`api.anthropic.com` and `platform.claude.com` and nothing else. No product code path adds
`github.com` today; a standing regression test introduced in slice G1 asserts this (REQ-PDF-020,
N-AC1).

---

## 3 — Git ancestry at seed time (D-PD-19)

**Decision D-PD-19** (decided 2026-08-15, T0 walkthrough): *git ancestry is injected at sandbox
seed time as a depth-1 shallow clone via the `file://` protocol. The shallow boundary SHA is
recorded as `BaseRef`.*

### Why `file://` and not `--local`

`git clone --local --depth 1 <path>` silently ignores `--depth`; it performs a full hard-link
clone with no pack negotiation, so the depth constraint is a no-op. This is a `git`
implementation detail, not a nexus3 bug, but it is a trap for any operator or future maintainer
who reaches for `--local` on the assumption it honours depth.

`git clone --depth 1 file://<path>` forces the pack-protocol path even for a local source, so
`--depth` is honoured.

### Measured sizes (example-app, 4 173 commits, 3 216 tracked files)

| Clone form | `.git` directory size | Notes |
|---|---|---|
| `--depth 1 file://` | 89 MB | Proven in T0 |
| Full clone | 1.6 GB | Measured in T0 |

### Bundle proof (T0, 2026-08-15)

With a depth-1 seed as `BaseRef`, an in-guest commit yields a 468-byte incremental bundle that
`git bundle verify` accepts on the host:

```
/tmp/proof-incremental.bundle is okay
The bundle contains this ref:
  e9df809020606498223679fbea9e64eaa1abc767 HEAD
The bundle requires this ref:
  a97112551edaec50de70d95e9a64273816c756f5   ← BaseRef (== merge-base with origin/main)
The bundle uses this hash algorithm: sha1
```

This proves the shallow boundary is sufficient for incremental export. It does not prove anything
about what happens when a sandbox diverges by more than one commit — the multi-commit case is not
yet tested.

### What is NOT YET BUILT

The code that injects a shallow clone at seed time does not exist. Slice G1 will add
`internal/core/service/git_identity.go` (new file) and wire it into `service.CreateAndBoot` at
seed time. Until G1 lands, sandboxes receive the full working-tree capture described in doc 09
(the worktree-disk capture, `nexus3 harvest`), not a shallow clone.

`BaseRef` as a tracked field on the `Sandbox` record does not exist in `internal/core/domain`
today. G1 must add it. References to `BaseRef` in this document are forward-references to G1's
design.

---

## 4 — In-guest git identity (D-PD-02)

**Decision D-PD-02** (motive charter): *in-guest git identity is a deterministic bot identity
configured per sandbox at seed time, with a `Co-authored-by` trailer naming the operator. The
operator's personal git credentials are never injected into a guest.*

**NOT YET BUILT** (slice G1). No code today sets a per-sandbox `user.name` or `user.email`. A
guest that commits will use whatever identity the base image ships with (currently empty, which
causes git to error). G1 will set a synthetic identity of the form `nexus3-bot/<sandbox-id>`
at seed time.

---

## 5 — Branch naming convention (D-PD-03)

**Decision D-PD-03** (motive charter): *branch names are derived, not chosen:*
`nexus3/<motive-slug>/<sandbox-short-id>`. The host refuses to push any branch not matching
this pattern, making host-side push safe by construction.

**NOT YET BUILT** (slice P1). The `nexus3 pr` command that enforces this check does not exist
today. The branch-pattern enforcement is a property of the push command, not of the git identity
(which is seeded earlier by G1).

---

## 6 — Work extraction — the git bundle transport

Work leaves the guest as a `git bundle` over the existing agent copy channel (`nexus3 cp`, doc
09). The host-side flow is:

1. `nexus3 cp <sandbox>:/repo/<project>/.git/…` to a host-side scratch worktree — **NOT YET
   BUILT** as an automated command; done manually in T0 walkthrough.
2. `git bundle create <outfile> <BaseRef>..HEAD` inside the guest — proven in T0.
3. `git bundle unbundle` on the host — proven in T0.
4. `git push origin <branch>` using the host's own credential — proven in T0 (PR #832, SSH).
5. `gh pr create` — proven in T0 (draft PR opened and closed).

The `nexus3 pr` command (slice P1) will automate steps 1–5 for every sandbox under a motive.
`nexus3 pr` does not exist today; RE: P1-AC1, REQ-PDF-021.

**Idempotency** (D-PD-12): re-running `nexus3 pr` against a sandbox whose PR already exists must
update the branch and body rather than opening a duplicate (P1-AC2, REQ-PDF-022). **NOT YET
BUILT.**

---

## 7 — Reviewer artifact (D-PD-04, D-PD-05)

### What the artifact is (D-PD-04)

**Decision D-PD-04** (motive charter): *the reviewer artifact is a ZIP of declared build outputs
plus `MANIFEST.json` plus `run.sh`. VM snapshots are explicitly not the artifact* — they are
hypervisor-version-tied host directories, unusable off-host (`internal/core/service/snapshot.go`,
`Snapshot` type).

The declared outputs are specified per project in `.nexus/preview.yaml` (D-PD-05). The schema
is not yet standardised; slice A1 will define it and validate it. The artifact is built inside
the sandbox (not on the host), so it benefits from the sandbox's installed toolchain.

**NOT YET BUILT**: `nexus3 preview build` (A1-AC2, REQ-PDF-025) and `.nexus/preview.yaml`
schema validation (A1-AC1, REQ-PDF-024) do not exist today.

### Measured artifact size (T1 spike, 2026-08-15)

For `example-app` (Next.js + Go API), the spike measured the build outputs at **4.5 MB** against
GitHub's 2 GiB per-asset limit. This is not a delivery risk.

### Delivery channel (D-PD-04, revised 2026-08-15)

**Published pre-release** on GitHub Releases, not a draft.

The channel decision was driven by a concrete failure: GitHub's `/releases/download/` URL returns
HTTP 404 for draft releases even with a valid token. A reviewer with read-level access cannot
construct a working download link from a draft, and cannot see the draft in the UI. This was
**empirically confirmed** in the T1 spike.

Published pre-releases are visible to anyone with read-level repository access. The tag namespace
is `preview/{motive-slug}-{sandbox-id}` (e.g. `preview/pdev-sbx-abc123`). Forward slashes in
git tags are valid on GitHub. Cleanup is automated: `gh release list --json tagName | jq 'map(select(.tagName | startswith("preview/")))' | xargs` and delete (slice A2).

**Open item OI-1**: the claim that a published pre-release `/releases/download/` URL succeeds for
a read-level reviewer is *docs-inferred*, not empirically tested. The T1 spike confirmed the 404
failure for drafts [E] and verified that GitHub's documentation says read-level access is
sufficient for published releases [D], but a real end-to-end reviewer download from outside the
repo was not performed. This is unproven until slice X0 exercises it with a real external
collaborator.

**NOT YET BUILT**: the publish step (A2-AC1), the `run.sh` generator, and the cleanup routine
(A2-AC2) do not exist today.

---

## 8 — Concurrent same-project sandboxes

Slice B1 landed a fix (2026-08-15, live on KVM) that makes shadow disks per-sandbox rather than
per-project-name. Before B1, two sandboxes of `example-app` collided on a shared shadow-disk host
path (hard-coded in `internal/cli/shadowdisk.go`), and the second sandbox failed at boot with
`Failed to get Write lock for disk image`. T0 walkthrough scenario 4 documented the failure;
scenario 8 showed sequential-only viability.

After B1: concurrent same-project sandboxes boot and run independently. The shadow disk path
includes the sandbox ID.

---

## 9 — What is proven, what is not

This section consolidates the status across the whole flow.

### Proven (cited source in parentheses)

| Claim | Source |
|---|---|
| Host-side git push works: real PR #832 opened via SSH, account IniZio | T0, 2026-08-15 |
| In-guest commit with a local `git commit` completes; commit is visible on host | P4 milestone, proven earlier |
| `git clone --depth 1 file://<path>` produces an 89 MB clone vs 1.6 GB full | T0, 2026-08-15 |
| `git clone --local --depth 1` silently ignores `--depth` | T0, 2026-08-15 |
| 468-byte incremental bundle passes `git bundle verify` and applies on host | T0, 2026-08-15 |
| Draft release `/releases/download/` URL returns 404 even with valid token | T1 spike, 2026-08-15 |
| GitHub published pre-release visible to read-only collaborators | T1 spike [D] |
| Concurrent same-project sandboxes (shadow disk per-sandbox) | B1 slice, 2026-08-15 |
| Workspace auto-mount at boot | B2 slice, 2026-08-15 |
| Artifact size for `example-app` build: 4.5 MB | T1 spike, 2026-08-15 |

### NOT YET BUILT or UNPROVEN

| Claim | Missing piece |
|---|---|
| Per-sandbox git bot identity at seed time | G1 (`git_identity.go`) |
| Shallow clone injected at sandbox creation | G1 (wire-in to `service.CreateAndBoot`) |
| `BaseRef` field on `domain.Sandbox` | G1 |
| `nexus3 pr` command (extract + push + open PR) | P1 (`cmd_pr.go`, new) |
| Idempotent PR update (re-run updates, no duplicate) | P1 |
| Branch-pattern enforcement on host push | P1 |
| `.nexus/preview.yaml` schema | A1 |
| `nexus3 preview build` command | A1 |
| Published pre-release download works for real external reviewer | X0 (end-to-end proof) |
| N-sandbox → N-PR → N-artifact full pipeline | X0 |
| `run.sh` generator and `MANIFEST.json` schema | A1 |
| Artifact cleanup routine | A2 |

### Open contradiction: stale run-dir sockets

T0 observed stale socket/iid files in the run directory for record-less sandboxes (3 entries with
no corresponding store record). The resource lifecycle spec (doc 18 / slice R2) documents this as
correct-by-design: the reaper is responsible for cleaning run-dir files, not the create or remove
paths. T0 observed these sockets on a host with no running or paused sandboxes, meaning they
belong to previously-crashed sandboxes whose records were deleted without cleaning the run dir.

The tension is between R2's "correct-by-design" position (sockets are reaper-eligible) and the
observation that no reaper has run on this host. The reaper command (`nexus3 reap`) is **NOT YET
BUILT** (slice R3). Until it lands, stale sockets accumulate. This is not a contradiction in the
evidence — it is a gap documented as REQ-RES-010 (N-AC2) and REQ-RES-011 (N-AC3).

---

*Sources: motive charter `.groundwork/motives/nexus3-parallel-dev-pr-flow/motive.md` (decisions
D-PD-01–D-PD-05, D-PD-12, D-PD-19, TBR-PD-3, ACs N-AC1, P1-AC1–AC4, A1-AC1–AC3); T0
walkthrough `docs/notes/2026-08-parallel-dev-walkthrough.md`; T1 spike
`docs/integrations/pr-preview-artifact.md`; resource lifecycle doc 18; resource inventory audit
`docs/notes/2026-08-resource-inventory-audit.md`.*
