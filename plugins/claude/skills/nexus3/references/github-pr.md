# GitHub pull requests from inside a nexus3 sandbox

## MITM perimeter: GraphQL is denied fail-closed

The MITM egress perimeter is the only boundary for in-guest GitHub traffic.
It allowlists REST endpoints and **denies GraphQL fail-closed (HTTP 403)**.

**`gh pr create` uses a GraphQL mutation. Do not use it — it returns 403.**

Other `gh` subcommands that use GraphQL are also blocked. Notably,
`gh repo view --json` returns 403. Use REST alternatives instead:

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

## Create the PR over REST

```sh
gh api -X POST repos/{owner}/{repo}/pulls \
  -f title="<title>" \
  -f head="<branch>" \
  -f base="<base-branch>" \
  -f body="<body>"
```

A successful call returns HTTP 201 with an `html_url` field pointing to the new PR.

**Draft PRs**: add `-F draft=true` only if the target repo supports drafts.
Some private repos do not — a hardcoded `draft=true` returns HTTP 422 there.
Omit it for a regular PR.

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
nexus3 create myproject/sandbox \
  --repo myorg/myrepo \
  --secret GH_TOKEN@github.com,api.github.com,uploads.github.com \
  --image nexus3-agent-base
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

Four checks exist (own-repo REST 200, cross-repo 403, GraphQL 403, placeholder
check). Details in `egress.md`.

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

Cross-repo API calls and `gh api graphql` (GraphQL) are denied (403) by default
because they are not listed in the path allowlist.

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
>   sole-bound risk documented in the security notes (`nexus3-github-token-sole-bound`).
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
nexus3 sandbox create myproject/dev-1 \
  --image nexus3-agent-base \
  --secret GH_TOKEN@github.com,api.github.com,uploads.github.com \
  --repo acme/myrepo

# GitLab
nexus3 sandbox create myproject/dev-1 \
  --image nexus3-agent-base \
  --secret GITLAB_TOKEN@gitlab.com
```

There is no built-in GitHub token; `--repo` alone without `--secret` does not
inject a credential (D-PDE-02). Pass both, or declare both in `.nexus/config.yaml` for
automatic wiring via the worktree flow.
