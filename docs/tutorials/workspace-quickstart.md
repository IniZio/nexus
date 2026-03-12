# Workspace Quickstart

Nexus Workspaces provide isolated, reproducible development environments that work seamlessly with your existing tools. Think of them as remote SSH VMs that automatically intercept your commands.

## Prerequisites

- Docker installed and running
- Nexus CLI built (`pnpm run build`)

## Creating a Workspace

```bash
nexus workspace create myproject
```

This creates a Docker-based workspace with:
- Isolated filesystem
- Git worktree integration
- SSH agent forwarding

## Activating a Workspace

Once created, activate the workspace to enable auto-intercept:

```bash
nexus workspace use myproject
```

When a workspace is active, all commands automatically run inside it. No prefix needed.

## Working Seamlessly

With the workspace activated, work exactly as you normally would:

```bash
# Start services
docker-compose up -d

# Install dependencies
npm install
npm install lodash

# Run development server
npm run dev

# Run tests
npm test

# Check processes
ps aux
```

### Escaping to Host

Need to run a command on your host machine instead Prefix with `HOST:`?:

```bash
HOST:npm install -g some-tool    # Install globally on host
HOST:docker ps                   # Check host Docker
HOST:git status                   # Check main repo status
```

## Checking Status

```bash
# See current workspace context
nexus workspace status

# List all workspaces
nexus workspace list
```

## Deactivating Workspace

```bash
# Remove workspace context (commands run on host again)
nexus workspace use --clear
```

## Quick Reference

| Command | Description |
|---------|-------------|
| `nexus workspace create <name>` | Create new workspace |
| `nexus workspace use <name>` | Activate workspace (enable auto-intercept) |
| `nexus workspace use --clear` | Deactivate workspace |
| `nexus workspace list` | List all workspaces |
| `nexus workspace status` | Show current context |
| `HOST:<command>` | Run command on host instead |

## Next Steps

- [CLI Reference](../reference/nexus-cli.md) - Full command documentation
- [Installation Guide](./installation.md) - Detailed setup instructions
