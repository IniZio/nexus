# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**nexus** - Isolated dev environments with SSH + automatic port management for AI coding assistants (Cursor, OpenCode, Claude). Written in Go 1.24+.

## Build & Test Commands

```bash
make build          # Build nexus binary to bin/nexus
make test           # Run all tests (unit + integration + e2e)
make test-unit      # Unit tests with race detector, coverage to coverage.html
make test-integration # Integration tests (no Docker/LXC required)
make test-e2e       # E2E tests (requires Docker/LXC)
make lint           # Run golangci-lint and go vet
make fmt            # Format code with gofmt
make fmt-check      # Check formatting without modifying
make ci-build       # Multi-platform builds (Linux, Darwin, Windows)
make install        # Build and install to ~/.local/bin
```

## Architecture

### Provider Plugin System (`pkg/provider/`)

The core abstraction is the `Provider` interface in `pkg/provider/provider.go`:

```go
type Provider interface {
    Name() string
    Create(ctx context.Context, sessionID string, workspacePath string, config interface{}) (*Session, error)
    Start(ctx context.Context, sessionID string) error
    Stop(ctx context.Context, sessionID string) error
    Destroy(ctx context.Context, sessionID string) error
    Exec(ctx context.Context, sessionID string, opts ExecOptions) error
    List(ctx context.Context) ([]Session, error)
}
```

Implementations:
- `pkg/provider/docker/` - Docker container-based workspaces
- `pkg/provider/lxc/` - LXC containers with proxy devices
- `pkg/provider/qemu/` - QEMU VM support

### Coordination Server (`pkg/coordination/`)

HTTP server (port 3001) managing workspaces with SQLite persistence. Handles workspace CRUD operations via REST API.

### Key Packages

| Package | Purpose |
|---------|---------|
| `pkg/coordination` | Workspace registry and HTTP server |
| `pkg/provider` | Container/VM provider plugins |
| `pkg/agent` | Agent running inside workspaces |
| `pkg/github` | GitHub App integration |
| `pkg/auth` | OIDC and session authentication |
| `pkg/ssh` | SSH key management |
| `pkg/transport` | SSH and HTTP transport layers |
| `pkg/templates` | Config template merging |
| `pkg/config` | Configuration loading |
| `pkg/worktree` | Git worktree management |

## Code Conventions

- **Error handling**: Always wrap: `fmt.Errorf("failed to X: %w", err)`
- **Naming**: camelCase for vars, PascalCase for exports, acronyms as `HTTPClient`
- **Testing**: Use `testify/assert` and `testify/require`, table-driven tests, aim for 90%+ coverage
- **Commits**: Conventional format `<type>(<scope>): <description>` (types: feat, fix, docs, style, refactor, perf, test, build, ci, chore)

## Anti-Patterns

- Use `interface{}` without justification
- Suppress type errors (`as any`)
- Delete failing tests to "pass"
- Giant commits (3+ files = split)

## Main Entry Points

- CLI: `cmd/nexus/main.go` (Cobra-based)
- Server: `coordination.StartServer()` in `pkg/coordination/server.go`
- Agent: `pkg/agent/node.go`

## Configuration

- `.nexus/config.yaml` - Workspace service definitions
- `schema/config.schema.json` - JSON schema for config validation
- `.golangci.yml` - Linter configuration
