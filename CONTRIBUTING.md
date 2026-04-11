# Contributing

Scope: `packages/nexus`, `packages/sdk/js`.

## Setup

```bash
git clone https://github.com/YOUR_USERNAME/nexus.git
cd nexus
pnpm install
```

## Build and test

```bash
task build
task test
```

Or: `cd packages/nexus && go test ./...` and `cd packages/sdk/js && pnpm exec tsc --noEmit && pnpm exec jest --runInBand`.

## Docs

When behavior changes, update `docs/reference/cli.md`, `sdk.md`, and `workspace-config.md` as needed. Developer notes: `docs/dev/contributing.md`, architecture: `docs/explanation/architecture.md`.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/): `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, etc.

## PRs

Tests pass, docs updated if needed, focused changes. Address review feedback.
