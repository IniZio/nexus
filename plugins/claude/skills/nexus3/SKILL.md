---
name: nexus3
description: >
  Load for anything involving nexus3 microVM sandboxes. (1) Sandbox operations:
  create, start, stop, pause, resume, rm, shell, exec, snapshot, fork, volume,
  mount (--mount or --mount-named), agent-in-sandbox, PR from inside a sandbox,
  projecting host config (credentials, git config, claude settings) into a guest.
  (2) First-run onboarding of a repo that has never used nexus3: detecting the
  stack, authoring .nexus/config.yaml and .nexus/Containerfile, the trust-anchor merge
  ritual. (3) Egress policy authoring and debugging: egress.policy / egress.secrets
  / egress.allow, credential brokering, --allow-host, GH_TOKEN in the guest,
  cross-repo 403, GraphQL 403, "why does the sandbox have open egress". (4)
  Delegating work into a worktree sandbox from any repo: create a worktree-bound
  sandbox, dispatch a brief to an in-guest agent, poll for completion, collect the
  diff, tear down and reclaim RAM, diagnose a stalled delegate workflow. Also load
  when a user wants isolated VM-based builds/tests on their dev machine without
  naming nexus3.
---

# nexus3 skill — dispatcher

Read the routing table below and open the reference file that matches the
question. Each reference is self-contained; open only what you need.

## Routing table

| Question area | Reference file |
|---|---|
| Create, start, stop, pause, resume, rm, shell, forward; `--mount` live host mounts | `references/lifecycle.md` |
| Named volumes (`--mount-named`), ecosystem manifest probes (npm/pnpm/Yarn PnP/Rust/Go/Docker) | `references/volumes.md` |
| MITM CA install, git ownership fix, Go PATH, `go test` trap in guest | `references/guest-setup.md` |
| Start agent in sandbox, drive it, first-run wizards, project host config, workspace trust | `references/agent-in-sandbox.md` |
| Create GitHub PR from sandbox (REST, not GraphQL) | `references/github-pr.md` |
| User-global config (`~/.config/nexus3/config.yaml`), diagnose missing tools, security boundary | `references/user-mounts.md` |
| First-run onboarding: detect stack, author `.nexus/config.yaml` + `.nexus/Containerfile`, trust-anchor ritual (`/nexus3:nexus3-init`) | `references/onboard.md` |
| Egress policy: `egress.policy` / `egress.secrets` / `egress.allow`, brokering model, provider patterns, verification probes, `--allow-host`, per-ecosystem hosts, open-egress posture of worktree sandboxes | `references/egress.md` |
| Delegate work into a worktree sandbox: MCP tool map, push rule, completion heuristic, teardown order, RAM cost (`/nexus3:nexus3-delegate`) | `references/delegate.md` |
| Delegation loop step by step, MCP and CLI spellings, builder failure modes | `references/delegate-loop.md` |
| Brief authoring for the in-guest agent — required content, commit discipline | `references/delegate-briefs.md` |

## Always-true invariants

- **Real token never enters the guest.** The guest receives a 64-hex placeholder; the
  host supervisor intercepts and swaps it on each outbound request.
- **Never mount `~/.claude` wholesale** into a sandbox — it contains the real token.
  Project config only; see `references/agent-in-sandbox.md` for the safe subset.
- **`.git` guest paths are allowed with `--mount`** but are hard-refused with
  `--mount-named`. If you see a refusal about `.git`, switch to `--mount`.
- **`gh pr create` is blocked inside sandboxes** (it uses GraphQL). Use
  `gh api -X POST /repos/{owner}/{repo}/pulls` instead; see `references/github-pr.md`.
- **`fork` and `snapshot create` are refused** on any sandbox that has live `--mount`
  mounts active. Remove live mounts before snapshotting.
- **Worktree sandboxes run with open egress.** `egress.allow` is stored but not enforced
  for them; what IS enforced is secret brokering and the per-path policy on secret hosts
  (cross-repo → 403, GraphQL → 403). See `references/egress.md`.
- **A sandbox may push exactly one ref** — the branch its bound worktree had checked out
  at create time. There is no fixed branch-name pattern. See `references/delegate.md`.
- **`.nexus/config.yaml` is read from `origin/HEAD`**, never from the agent's feature branch.
  A config on a PR branch grants nothing until merged. See `references/onboard.md`.
- **Never write `/**` at root or list `/graphql`** under `api.github.com` in
  `egress.policy`. Scope every path to `/repos/OWNER/REPO/...`.
