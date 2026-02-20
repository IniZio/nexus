# Workspaces

Workspaces in Nexus provide isolated environments for managing tasks and projects.

## Creating a Workspace

To create a new workspace, use the `nexus workspace create` command:

```bash
nexus workspace create my-workspace
```

## Managing Workspaces

List all workspaces:
```bash
nexus workspace list
```

Switch to a workspace:
```bash
nexus workspace use my-workspace
```

Delete a workspace:
```bash
nexus workspace delete my-workspace
```

## Lifecycle Scripts

Workspaces support lifecycle scripts for automating setup and teardown. Create a `.nexus/lifecycle.json` file in your workspace:

```json
{
  "version": "1.0",
  "hooks": {
    "pre-start": [
      {
        "name": "validate-env",
        "command": "echo",
        "args": ["Validating environment..."],
        "timeout": 10
      }
    ],
    "post-start": [
      {
        "name": "start-services",
        "command": "docker",
        "args": ["compose", "up", "-d"],
        "timeout": 60
      }
    ],
    "pre-stop": [
      {
        "name": "backup-data",
        "command": "./scripts/backup.sh",
        "timeout": 30
      }
    ],
    "post-stop": [
      {
        "name": "cleanup",
        "command": "docker",
        "args": ["compose", "down"],
        "timeout": 60
      }
    ]
  }
}
```

### Hook Types

| Hook | Description | Use Case |
|------|-------------|----------|
| `pre-start` | Runs before daemon starts | Validate config, check dependencies |
| `post-start` | Runs after daemon starts | Start docker compose, initialize services |
| `pre-stop` | Runs before daemon stops | Save state, graceful shutdown |
| `post-stop` | Runs after daemon stops | Cleanup resources, stop services |

### Hook Configuration

Each hook supports:
- `name`: Descriptive name for the hook
- `command`: Command to execute
- `args`: Command arguments (optional)
- `env`: Environment variables (optional)
- `timeout`: Timeout in seconds (default: 30)

## Service Port Awareness

The workspace daemon automatically detects and tracks service ports:

```bash
# List workspace ports
nexus workspace ports my-workspace

# Common service ports:
# - SSH: 22
# - Web: 3000, 3001, 5173
# - API: 5000, 8080
# - Database: 5432 (PostgreSQL), 6379 (Redis)
```

### Docker Compose Integration

When using docker-compose, lifecycle hooks automatically detect exposed ports:

```yaml
# docker-compose.yml
services:
  web:
    ports:
      - "3000:3000"
  api:
    ports:
      - "8080:8080"
```

The workspace daemon will:
1. Start services via `docker compose up -d` (post-start hook)
2. Detect exposed ports automatically
3. Stop services via `docker compose down` (post-stop hook)

## Best Practices

- Use descriptive names for workspaces
- Keep workspaces focused on specific projects or goals
- Regularly clean up unused workspaces
- Use lifecycle hooks for repeatable setup
- Configure appropriate timeouts for long-running operations
