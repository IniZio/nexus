# GitHub pull requests from inside a nexus sandbox

## MITM perimeter: PR commands work for the policy repo

The MITM egress perimeter is the only boundary for in-guest GitHub traffic.
It enforces a REST path ACL and an AST-shape GraphQL allowlist.
The allowlist covers every GraphQL document issued by `gh pr create`,
`gh pr view`, `gh pr list`, `gh pr status`, and `gh pr checkout` against
the sandbox's **policy repo** (the repo bound at sandbox-create time).

**Only the policy repo is covered.** Commands targeting any other repo return 403.

`gh auth status` works: the perimeter admits its two probes (`GET /` on
`api.github.com` and the `viewer{login}` GraphQL document) regardless of how the
repo's `egress.policy` is written.

GraphQL documents outside the allowlist return 403. Notably, `gh repo view --json`
is not in the allowlist — use REST for repo metadata:

```sh
# Read repo context over REST (not gh repo view --json)
gh api repos/{owner}/{repo} --jq .default_branch
gh api repos/{owner}/{repo} --jq .owner.login
gh api repos/{owner}/{repo} --jq .name
```

`gh api` expands `{owner}` and `{repo}` from the local git remote automatically.

## Push first

The branch must exist on the remote before opening a PR:

```sh
git push origin HEAD
```

The perimeter allows pushing to the worktree's own branch; the push allowlist is
derived from the bound worktree, not a fixed pattern.

## Create, view, list, check out

Use the `gh` commands directly — no REST workarounds needed for the policy repo:

```sh
git push origin HEAD   # branch must exist on the remote first

gh pr create --base <base-branch> --head <branch> --title "<title>" --body "<body>"
gh pr view <number>
gh pr list
gh pr status
gh pr checkout <number>
```

**Draft PRs**: pass `--draft` only if the target repo supports drafts. Some private
repos do not — `--draft` returns HTTP 422 there. Omit it for a regular PR.

### Flags that remain denied

`gh pr create --reviewer`, `--label`, `--assignee`, `--project`, and `--milestone`
send follow-up GraphQL mutations (`requestReviews`, `updatePullRequest`, etc.) that
are not in the allowlist. Those flags return 403 after the PR is created. Use REST
or run them from the host:

```sh
# Add a label over REST (from inside the sandbox, if the REST path is allowed)
gh api -X POST repos/{owner}/{repo}/issues/<number>/labels -f labels[]="<label>"
```

`gh pr merge`, `gh pr edit`, `gh pr comment`, and `gh pr review` use GraphQL
operations not in the allowlist and remain denied (403).

### Error messages

A denied GraphQL document returns HTTP 403 with error code `D-PD-36: request path
not in allowlist`. The proxy logs `mitm: GraphQL denied` with a reason field.

### Newer gh versions

If a newer `gh` release adds fields to any PR document, those requests may 403
until the shape allowlist is extended. Recapture the corpus and update
`graphql_shapes.go` (see the R30 design doc at `doc/design/mitm-graphql-allowlist.md`).

## Stacked PRs

`gh stack submit` works if the `gh-stack` extension is present — it creates PRs
over REST, not GraphQL, so the perimeter allows it.

## Attaching a GitHub credential

Pass `--repo owner/name` together with
`--secret GH_TOKEN@github.com,api.github.com,uploads.github.com` at create time.
There is no built-in GitHub token — sandboxes default to no GitHub credential
(fail-closed). The `--no-builtin-gh` flag was removed (D-PDE-02); the one
mechanism is `--secret`.

`--repo` alone without `--secret` does **not** inject a credential.

```sh
nexus create myproject/sandbox \
  --repo myorg/myrepo \
  --secret GH_TOKEN@github.com,api.github.com,uploads.github.com \
  --image nexus-agent-base
```

---

## VCS egress for worktree sandboxes (authoring .nexus/config.yaml)

> **Full procedure is in `egress.md`.** This section is a factual
> summary; use the referenced skill for the complete step-by-step workflow and
> verification probes.

### How .nexus/config.yaml egress works

- `.nexus/config.yaml` at the repo root controls egress for worktree sandboxes.
- It is read from the worktree's own checkout; a change takes effect on the
  next worktree-sandbox create.
- GitHub hosts **require** an `egress.policy` entry; sandbox create is refused
  with a hard error if one is absent.
