# Architecture

Nexus is organized into four main components that work together to provide isolated containerized workspaces with task coordination capabilities.

## System Overview

```
┌─────────────────────────────────────────────────────────────┐
│                      Nexus CLI                              │
├─────────────────────────────────────────────────────────────┤
│  Commands: init, workspace, task, agent, template, etc.    │
└─────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
    ┌─────────────┐ ┌─────────────┐ ┌─────────────┐
    │   Docker     │ │  Coordination│ │   Skills    │
    │  Provider    │ │   Manager   │ │  & Hooks    │
    └─────────────┘ └─────────────┘ └─────────────┘
```

## Core Components

### Docker Provider (`internal/docker/provider.go`)

The Docker provider manages container lifecycle:

- **Container Creation:** Ubuntu 22.04 containers with SSH server, git, and sudo
- **User Setup:** `dev` user with passwordless sudo
- **Project Mounting:** Project mounted at `/workspace` inside container
- **Port Mapping:** SSH port mapped to random available host port

```go
type Provider struct {
    client *docker.Client
}

func (p *Provider) Create(name, worktreePath string) error {
    // Creates container with SSH access
}
```

### Workspace Manager (`internal/workspace/manager.go`)

Integrates git worktrees with Docker containers:

- Creates git worktree at `.worktree/<name>/`
- Creates branch `nexus/<workspace-name>`
- Mounts worktree to container (not project root)
- Syncs changes between workspace and main branch

### Task Coordination (`pkg/coordination/`)

SQLite-based persistent storage:

- **Tasks:** With dependencies, status tracking, and verification criteria
- **Agents:** Registration with capabilities, assignment, and idle/busy states
- **Ralph Loop:** Feedback collection and auto skill updates

**Database Schema:**
```sql
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    title TEXT,
    status TEXT,  -- pending, assigned, in_progress, verification, completed
    ...
);

CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    name TEXT,
    capabilities TEXT,
    status TEXT,
    ...
);
```

### Skills & Hooks (`.nexus/`)

- **Hooks:** up.sh, down.sh, post-create.sh for lifecycle events
- **Agent Configs:** OpenCode, Claude, Codex configurations
- **System Prompts:** Rules and prompts for agent behavior
- **Skills:** Located at `~/.config/opencode/skills/nexus/`

## Data Flow

1. **Workspace Creation:**
   ```
   nexus workspace create → git worktree → Docker container → SSH port
   ```

2. **Task Lifecycle:**
   ```
   Create → Assign → Start → Verify → Approve → Complete
   ```

3. **Ralph Loop:**
   ```
   Session → Feedback → Pattern Detection → Skill Update
   ```

## File Structure

```
nexus/
├── cmd/nexus/
│   └── main.go                    # CLI entry point
├── internal/
│   ├── docker/
│   │   └── provider.go           # Container management
│   └── workspace/
│       └── manager.go             # Worktree integration
├── pkg/
│   ├── coordination/
│   │   ├── types.go
│   │   ├── manager.go            # Task/Agent management
│   │   ├── task_manager.go
│   │   └── ralph.go              # Auto improvement
│   ├── git/
│   │   └── worktree.go           # Git worktree operations
│   └── template/
│       └── engine.go              # Template rendering
└── .nexus/
    ├── config.yaml
    ├── worktrees/                # Git worktrees
    ├── hooks/
    ├── agents/
    └── templates/
```

## Dependencies

- `docker/docker` - Container management
- `spf13/cobra` - CLI framework
- `mattn/go-sqlite3` - Persistence
- `stretchr/testify` - Testing

## Lifecycle Scripts

Nexus workspaces support lifecycle scripts for automation:

### Available Hooks

| Hook | Location | Trigger |
|------|----------|---------|
| Pre-create | `.nexus/hooks/pre-create.sh` | Before workspace creation |
| Post-create | `.nexus/hooks/post-create.sh` | After workspace creation |
| Pre-up | `.nexus/hooks/pre-up.sh` | Before workspace starts |
| Post-up | `.nexus/hooks/post-up.sh` | After workspace starts |
| Pre-down | `.nexus/hooks/pre-down.sh` | Before workspace stops |
| Post-down | `.nexus/hooks/post-down.sh` | After workspace stops |

### Example Hook

```bash
#!/bin/bash
# .nexus/hooks/post-up.sh

echo "Workspace is ready!"
echo "Starting dev server..."

# Start your development server
cd /workspace
npm run dev &
```

### Service Port Awareness

Workspaces automatically detect and manage service ports:

- **Port Detection:** Scans for common services (Node, Python, Go, PostgreSQL)
- **Port Registry:** Tracks all exposed ports in `.nexus/ports.json`
- **Port Forwarding:** SSH tunnel setup for remote debugging
- **Port Cleanup:** Automatically releases ports on workspace shutdown

```bash
# View active ports
nexus workspace ports <workspace-name>

# Example output:
# Workspace: my-feature
# SSH: 32768
# Web Server: 32769 (3000 internal)
# API: 32770 (5000 internal)
# Database: 32771 (5432 internal)
```
