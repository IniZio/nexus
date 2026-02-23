# Lifecycle Scripts

Nexus supports lifecycle hooks similar to nexus-old, allowing you to run commands at specific points in the workspace lifecycle.

## Configuration

Create `.nexus/lifecycle.json` in your workspace root:

```json
{
  "version": "1.0",
  "hooks": {
    "pre-start": [
      {
        "name": "install-dependencies",
        "command": "npm",
        "args": ["install"],
        "timeout": 120
      }
    ],
    "post-start": [
      {
        "name": "start-services",
        "command": "docker",
        "args": ["compose", "up", "-d"],
        "env": {
          "COMPOSE_PROJECT_NAME": "my-app"
        }
      }
    ],
    "pre-stop": [
      {
        "name": "backup-data",
        "command": "./scripts/backup.sh"
      }
    ],
    "post-stop": [
      {
        "name": "cleanup",
        "command": "docker",
        "args": ["compose", "down"]
      }
    ]
  }
}
```

## Hook Stages

### pre-start
Runs before the workspace daemon starts accepting connections. Useful for:
- Installing dependencies
- Running database migrations
- Setting up environment

### post-start
Runs after the daemon is ready. Useful for:
- Starting docker-compose services
- Running development servers
- Initializing background processes

### pre-stop
Runs before the daemon shuts down. Useful for:
- Graceful service shutdown
- Data backup
- State preservation

### post-stop
Runs after the daemon stops. Useful for:
- Cleanup operations
- Removing temporary files
- Docker compose down

## Docker Compose Example

For hanlun-lms style projects:

```json
{
  "version": "1.0",
  "hooks": {
    "post-start": [
      {
        "name": "start-database",
        "command": "docker",
        "args": ["compose", "up", "-d", "postgres", "redis"],
        "timeout": 60
      },
      {
        "name": "run-migrations",
        "command": "npm",
        "args": ["run", "migrate"],
        "timeout": 30
      }
    ],
    "pre-stop": [
      {
        "name": "stop-services",
        "command": "docker",
        "args": ["compose", "down"],
        "timeout": 30
      }
    ]
  }
}
```

## Environment Variables

Pass environment variables to hooks:

```json
{
  "name": "build-app",
  "command": "npm",
  "args": ["run", "build"],
  "env": {
    "NODE_ENV": "production",
    "API_URL": "https://api.example.com"
  }
}
```

## Timeouts

Default timeout is 30 seconds. Override per hook:

```json
{
  "name": "long-operation",
  "command": "npm",
  "args": ["install"],
  "timeout": 300
}
```

## Error Handling

If a hook fails:
1. The error is logged
2. Subsequent hooks in the same stage are skipped
3. The daemon continues (pre-start/post-start) or stops (pre-stop/post-stop)

Check logs for hook execution details:
```bash
docker logs nexus-<workspace-name>
```
