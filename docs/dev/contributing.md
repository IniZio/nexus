# Contributing

## Scope

Nexus currently accepts contributions focused on:

- `packages/nexus`
- `packages/sdk/js`

## Setup

```bash
git clone https://github.com/YOUR-USERNAME/nexus
cd nexus
pnpm install
```

## Build and test

```bash
task build
task test
```

Direct commands:

```bash
cd packages/nexus && go test ./...
cd packages/sdk/js && pnpm exec tsc --noEmit && pnpm exec jest --runInBand
```

## Documentation

Update docs when behavior changes:

- `docs/reference/cli.md`
- `docs/reference/sdk.md`
- `docs/reference/workspace-config.md`

Design/research references for API and UX decisions:

- `docs/dev/internal/research/2026-04-07-sdk-cli-ergonomics-benchmark.md`

## Commit style

Use Conventional Commits.

Examples:

- `feat(workspace-daemon): add compose port auto-forward`
- `fix(workspace-sdk): align spotlight response types`
- `docs: update workspace config reference`
