# Controller GitHub auto-bind

Status: implemented (R27). Applies to `nexus herdr worktree-sandbox`, which the
controller runs with `NEXUS_PRINCIPAL=<principal>`
(internal/controller/backend/herdr/backend.go:425).

## Why

`/link github` stores `vault.Key{Principal, Integration:"github"}`
(internal/controller/link.go:152-159).

Before this change, the credential reached the sandbox only if the repo's
`.nexus/config.yaml` declared `egress.secrets` for GitHub plus a covering
`egress.policy`. Secret hosts are frozen at create
(`buildWorktreeEgressArgs`, internal/cli/cmd_herdr_plugin.go:4614); the
supervisor seeds the vault token only when GitHub hosts are already in
`SecretHosts` (internal/supervisor/vault_resolve.go:33-48); MITM swaps only for
`SecretHosts` (internal/core/perimeter/mitm/proxy.go:343). So every repo needed
per-repo config.

With auto-bind, any user who ran `/link github` gets GitHub access in their
controller sandboxes automatically, scoped to the sandbox's repo. No per-repo
config is required.

Refs: doc/design/credential-vault.md §Delivery,
.groundwork/motives/agent-front-door-p1-slack-controller/followups.md #4,
D-PD-36 / D-PDE-16.

## Scope rule — add a bind only when ALL hold

1. The checkout config declares no GitHub egress: no `egress.secrets` host and
   no `egress.policy` host matches `domain.IsGitHubHost`. A missing
   `.nexus/config.yaml` counts as none.

2. `remote.origin.url` is github.com in one of these forms:
   - `git@github.com:O/R(.git)`
   - `ssh://git@github.com/O/R(.git)`
   - `https://github.com/O/R(.git)`

   Owner must match `^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`; repo must match
   `^[A-Za-z0-9._-]{1,100}$`; `"."` and `".."` are rejected. URLs with a query,
   fragment, opaque part, or escaped path are also rejected. Reason: the MITM
   path glob treats whole-segment `*` and `**` as wildcards
   (internal/core/perimeter/mitm/proxy.go:1454,1482). An origin such as
   `git@github.com:*/*.git` or a percent-encoded `%2A` would otherwise produce
   `/repos/*/**`, which covers every repo. The guest can rewrite origin through
   the mounted common git dir (internal/cli/cmd_herdr_plugin.go:2463), so origin
   is untrusted input.

   Other hosts, including GitHub Enterprise, get no bind.

3. `NEXUS_PRINCIPAL` (vault.PrincipalEnv) is non-empty. If it is empty, there
   is no bind and the vault is never opened. This restriction is intentional:
   auto-bind targets controller sandboxes only. A host user who ran `nexus vault
   link` and then runs `worktree-sandbox --auto` by hand or from a hook must not
   get an implicit token.

   The link check calls `vault.Source(key, project)`, where `project` is the
   first segment of the sandbox handle (`domain.ParseHandle` of the
   `"<repo>/<identity>"` worktree handle) — the same value the supervisor later
   uses as `sb.Project` (internal/supervisor/vault_resolve.go:47). If the handle
   does not parse, `project` is `""` (fail-closed).

   `/link github` stores `AllowedProjects=nil` in shared mode
   (`ErrProjectNotAllowed`, so no bind until an operator grants a project) and
   `["*"]` otherwise. A record with `AllowedProjects=["projA"]` binds only for
   sandboxes whose project is `projA`. `ErrUnlinked` and `ErrProjectNotAllowed`
   both mean no bind. The CLI checks only that the link exists. It never reads
   the token. The supervisor resolves the token later.

### What gets added

Secret `GH_TOKEN@github.com,api.github.com,uploads.github.com`, plus path
policies under the wildcard repo-scope key `""` (so the D-PDE-16 check passes).

| Host | Paths |
|------|-------|
| github.com | `/OWNER/REPO/**`, `/OWNER/REPO.git/**` |
| api.github.com | `/repos/OWNER/REPO/**`, `/user` |
| uploads.github.com | `/repos/OWNER/REPO/**` |

Rules:

- `OWNER` and `REPO` use the origin's exact casing. Glob segments match exactly
  and case-sensitively (proxy.go:1497). `**` matches zero or more segments, so
  `/repos/O/R/**` also covers `/repos/O/R`.
- Never `/**` at root for `api.github.com` or `github.com`
  (plugins/claude/skills/nexus/references/onboard.md:124, egress.md:230-245).
- Never list `/graphql`. The perimeter denies GraphQL except the built-in
  `viewer{login}` document (proxy.go:588-610). `gh pr create` therefore does
  **not** work. PRs go through REST (see references/github-pr.md).
- Hosts not in the policy stay open, because `worktree-sandbox` forces
  `--egress open` (cmd_herdr_plugin.go:4267). Auto-bind adds only the secret and
  the policy.
- Unit tests pin this exact path set.

## Precedence

The repo config wins. If the checkout declares any GitHub egress (secret or
policy host), auto-bind adds nothing and merges no paths. A repo that wants a
different or narrower GitHub scope declares its own config.

Explicit `nexus sandbox create --secret/--repo` is a separate path and is not
affected by auto-bind.

## Failure modes

- **Vault cannot be opened**: no bind, a warning line in the `worktree-sandbox`
  output, and create continues without GitHub access, as before this change.
- **Origin is missing or not github.com**: no bind.
- **Link exists but token fails in the supervisor** (expired, or refresh fails):
  the supervisor logs `supervisor.vault_github_skip`, the placeholder does not
  resolve, and GitHub returns 401. Re-run `/link github`.
- **Shared-mode controller with no project grant**: `/link github` stores
  `AllowedProjects=nil` in shared mode. `Source` returns `ErrProjectNotAllowed`
  for any `project` value, so no bind is added until an operator grants the
  project. A record with `AllowedProjects=["projA"]` binds only for sandboxes
  whose handle's first segment is `projA`.
- **Scope is frozen at create**: the path set is computed from `remote.origin.url`
  at sandbox creation time. An origin rename requires recreating the sandbox.

## Non-goals

- Cross-repo, fork, or org-wide scopes.
- GitHub Enterprise.
- Changes to the supervisor vault seeding logic.
