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

### Builder agent: two binaries, one silent trap

`nexus3 create --file` bakes a **separate** `nexus3-agent` binary into the
builder VM image. It is resolved at runtime via `exec.LookPath("nexus3-agent")`
— typically `~/.local/bin/nexus3-agent` — and is **not** `go:embed`ded in the
CLI. The builder-image cache key is `sha256(agentBytes)[:8]`, so rebuilding
only `go build ./cmd/nexus3` leaves the on-PATH agent stale. New agent
code (e.g. `boot.json` capture) silently never runs.

**Always rebuild both binaries before a live-e2e run that exercises `--file`:**

```bash
# 1. Rebuild the on-PATH builder agent (baked into the builder VM image)
make install-agent          # CGO_ENABLED=0, installs to ~/.local/bin/nexus3-agent

# 2. Rebuild the CLI
go build -o ~/.local/bin/nexus3 ./cmd/nexus3
```

**Detect staleness before running:**

```bash
make check-agent-fresh
```

This checks both the base-image agent (`images/kernel/nexus3-agent`) and
the on-PATH builder agent. It fails with a clear message if either is older
than the agent source tree.

### Live-e2e incantation (KVM + herdr host required)

For tests under the `herdr_live` build tag (e.g. `TestOCIBootJSON_Live`):

```bash
CGO_ENABLED=0 go build -o ~/.local/bin/nexus3-agent ./cmd/nexus3-agent
go build -o ~/.local/bin/nexus3 ./cmd/nexus3
TMPDIR=/tmp NEXUS3_LIVE_REQUIRED=1 NEXUS3_KERNEL_PATH=$(pwd)/images/kernel/vmlinux-x86_64 \
  go test -tags herdr_live -count=1 -run TestOCIBootJSON_Live ./internal/cli/
```

Key flags:
- `-count=1` — forces re-execution; `go test` replays cached logs otherwise
- `TMPDIR=/tmp` — avoids AF_UNIX 107-byte `sun_path` overflow on long repo paths
- `NEXUS3_LIVE_REQUIRED=1` — fails immediately if KVM/herdr is not present instead
  of silently skipping the test
- Both binaries must be rebuilt from the same source commit — the builder image
  caches on `sha256(nexus3-agent)[:8]`, so a mismatched agent produces a fresh
  image build every run and silently runs old agent code until the cache invalidates
