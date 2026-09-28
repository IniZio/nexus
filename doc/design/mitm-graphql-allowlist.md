# MITM GraphQL allowlist (TBR-GRAPHQL / R5)

Status: implemented in `internal/core/perimeter/mitm/graphql_*.go` (slice R30).
Supersedes the S1c GraphQL deny-all stub. Its one carve-out, `viewer{login}`,
is now one shape in the allowlist.

## Problem

`gh pr create|view|list|status|checkout` use only GraphQL (gh 2.100.0 made no
PR-related REST call in the capture). The MITM denied every `api.github.com/graphql`
document except `viewer{login}`, so these commands returned 403 in every sandbox.
The brokered token is a GitHub App user token. The App installation bounds its
scope, and that installation can cover many repos. nexus must still pin GraphQL
to the sandbox's policy repo (D-PD-36 / D-PDE-16), exactly as it pins REST.

## Corpus

`internal/core/perimeter/mitm/testdata/gh_graphql_corpus.json` holds 17 byte-exact
request bodies from gh 2.100.0. They were captured by a local TLS-intercepting stub,
with no GitHub contact. Operations: RepositoryInfo, PullRequestForBranch,
PullRequestCreate (mutation `createPullRequest(input:$input)`),
PullRequestByNumber (3 field sets), PullRequestProjectItems, PullRequestList,
PullRequestSearch (`search(query:$q,type:$type)`), PullRequestStatus
(`repository` + aliased `viewerCreated: search` + `reviewRequested: search`),
and 3 introspection probes (`__type(name:"SearchType"|"PullRequest"|
"StatusCheckRollupContextConnection"|"WorkflowRun")`). gh does not send
`operationName`. The documents use named fragments, inline fragments, aliases,
`__typename`, and variable defaults. They use no directives.

## Decision

The decision is based on the AST shape of the document, never on the operation
name or on regexes.

1. **Envelope** (strict JSON). POST only, to path exactly `/graphql` on
   `api.github.com`, with an empty URL query string. The `Content-Type`
   media type must be `application/json`; the only parameter allowed is
   `charset=utf-8`. The body is at most 1 MiB. It must be a JSON object: a
   top-level array is a batch and is denied. Allowed keys are
   `query` (required string), `variables` (object or null), and `operationName`
   (string or null; if non-empty, it must equal the name of the single operation).
   Any other key (`extensions`, persisted-query `id`/`doc_id`, ...) is denied.
   A duplicate JSON key at ANY depth is denied, because decoders are last-wins.
2. **Parse** with an in-package strict parser (`graphql_parse.go`). go.mod has no
   GraphQL dependency, and a new module is not worth the supply-chain surface for
   this subset. The parser denies:
   - more than one operation, or subscriptions;
   - any directive, or any block string;
   - type-system definitions;
   - undefined, unused, or cyclic fragments, and undeclared variables;
   - a query longer than 32 KiB, a depth over 24, or more than 3000 fields.

   It inlines fragments and drops type conditions: shape checks go by field
   name. The merge check is recursive and applies to every selection set at
   every depth, including fields brought in by inline fragments and spreads,
   whether or not the parent field was duplicated. Fields that share a
   response key must have the same name and deep-equal arguments. Their
   children are unioned, and the check repeats on the result. On any conflict
   the document is denied. Because of this, an alias cannot give `id` a second
   meaning at any level. For example,
   `repository{id ...on Repository{id: name}}` is denied.
3. **Root fields** are keyed by field NAME, not alias:
   - query: `repository`, `search`, `viewer`, `__type`, `__typename`
   - mutation: exactly one root field, `createPullRequest`

   Anything else is denied. Examples: `node`, `nodes`, `organization`, `user`,
   `__schema`, and every other mutation.
