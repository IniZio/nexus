# Boulder CLI Reference

The Boulder CLI (`boulder`) manages the enforcement system. It provides commands for controlling the Boulder, viewing statistics, and configuring enforcement behavior.

## Commands

### boulder status

Show current Boulder status including iteration, tasks, and idle time.

```bash
boulder status
```

Output includes:
- Current iteration number
- Session duration and idle time
- Task queue statistics (total, pending, active, done, paused)
- Completion stats (tasks created, tasks completed, work time)
- Configuration settings

### boulder reset

Reset Boulder state for testing or starting fresh.

```bash
boulder reset
```

This will:
- Reset iteration to 0
- Clear the task queue
- Reset session start time
- Clear all statistics

### boulder enforce

Manually trigger enforcement. This adds new tasks to the queue and increments the iteration counter.

```boulder enforce
```

This will:
- Increment iteration counter
- Add 10 new tasks to the queue
- Reset last activity timestamp

### boulder config

Show or configure Boulder settings.

```bash
# Show all settings
boulder config

# Set a specific value
boulder config <key> <value>
```

#### Configuration Keys

| Key | Description | Default |
|-----|-------------|---------|
| `minTasksInQueue` | Minimum tasks to maintain in queue | 5 |
| `idleThresholdMs` | Idle time before enforcement triggers | 60000 |
| `nextTasksCount` | Number of next tasks to show | 3 |

#### Examples

```bash
# Increase minimum queue size
boulder config minTasksInQueue 10

# Increase idle threshold to 2 minutes
boulder config idleThresholdMs 120000

# Show more next tasks
boulder config nextTasksCount 5
```

### boulder help

Show help message.

```bash
boulder help
```

## Files

The Boulder CLI stores state in `.nexus/boulder/` directory:

| File | Description |
|------|-------------|
| `.nexus/boulder/state.json` | Current Boulder state |
| `.nexus/boulder/tasks.json` | Task queue |
| `.nexus/boulder/config.json` | Configuration |

## Integration with Plugins

The Boulder CLI can be used alongside IDE plugins:

```bash
# Check status from terminal
boulder status

# Trigger enforcement manually
boulder enforce
```

## Exit Codes

| Code | Description |
|------|-------------|
| 0 | Success |
| 1 | Unknown command or error |
