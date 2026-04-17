# Testing Guide

Nexus has three testing tiers: unit, integration, and E2E.

## Tier 1 — Unit Tests

Unit tests are colocated with the code they test:

```bash
# Go packages
cd packages/nexus && go test ./...

# TypeScript SDK
cd packages/sdk/js && pnpm test
```

Go unit tests use the standard `*_test.go` naming convention. TypeScript tests use Jest.

## Tier 2 — Integration Tests

Integration tests live in `packages/nexus/test/integration/` and are gated by `//go:build integration`. They test the full daemon stack with real runtime drivers.

### Running Integration Tests

1. Start the daemon:

```bash
task daemon:restart
```

2. Run the integration suite:

```bash
cd packages/nexus && go test -tags=integration ./test/integration/...
```

### Harness API

The integration harness (`packages/nexus/test/integration/harness.go`) provides:

| Function | Purpose |
|---|---|
| `CreateWorkspace(t, cfg, projectRoot)` | Create a workspace via CLI, start it, return a handle |
| `ExecInWorkspace(t, ws, shellCmd)` | Run a shell command inside a workspace, return stdout |
| `ForkWorkspace(t, parent)` | Fork a workspace, start the child, return the child handle |
| `DestroyWorkspace(t, id)` | Destroy a workspace by ID (called automatically as Cleanup) |

### Driver Configurations

`AllDrivers` in `harness.go` defines all tested driver configurations:

```go
var AllDrivers = []DriverConfig{
    {Backend: "firecracker", Mode: "dedicated"},   // KVM required
    {Backend: "firecracker", Mode: "pool"},        // KVM required
    {Backend: "process", Mode: "process"},         // runs everywhere
}
```

Each driver has a `SkipUnless` guard that skips tests when requirements aren't met (e.g., no `/dev/kvm` for firecracker).

### Example Test

See `packages/nexus/test/integration/driver_test.go`:

```go
func TestAllDrivers(t *testing.T) {
    for _, driver := range AllDrivers {
        driver := driver
        t.Run(driver.Backend+"/"+driver.Mode, func(t *testing.T) {
            driver.SkipUnless(t)
            // Use harness helpers...
        })
    }
}
```

### Adding Integration Tests

1. Add a file under `packages/nexus/test/integration/`
2. Use `//go:build integration` at the top
3. Use `package integration`
4. Use `CreateWorkspace`, `ExecInWorkspace`, `ForkWorkspace` from harness
5. `t.Parallel()` is supported per driver sub-test

## Tier 3 — E2E Tests

E2E tests live in `packages/e2e/flows/` and test against a live daemon + runtime.

```bash
# Full CI-equivalent (requires daemon + runtime)
task ci:flows-e2e

# Soft-skip mode (no runtime required)
NEXUS_E2E_STRICT_RUNTIME=0 task ci:flows-e2e
```

### Environment Variables

| Variable | Description |
|---|---|
| `NEXUS_DAEMON_WS` | WebSocket URL for daemon connection |
| `NEXUS_DAEMON_TOKEN` | Auth token for daemon |
| `NEXUS_DAEMON_PORT` | Daemon port (default 63987) |
| `NEXUS_E2E_STRICT_RUNTIME=0` | Allow soft skips when no VM runtime installed |
| `CI=true` | Enforces runtime expectations (always set in CI) |
| `NEXUS_CLI_PATH` | Path to nexus CLI binary (set by `flows-e2e-setup.sh`) |

### Setup Script

`scripts/ci/flows-e2e-setup.sh` builds the CLI, initializes a seed repo, and writes `.nexus-e2e-env.sh` with `NEXUS_CLI_PATH` and `PATH` for the test environment.

## XCUITests (macOS App)

These run against the built NexusApp:

```bash
task test:smoke    # launch, connect, connection status
task test:terminal  # NexusTerminalUITests (daemon + Accessibility permission required)
task test           # all XCUITests
task test:unit      # NexusAppTests (unit only, no UI)
```

## CI Pipeline

The full CI pipeline (runs on every PR):

```bash
task ci
```

Which runs:
- `ci:go-fix` — Go fix style check
- `ci:coverage` — Coverage report
- `ci:core` — Build + lint + test all packages
- `ci:flows-e2e` — Runtime E2E flows (strict when `CI=true`)

## Coverage

```bash
task ci:coverage
```

Runs `go test ./... -covermode=atomic` and prints the total coverage summary.