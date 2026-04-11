# Contributing

## Scope

`packages/nexus`, `packages/sdk/js`.

## Setup

```bash
git clone https://github.com/YOUR_USERNAME/nexus
cd nexus
pnpm install
```

## Build and test

```bash
task build
task test
```

Directly:

```bash
cd packages/nexus && go test ./...
cd packages/sdk/js && pnpm exec tsc --noEmit && pnpm exec jest --runInBand
```

## Docs

When behavior changes, update:

- `docs/reference/cli.md`
- `docs/reference/sdk.md`
- `docs/reference/workspace-config.md`

## Commits

Conventional Commits, for example:

- `feat(workspace-daemon): add compose port auto-forward`
- `fix(workspace-sdk): align spotlight response types`
- `docs: update workspace config reference`