- Non-GitHub hosts (GitLab, generic API) have no mandatory path policy.

### Config source

1. Author `.nexus/config.yaml` in the worktree and commit it on the branch.
2. The next worktree sandbox created for that checkout inherits the egress
   rule. No push to the default branch is needed.
3. An already-running sandbox is not updated — relaunch it.

### Verification

Four checks exist (own-repo REST 200, cross-repo 403, GraphQL two-direction probe,
placeholder check). Details in `egress.md`.

---

### .nexus/config.yaml — GitHub example

```yaml
version: 1
egress:
  policy:
    - host: github.com
      paths: ["/acme/myrepo/**"]
    - host: api.github.com
      paths: ["/repos/acme/myrepo/**", "/repos/acme/myrepo", "/user"]
    - host: uploads.github.com
      paths: ["/**"]
  secrets:
    - env: GH_TOKEN
      hosts: [github.com, api.github.com, uploads.github.com]
```

Cross-repo API calls are denied by the REST path allowlist. GraphQL is controlled
by the shape allowlist, not the path list — PR operations for this repo work;
documents outside the allowlist (cross-repo queries, non-PR mutations) return 403.

> **SECURITY WARNING — GitHub glob tightness is the author's responsibility.**
> The `egress.policy` layer is generic default-deny globs; the system cannot
> automatically narrow what you write. When authoring GitHub entries you MUST:
>
> - **Scope every `api.github.com` path to `/repos/<owner>/<repo>/**`** (plus
>   specific endpoints like `/repos/<owner>/<repo>` and `/user`). Do **not**
>   write `/**` or `/` at the root — that would let a compromised guest reach
>   other repos and perform any API action with the operator's full-scope token.
> - **Do not list `/graphql`** under `api.github.com`. GraphQL is a parallel
>   write channel that bypasses path-allowlist semantics; listing it reopens the
>   sole-bound risk documented in the security notes (`nexus-github-token-sole-bound`).
>   The unconditional `/graphql` backstop only fires on the legacy CLI
>   `GitHubPolicy` path — it does **not** apply to config-authored globs.
> - **Do not list `/**` under `github.com`** — that exposes all repository HTML
>   and raw download paths across the entire platform.
>
> An unscoped or `/graphql`-listing GitHub glob **reopens the sole-bound risk**:
> the operator's unrotated full-scope token becomes the only protection. Treat
> the path list as the security perimeter, not as a convenience filter.

The short form (`GH_TOKEN@github.com,...`) is **not valid for GitHub** because
policy entries are mandatory. Always use the long form with explicit `policy:`
and `secrets:` keys.

### .nexus/config.yaml — GitLab example

Non-GitHub hosts have no mandatory path policy. A project access token scoped to
the specific project is strongly preferred over a full personal access token.

```yaml
version: 1
egress:
  secrets:
    - env: GITLAB_TOKEN
      hosts: [gitlab.com]
```

For a self-hosted instance:

```yaml
version: 1
egress:
  secrets:
    - env: GITLAB_TOKEN
      hosts: [git.example.com]   # adjust to your instance hostname
```

Short form is also accepted for non-GitHub hosts:

```yaml
egress:
  secrets:
    - GITLAB_TOKEN@gitlab.com
```

### .nexus/config.yaml — Generic path-restricted API token

```yaml
version: 1
egress:
  policy:
    - host: api.example.com
      paths: ["/v4/projects/123/**"]
  secrets:
    - env: API_TOKEN
      hosts: [api.example.com]
```

Paths are anchored globs. An optional `METHOD ` prefix restricts to one HTTP
verb (e.g. `"GET /v4/projects/123/**"`).

### Manual sandbox create (non-worktree) with a VCS secret

Outside the herdr worktree flow, pass `--secret` explicitly. GitHub additionally
requires `--repo`:

```sh
# GitHub
nexus sandbox create myproject/dev-1 \
  --image nexus-agent-base \
  --secret GH_TOKEN@github.com,api.github.com,uploads.github.com \
  --repo acme/myrepo

# GitLab
nexus sandbox create myproject/dev-1 \
  --image nexus-agent-base \
  --secret GITLAB_TOKEN@gitlab.com
```

There is no built-in GitHub token; `--repo` alone without `--secret` does not
inject a credential (D-PDE-02). Pass both, or declare both in `.nexus/config.yaml` for
automatic wiring via the worktree flow.
