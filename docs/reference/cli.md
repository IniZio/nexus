# CLI Reference

## Global Flags

| Flag | Description |
|------|-------------|
| `--help` | Show help |
| `--version` | Show version |
| `--config` | Config file path |

## Commands

### nexus init

Initialize Nexus in the current project.

```bash
nexus init
```

Creates `.nexus/` directory with default configuration.

### nexus workspace

Manage containerized workspaces.

#### nexus workspace create <name>

Create a new workspace.

```bash
nexus workspace create <name>
nexus workspace create <name> --template <template>
```

| Flag | Description |
|------|-------------|
| `--template` | Template to use (default: empty) |

#### nexus workspace up <name>

Start a workspace.

```bash
nexus workspace up <name>
```

#### nexus workspace down <name>

Stop a workspace.

```bash
nexus workspace down <name>
```

#### nexus workspace destroy <name>

Destroy a workspace and its worktree.

```bash
nexus workspace destroy <name>
```

#### nexus workspace list

List all workspaces.

```bash
nexus workspace list
```

#### nexus workspace ports <name>

Show port mappings for a workspace.

```bash
nexus workspace ports <name>
```

#### nexus workspace sync <name>

Sync workspace with main branch.

```bash
nexus workspace sync <name>
```

### nexus task

Manage tasks with verification workflow.

#### nexus task create "Title"

Create a new task.

```bash
nexus task create "Title" -d "Description"
nexus task create "Title" -p high
```

| Flag | Description |
|------|-------------|
| `-d, --description` | Task description |
| `-p, --priority` | Priority (low, medium, high) |

#### nexus task assign <task-id> <agent-id>

Assign task to an agent.

```bash
nexus task assign <task-id> <agent-id>
```

#### nexus task verify <task-id>

Submit task for verification.

```bash
nexus task verify <task-id>
```

#### nexus task approve <task-id>

Approve completed task.

```bash
nexus task approve <task-id>
```

#### nexus task reject <task-id>

Reject task for rework.

```bash
nexus task reject <task-id> -r "reason"
```

#### nexus task list

List all tasks.

```bash
nexus task list
```

### nexus agent

Manage agents.

#### nexus agent register <name>

Register a new agent.

```bash
nexus agent register <name> -c go,python,docker
```

| Flag | Description |
|------|-------------|
| `-c, --capabilities` | Comma-separated capabilities |

#### nexus agent list

List all registered agents.

```bash
nexus agent list
```

### nexus template

Manage workspace templates.

#### nexus template list

List available templates.

```bash
nexus template list
```

### nexus telemetry

Manage local telemetry.

```bash
nexus telemetry status
nexus telemetry on
nexus telemetry off
nexus telemetry purge
```

### nexus stats

Show usage statistics.

```bash
nexus stats
nexus stats --week
nexus stats --month
```

### nexus insights

Show detected patterns and insights.

```bash
nexus insights
nexus insights --slow
nexus insights --errors
```
