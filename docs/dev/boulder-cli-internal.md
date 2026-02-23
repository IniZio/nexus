# Boulder CLI - Internal Documentation

**Status:** Experimental/Internal  
**Component:** Boulder Enforcer

---

## Overview

Boulder is an experimental/internal enforcement system for continuous task enforcement with idle detection. It is **not** intended for general user-facing documentation.

This document is for internal development reference only.

---

## Command Reference

### Status

```bash
boulder status [options]
```

**Options:**

| Flag | Description | Default |
|------|-------------|---------|
| `--workspace` | Show status for specific workspace | (all) |
| `--verbose, -v` | Show detailed status | false |

### Pause/Resume

```bash
# Pause global enforcement
boulder pause

# Pause specific workspace
boulder pause --workspace <name>

# Resume global enforcement
boulder resume

# Resume specific workspace
boulder resume --workspace <name>
```

### Configuration

```bash
# Get configuration value
boulder config get <key>

# Set configuration value
boulder config set <key> <value>

# Get all configuration
boulder config get
```

**Configuration Keys:**

| Key | Type | Description | Default |
|-----|------|-------------|---------|
| `idle_threshold` | number | Idle threshold in seconds | 30 |
| `enforcement_level` | string | (strict, normal, lenient) | normal |
| `workflows.enabled` | boolean | Enable workflow enforcement | true |
| `workflows.require_tests` | boolean | Require tests for commits | false |
| `notifications.enabled` | boolean | Enable notifications | true |
| `notifications.webhooks` | string[] | Webhook URLs | [] |

### Logs

```bash
boulder logs [options]
```

**Options:**

| Flag | Description | Default |
|------|-------------|---------|
| `--follow, -f` | Follow log output | false |
| `--level` | Filter by level (debug, info, warn, error) | all |
| `--workspace` | Filter by workspace | all |

---

## Configuration File

```yaml
boulder:
  enforcement_level: normal
  idle_threshold: 30
  workflows:
    enabled: true
    require_tests: false
  notifications:
    enabled: true
    webhooks: []
```

---

## Notes

- Boulder is experimental and subject to change
- Commands are accessed via the standalone `boulder` CLI, not via `nexus`
- User-facing documentation should not mention Boulder - use the Nexus workspace commands instead
