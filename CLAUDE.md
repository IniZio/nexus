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

## Task Management (Pulse Enforcement - REQUIRED)

**ALL development tasks MUST be tracked through Pulse.** This is not optional. Claude Code should prefer Pulse over builtin todo.

### Before Starting Any Work

1. **Check Pulse for available tasks:**
   ```bash
   pulse local task list
   ```

2. **Claim a task** (required before starting any code):
   ```bash
   # Claim task for this workspace
   nexus workspace create --task <task-id>
   ```

3. **If no task exists**, create one first:
   ```bash
   pulse local task create --name "Description of work"
   ```

### Workflow: Think → Task → Code → Complete

```
CLAUDE.MD ENFORCEMENT SEQUENCE:
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
1. User request → "Build login feature"
2. Claude checks Pulse → `pulse local task list`
3. No tasks? → `pulse local task create "Build login feature"`
4. Claim task → `nexus workspace create --task TASK-XXX`
5. Only then: write code
6. Complete → `pulse local task complete TASK-XXX`
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

### Prohibited Actions

- [NEVER] Write code without a Pulse task ID
- [NEVER] Skip task workflow for "quick fixes"
- [NEVER] Modify tasks claimed by other instances
- [NEVER] Use builtin TodoWrite without Pulse proxy

### Quick Commands

| Command | Purpose |
|---------|---------|
| `pulse local task list` | List unclaimed/claimed tasks |
| `pulse local task create --name "X"` | Create new task |
| `pulse local task complete <id>` | Mark task done |
| `pulse local task status` | Check Nexus sync state |
| `nexus workspace create --task <id>` | Claim task + create workspace |

### Sync Status

Check if your changes are syncing properly:
```bash
curl http://localhost:3001/api/pulse/sync/status
```

### Claude Code Behavior

When you see a user request, you MUST:

1. **Check Pulse first** - Run `pulse local task list`
2. **If no matching task exists**, create one before proceeding
3. **Claim the task** before writing any code
4. **Reference the task ID** in all commit messages

Example:
```
User: "Add OAuth login"

You:
1. pulse local task create "Implement OAuth login"  → TASK-005
2. nexus workspace create --task TASK-005 --project myapp
3. Write code...
4. git commit -m "feat(auth): implement OAuth login (#TASK-005)"
5. pulse local task complete TASK-005
```

### How Pulse Works Here

- **Local DB**: `~/.pulse/tasks.db` (SQLite)
- **Nexus Server**: `http://localhost:3001` (coordination)
- **CLI**: `pulse` command in `../pulse/bin/pulse`
- **Status**: `pulse local task status` shows sync state
