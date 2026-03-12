# Environment Quickstart

Nexus environments provide isolated development environments managed through `nexus environment` commands.

## Mental Model

- `project` -> repository-level context (command group scaffold; list workflow not implemented yet)
- `branch` -> active branch context (command group scaffold; use workflow not implemented yet)
- `version` -> reserved command group for upcoming version workflows
- `environment` -> isolated development runtime (`nexus environment ...`)

## Prerequisites

- Docker installed and running
- Nexus CLI installed (see `installation.md`)

## Create an Environment

```bash
nexus environment create myproject
```

This creates an environment with:
- Isolated filesystem
- Git worktree integration
- SSH agent forwarding

## Check Environments

```bash
# List all environments
nexus environment list

# Show detailed status for one environment
nexus environment status myproject
```

`nexus environment status <name>` requires an environment name.

## Connect to an Environment

Use SSH for an interactive shell:

```bash
nexus environment ssh myproject
```

Or run a single command without opening a shell:

```bash
nexus environment exec myproject -- pwd
```

## Optional Session Context

You can set an active environment context:

```bash
nexus environment use myproject
```

Clear it later:

```bash
nexus environment use --clear
```

`environment use` sets an active environment context for commands that support context-based routing in the current session. It does not replace explicit environment commands like `environment ssh <name>` or `environment exec <name> -- <command>`, which remain the most predictable options.

## Start/Stop Lifecycle

```bash
nexus environment start myproject
nexus environment stop myproject
```

## Delete an Environment

```bash
nexus environment delete myproject
```

Use `-f` to skip confirmation:

```bash
nexus environment delete myproject -f
```

## Quick Reference

| Command | Description |
|---------|-------------|
| `nexus environment create <name>` | Create new environment |
| `nexus environment ssh <name>` | Open interactive shell in environment |
| `nexus environment exec <name> -- <command>` | Run one command in environment |
| `nexus environment list` / `nexus environment ls` | List all environments |
| `nexus environment status <name>` | Show detailed environment status |

## Next Steps

- [CLI Reference](../reference/nexus-cli.md) - Full command documentation
- [Installation Guide](./installation.md) - Detailed setup instructions
