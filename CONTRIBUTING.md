# Contributing to nexus3

## Commit convention

nexus3 uses [Conventional Commits](https://www.conventionalcommits.org/) for
automated semantic versioning. Every commit that lands on `main` — in
practice, the PR title on a squash-merge — must match this format:

```
type[(scope)][!]: description
```

**Allowed types**

| Type       | Triggers release? | Bump     |
|------------|-------------------|----------|
| `feat`     | yes               | minor    |
| `fix`      | yes               | patch    |
| `perf`     | yes               | patch    |
| `refactor` | yes               | patch    |
| `docs`     | no                | —        |
| `test`     | no                | —        |
| `chore`    | no                | —        |
| `ci`       | no                | —        |
| `build`    | no                | —        |
| `style`    | no                | —        |

A trailing `!` (e.g. `feat!:` or `fix(core)!:`) marks a breaking change and
triggers a **minor** bump regardless of type.

**Examples**

```
feat(sandbox): add --mount-named flag for named volume mounts
fix(reaper): release flock lease before record commits
refactor(cli): extract version stamping into buildinfo package
docs: update contributing guide with commit convention
chore!: drop support for Go 1.21
```

### Scope

Scope is optional but encouraged for non-trivial changes. Use the affected
subsystem name (e.g. `sandbox`, `reaper`, `cli`, `supervisor`, `perimeter`).

### Enforcement

The PR title is validated against the pattern above by the `Commitlint` CI
check (`.github/workflows/commitlint.yml`). To test a subject locally:

```bash
bash scripts/ci/check-commit-subject.sh "feat(sandbox): my change"
```

The same script is the source of truth for the CI check — no external
commitlint binary or config file.

### Why PR title only

GitHub squash-merge uses the PR title as the commit message that lands on
`main` and is analyzed by semantic-release. Individual commits in the
feature branch are not inspected. This lets you use WIP commit messages,
fixup commits, and squash! prefixes freely during development.

## Release process

Releases are fully automated. Push to `main` (via a merged PR) and
semantic-release inspects the commit history since the last tag, computes
the next version from the commit types, mints a `vMAJOR.MINOR.PATCH` tag,
creates a GitHub release with auto-generated release notes, and triggers the
CD workflow to build and publish the `nexus3-linux-amd64` binary with
SHA256SUMS.

No manual version bumping or tagging is required. The CD workflow
(`.github/workflows/cd.yml`) is the source of truth for the release process.

## Development

```bash
# Build
go build ./...

# Test (TMPDIR=/tmp required: long paths exceed AF_UNIX sun_path limit)
TMPDIR=/tmp go test ./...
```

Integration tests (KVM-gated) are excluded from the default run. They require
the `integration` build tag and a host with `/dev/kvm`.
