---
name: nexus3
description: >
  Load when the user asks about nexus3 sandbox operations: create, start, stop,
  pause, resume, rm, shell, exec, snapshot, fork, volume, mount (--mount or
  --mount-named), agent-in-sandbox, egress rules, PR from inside a sandbox, or
  projecting host config (credentials, git config, claude settings) into a guest.
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
| `--allow-host` for agent sandboxes, per-ecosystem hosts; full egress policy authoring | `references/egress.md` (and skill `nexus3:nexus3-egress` for policy authoring) |

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
