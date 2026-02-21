# Enforcer Configuration

Nexus Enforcer is configured via `.nexus/enforcer-config.json` in your project root.

## Schema

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "enabled": { "type": "boolean" },
    "plugin": { "type": "string" },
    "rules": { "type": "object" },
    "boulder": { "type": "object" },
    "allowedPaths": { "type": "array", "items": { "type": "string" } }
  }
}
```

## Configuration Options

### enabled

Enable or disable the enforcer.

```json
{
  "enabled": true
}
```

Default: `true`

### plugin

Specify which plugin to use.

```json
{
  "plugin": "opencode"
}
```

Valid values: `opencode`, `claude`, `cursor`

### rules

Configure enforcement rules.

```json
{
  "rules": {
    "ruleName": {
      "enabled": true,
      "message": "Custom error message"
    }
  }
}
```

#### Built-in Rules

| Rule | Description |
|------|-------------|
| `noDirectFileCreation` | Block file creation outside workspaces |
| `requireTaskCompletion` | Require minimum tasks before completion |

### boulder

Configure Boulder system settings.

```json
{
  "boulder": {
    "enabled": true,
    "idleThresholdMs": 60000,
    "minTasksInQueue": 5,
    "nextTasksCount": 3
  }
}
```

| Setting | Description | Default |
|---------|-------------|---------|
| `enabled` | Enable Boulder system | `true` |
| `idleThresholdMs` | Idle time before enforcement (ms) | `60000` |
| `minTasksInQueue` | Minimum tasks to maintain | `5` |
| `nextTasksCount` | Tasks to show in prompts | `3` |

### allowedPaths

Paths that bypass enforcement.

```json
{
  "allowedPaths": [
    "/home/user/nexus-dev/",
    "/tmp/"
  ]
}
```

## Examples

### Basic Configuration

```json
{
  "enabled": true,
  "boulder": {
    "enabled": true
  }
}
```

### Strict Configuration

```json
{
  "enabled": true,
  "rules": {
    "noDirectFileCreation": {
      "enabled": true,
      "message": "Files can only be created in approved workspaces"
    },
    "requireTaskCompletion": {
      "enabled": true,
      "minTasksBeforeCompletion": 5
    }
  },
  "boulder": {
    "enabled": true,
    "idleThresholdMs": 30000,
    "minTasksInQueue": 10
  }
}
```

### Permissive Configuration

```json
{
  "enabled": true,
  "rules": {
    "noDirectFileCreation": {
      "enabled": false
    }
  },
  "boulder": {
    "enabled": false
  }
}
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `NEXUS_ENFORCER_CONFIG` | Path to config file |
| `NEXUS_BOULDER_DIR` | Path to Boulder state directory |

## Local Overrides

Create `.nexus/enforcer-config.local.json` for local overrides:

```json
{
  "boulder": {
    "enabled": false
  }
}
```

Local config is merged with base config and takes precedence.
