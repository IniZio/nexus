# Contributing

## Scope

`packages/nexus`, `packages/sdk/js`, `packages/e2e/flows/` (E2E tests and harness).

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

## E2E (`packages/e2e/flows/`)

The flows package talks to a running daemon. Typical environment variables:

- `NEXUS_DAEMON_WS`, `NEXUS_DAEMON_TOKEN` — point tests at an existing daemon (see harness errors when unset).
- `NEXUS_DAEMON_PORT` — used where tests or scripts spawn or address a daemon by port.
- `NEXUS_E2E_STRICT_RUNTIME` — `1` enforces runtime expectations; `0` allows soft skips (see also `CI=true` for managed daemon in CI). Set to `0` for local runs without a runtime installed; CI always sets `1`.
- `NEXUS_E2E_FIXTURE_ROOT` — optional override for fixture disk layout.
- Auth and live-model runs may use additional `NEXUS_E2E_*` variables (see `packages/e2e/flows/src/cases/auth/` and CI).

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

