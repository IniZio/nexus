# Workspace Quickstart

Nexus workspaces provide isolated development environments managed through `nexus workspace` commands.

## Prerequisites

- Docker installed and running
- Nexus CLI installed (see `installation.md`)

## Creating a Workspace

```bash
nexus workspace create myproject
```

This creates a workspace with:
- Isolated filesystem
- Git worktree integration
- SSH agent forwarding

## Check Workspaces

```bash
# List all workspaces
nexus workspace list

# Show detailed status for one workspace
nexus workspace status myproject
```

`nexus workspace status` requires a workspace name.

## Connect to a Workspace

Use SSH for an interactive shell:

```bash
nexus workspace ssh myproject
```

Or run a single command without opening a shell:

```bash
nexus workspace exec myproject -- pwd
```

## Optional Session Context

You can set an active workspace context:

```bash
nexus workspace use myproject
```

Clear it later:

```bash
nexus workspace use --clear
```

`workspace use` stores session context and prints guidance about host escape (`HOST:`). For predictable execution, continue using `workspace ssh` and `workspace exec`.

## Start/Stop Lifecycle

```bash
nexus workspace start myproject
nexus workspace stop myproject
```

## Delete a Workspace

```bash
nexus workspace delete myproject
```

Use `-f` to skip confirmation:

```bash
nexus workspace delete myproject -f
```

## Quick Reference

| Command | Description |
|---------|-------------|
| `nexus workspace create <name>` | Create new workspace |
| `nexus workspace ssh <name>` | Open interactive shell in workspace |
| `nexus workspace exec <name> -- <command>` | Run one command in workspace |
| `nexus workspace list` | List all workspaces |
| `nexus workspace status <name>` | Show detailed workspace status |
| `nexus workspace use <name>` | Set active workspace context |
| `nexus workspace use --clear` | Clear active workspace context |

## Next Steps

- [CLI Reference](../reference/nexus-cli.md) - Full command documentation
- [Installation Guide](./installation.md) - Detailed setup instructions