4. **Shape allowlist** (`graphql_shapes.go`). Each root has an allowed
   selection tree, written as GraphQL text and parsed at init. It is the union of
   the corpus field sets. A request field must exist in the tree. A shape leaf
   must be a request leaf. `__typename` is allowed anywhere. Nested traversal to
   other repositories is therefore blocked structurally. Examples:
   `repository{owner{repositories}}`, `parent{pullRequests}`,
   `author{...on User{repositories}}`. Cross-repo nodes that gh needs
   (`parent`, `headRepository`, `headRepositoryOwner`) expose only identity
   scalars.
5. **Repo pinning**. The allowed repos come from the request's
   `(placeholder, host)` HostPolicy:
   - `GitHub{Owner,Name}` gives a writable repo.
   - A pattern gives `Owner/Name` when its segments are literally
     `/repos/O/R/**`. The repo is writable if the method is empty or POST, and
     read-only if the method is GET.

   Checks per root field:
   - `repository(owner,name)`: the arguments must be exactly `{owner,name}`.
     Each value is a string literal, or a variable whose JSON value is a string.
     The pair must case-insensitively equal an allowed repo.
   - `search(query,type,...)`: `type` must be `ISSUE`. `query` is split on
     whitespace, and the parts must satisfy all of these rules:
     - Exactly one `repo:O/R` qualifier, and it names an allowed repo.
     - No `org:`, `user:`, or `owner:` qualifier, and no negated `-repo:`.
     - No quotes or parentheses, and no `AND`, `OR`, or `NOT`.
     - Every other qualifier key comes from a fixed list.
   - `__type(name:"<literal>")`: the name must be in the set of 4 captured type
     names. The children are only `fields`/`enumValues(includeDeprecated){name}`.
     This returns public schema metadata only.
   - `viewer`: only `{login}` (the same as the REST `GET /user` carve-out).
   - `createPullRequest(input)`: the input keys must be a subset of
     `{repositoryId, baseRefName, headRefName, headRepositoryId, title, body,
     draft, maintainerCanModify, clientMutationId}`, and `repositoryId` is
     required. `repositoryId` and any `headRepositoryId` must be node IDs in the
     sandbox's **repo-ID cache**, and they must map to a *writable* allowed repo.
     A `headRefName` that contains `:` is denied.
6. **Node-ID binding** (`repoIDCache`, per Proxy, so per sandbox, bounded at 256
   entries with FIFO eviction). When an allowed query has a root
   `repository(owner,name)` that selects `id`, the request handler records
   `(rootResponseKey, idResponseKey, owner/name)` in `ctx.UserData`. The
   response handler reads the 200 JSON (at most 2 MiB; goproxy strips
   Accept-Encoding, so the body is plain) and caches `data[root][id] → owner/name`.
   gh always runs RepositoryInfo before PullRequestCreate in the same process,
   so the cache is warm. An unknown ID is denied, and the failure is closed.
   The values come only from GitHub's responses to queries that were already
   pinned, so the guest cannot poison the cache. We rejected two alternatives:
   - A host-side `node(id)` lookup: an extra upstream call with the real token.
   - Decoding the node-ID format: undocumented, and it still needs the
     database ID.

**Host binding.** A separate first request handler binds the request to its
dial target, and every handler (path policy, GraphQL, harvest, swap) relies
on it:
- The URL host and the `Host` header of each MITM'd request must equal the
  CONNECT target.
- For a plain proxied request, the `Host` header must equal the URL host.

Without this, a guest could CONNECT to host A and send
`Host: api.github.com`. The proxy would then treat the request as GitHub
traffic and send the real token to A.

Enforcement runs in the existing GraphQL OnRequest handler. That handler runs
before the credential swap, so a denied document never sees the real token.

## Out of scope / follow-ups

- `gh pr create --reviewer/--label/--assignee/--project/--milestone` sends
  follow-up mutations (`updatePullRequest`, `requestReviews`, ...) that take
  a `pullRequestId`. These stay denied. A follow-up could harvest PR IDs from
  `createPullRequest` responses into the same cache.
- `gh pr merge|edit|comment|review`: not enumerated; they stay denied.
- gh versions that add fields get a 403 until the shape is extended. Recapture
  with the scratch stub (see the R30 receipt) and extend `graphql_shapes.go`.
