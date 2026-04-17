# Contributing

## Scope

Core packages:

- `packages/nexus` — Workspace Daemon (Go, JSON-RPC over WebSocket)
- `packages/sdk/js` — Workspace SDK (TypeScript, `@nexus/sdk`)
- `packages/e2e/flows/` — E2E test harness and flows
- `packages/nexus-ui/` — Web UI (Vue, served by the daemon)
- `packages/nexus-swift/` — macOS app (NexusApp, embeds the daemon for local dev)

## Setup

```bash
git clone https://github.com/YOUR_USERNAME/nexus.git
cd nexus
pnpm install
```

## Architecture

Nexus is intentionally small: daemon + SDK + repository conventions.

**Daemon** runs on a **remote Linux** machine. Clients connect via JSON-RPC over WebSocket. The only supported VM backend is **Firecracker** (process sandbox is available as a fallback for environments without VM support).

Auth travels via `configBundle` in `workspace.create` RPC — never via symlinks to the daemon's `$HOME`.

**Typical request flow**

1. SDK connects to the daemon over an authenticated WebSocket.
2. The client creates or opens a workspace.
3. Operations run through workspace-scoped handlers.
4. Results return as JSON-RPC responses.

**Runtime backends**

| Backend | Description |
|---|---|
| `firecracker` | VM isolation (primary, Linux only) |
| `process` | Process isolation fallback (all platforms) |

## Building

```bash
task build              # all packages (nexus, sdk, nexus-ui)
task build:workspace-daemon   # Go daemon + CLI only
task build:workspace-sdk       # TypeScript SDK only
```

### Daemon development

```bash
task daemon:build    # compile the Go daemon binary
task daemon:restart  # kill existing daemon, start fresh, wait for /healthz
task daemon:logs     # tail ~/.config/nexus/run/daemon.log
```

### macOS app development

```bash
task swift:prepare-resources  # embed locally built daemon into the app bundle
task swift:build             # build NexusApp.app via xcodebuild
task app:open                # launch NexusApp.app for eyeball testing
task dev                     # daemon:restart + app:open
```

## Testing Tiers

Nexus has three testing tiers:

### Tier 1 — Unit tests

```bash
task test:workspace-daemon   # go test ./...
task test:workspace-sdk       # pnpm test
```

### Tier 2 — Integration tests

Integration tests live in `packages/nexus/test/integration/` and use the `//go:build integration` build tag. They require a running daemon with `NEXUS_DAEMON_PORT` set.

```bash
# Start the daemon first
task daemon:restart

# Run integration tests
cd packages/nexus && go test -tags=integration ./...
```

The integration harness (`packages/nexus/test/integration/harness.go`) provides `CreateWorkspace`, `ExecInWorkspace`, `ForkWorkspace`, and `DestroyWorkspace` helpers. See `packages/nexus/test/integration/driver_test.go` for examples covering all backends.

### Tier 3 — E2E tests

E2E flows test against a live daemon + runtime.

```bash
# Full CI-equivalent (requires daemon + runtime)
task ci:flows-e2e

# Local soft-skip mode (no runtime required)
NEXUS_E2E_STRICT_RUNTIME=0 task ci:flows-e2e
```

Key environment variables:

| Variable | Description |
|---|---|
| `NEXUS_DAEMON_WS`, `NEXUS_DAEMON_TOKEN` | Point tests at an existing daemon |
| `NEXUS_DAEMON_PORT` | Daemon port (default 63987) |
| `NEXUS_E2E_STRICT_RUNTIME=0` | Allow soft skips when no VM runtime is installed |
| `CI=true` | Enforces runtime expectations; always set in CI |

### XCUITests (macOS)

```bash
task test:smoke    # quick smoke: launch, connect
task test:terminal # NexusTerminalUITests (requires daemon + Accessibility permission)
task test          # all XCUITests
task test:unit     # NexusAppTests (unit only, no UI)
```

## File Size Policy

Code is organized into layers with size limits:

| Layer | Limit | Examples |
|---|---|---|
| Core/domain logic | ≤300 lines | `runtime/driver.go`, `runtime/factory.go` |
| Orchestration/application logic | ≤400 lines | handlers, workspacemgr |
| Transport/adapters/tests | ≤500 lines | transport adapters, integration tests |

Known violations (tracked, not hard failures):

```
packages/nexus/pkg/handlers/workspace_manager.go  ~1246 lines  (severely over limit)
packages/nexus/pkg/workspacemgr/manager.go        ~1268 lines  (severely over limit)
```

## Agent Skills

Nexus ships opencode agent skills in `.opencode/skills/`:

| Skill | When to use |
|---|---|
| `nexus-macos-dev` | Building and running NexusApp locally |
| `bumping-app-version` | Incrementing app version numbers |
| `creating-git-commits` | Creating commits matching repo conventions |
| `superpowers` | Workspace superpowers (experimental) |

Groundwork workflow skills (loaded from `~/.config/opencode/plugins/groundwork/skills/`):

| Skill | When to use |
|---|---|
| `create-prd` | Starting a non-trivial feature (≥1 day) |
| `advisor-gate` | Any technical decision; always at task completion |
| `nested-prd` | Architectural pivot mid-implementation |
| `bdd-implement` | Any visible UI change or bug fix |
| `commit` | Creating git commits |
| `session-continue` | Context window growing long |

## Docs

When behavior changes, update as needed:

- `docs/reference/cli.md`
- `docs/reference/sdk.md`
- `docs/reference/workspace-config.md`
- `docs/guides/release-signing.md`
- `docs/guides/testing.md`

## Commits

[Conventional Commits](https://www.conventionalcommits.org/): `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, etc.

Examples:

- `feat(workspace-daemon): add compose port auto-forward`
- `fix(workspace-sdk): align spotlight response types`
- `docs: update workspace config reference`
- `refactor(workspacemgr): split manager.go into focused sub-packages`

## PRs

Keep changes focused. Ensure `task build` and `task test` pass. Update docs when behavior changes. Address review feedback.

## Release Pipeline

```
opencode (implementation)
    │  conventional commits
    ▼
gh pr create
    │  CI checks (task ci)
    ▼
merge to main
    │  tag pushed → GitHub Actions builds + signs + publishes
    ▼
GitHub release
```

Release signing: see `docs/guides/release-signing.md`.
