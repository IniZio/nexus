# Nexus Daemon (nexusd)

The Nexus daemon (`nexusd`) provides Docker-based workspace management with SSH access, lifecycle hooks, and the `nexus` CLI.

## Overview

`nexusd` is a Go-based daemon that manages isolated development workspaces using Docker containers. It provides:

- **Docker-based workspaces** - Each workspace runs in its own container
- **SSH access** - Direct SSH into workspaces with agent forwarding
- **Port allocation** - Automatic port mapping (SSH: 32800-34999)
- **Git worktree integration** - Workspaces are linked to git worktrees
- **Lifecycle hooks** - Automated setup and teardown scripts
- **Checkpoint/restore** - Save and restore workspace state

## Architecture

```
┌─────────────┐      HTTP/gRPC      ┌─────────────────┐
│   nexus     │ ◄─────────────────► │    nexusd       │
│    CLI      │                     │    daemon       │
└─────────────┘                     └────────┬────────┘
                                             │
                                     ┌───────▼────────┐
                                     │ Docker Backend │
                                     │   (provider)   │
                                     └───────┬────────┘
                                             │
                                     ┌───────▼────────┐
                                     │   Workspace    │
                                     │   Containers   │
                                     └────────────────┘
```

## Quick Start

### Build

```bash
# Build the daemon
go build -o nexusd ./cmd/daemon

# Build the CLI
go build -o nexus ./cmd/cli
```

### Run the Daemon

```bash
# Start the daemon
./nexusd daemon

# Or with custom port
./nexusd daemon --port 8080
```

### Use the CLI

```bash
# Create a workspace
nexus workspace create myproject

# List workspaces
nexus workspace list

# SSH into workspace
nexus workspace ssh myproject

# Execute command in workspace
nexus workspace exec myproject -- ls -la

# Stop workspace
nexus workspace stop myproject

# Delete workspace
nexus workspace delete myproject
```

## Commands

### Workspace Management

| Command | Description |
|---------|-------------|
| `nexus workspace create <name>` | Create a new workspace |
| `nexus workspace start <name>` | Start a stopped workspace |
| `nexus workspace stop <name>` | Stop a running workspace |
| `nexus workspace delete <name>` | Delete a workspace |
| `nexus workspace list` | List all workspaces |
| `nexus workspace status <name>` | Show workspace details |
| `nexus workspace ssh <name>` | SSH into workspace interactively |
| `nexus workspace exec <name> -- <cmd>` | Execute command in workspace |
| `nexus workspace use <name>` | Set active workspace |
| `nexus workspace use --clear` | Clear active workspace |

### Daemon Management

| Command | Description |
|---------|-------------|
| `nexus daemon start` | Start the nexusd daemon |
| `nexus daemon stop` | Stop the daemon |
| `nexus daemon status` | Check daemon status |

### Other Commands

| Command | Description |
|---------|-------------|
| `nexus boulder status` | Check boulder enforcement status |
| `nexus sync` | File synchronization commands |
| `nexus doctor` | Diagnose issues |

## Configuration

The daemon stores state in `~/.nexus/`:

- `~/.nexus/daemon.sock` - Unix socket for local communication
- `~/.nexus/workspaces/` - Workspace metadata
- `~/.nexus/session/` - Active session tracking

## Workspace Structure

Each workspace has:

- **Container** - Docker container with isolated filesystem
- **Worktree** - Git worktree in `.worktree/<name>/`
- **SSH Access** - Auto-allocated SSH port (32800-34999)
- **Branch** - Associated git branch (`nexus/<name>`)

## Docker Integration

Workspaces use Docker containers with:

- OpenSSH server for SSH access
- User SSH key injection
- SSH agent forwarding support
- Volume persistence
- Port auto-allocation

## Development

### Running Tests

```bash
# Run all tests
go test ./...

# Skip integration tests
go test ./... -short

# Run specific package tests
go test ./internal/docker/...
```

### Project Structure

```
packages/nexusd/
├── cmd/
│   ├── daemon/          # Daemon entry point
│   └── cli/             # CLI entry point
├── internal/
│   ├── cli/             # CLI commands
│   ├── docker/          # Docker backend
│   ├── checkpoint/      # State management
│   ├── git/             # Git operations
│   ├── sync/            # File sync (mutagen)
│   └── ...
├── pkg/
│   ├── handlers/        # HTTP/gRPC handlers
│   ├── server/          # Server implementation
│   └── workspace/       # Workspace types
└── test/                # Test utilities
```

## See Also

- [Workspace Quickstart](../../docs/tutorials/workspace-quickstart.md)
- [Nexus CLI Reference](../../docs/reference/nexus-cli.md)
- [Installation Guide](../../docs/tutorials/installation.md)
