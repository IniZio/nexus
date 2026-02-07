# Pulse - Linear-Inspired Project Management

Pulse is a fast, keyboard-first project management tool inspired by Linear. Manage issues, cycles, and team velocity.

## Features

### Issue Management
- **Create issues** with title, description, priority, labels, and story point estimates
- **Workflow states**: Backlog → To Do → In Progress → Done
- **Priority levels**: Urgent (1), High (2), Medium (3), Low (4)
- **Labels**: Organize with custom labels (bug, feature, etc.)
- **Sub-issues**: Break down large tasks into smaller pieces
- **Relations**: Track blocking, duplicates, and related issues

### Project Organization
- **Workspaces**: Create multiple workspaces for different projects
- **Teams**: Organize team members within workspaces
- **Cycles**: Time-boxed iterations for sprint planning
- **Custom views**: Kanban board, list, and calendar views

### Analytics & Metrics
- **Velocity tracking**: Points completed per cycle
- **Cycle time**: Time from "In Progress" to "Done"
- **Lead time**: Time from creation to completion
- **Completion rate**: Percentage of issues completed
- **Bug count**: Track quality metrics

### Keyboard Shortcuts
| Shortcut | Action |
|----------|--------|
| `C` | Create new issue |
| `/` | Quick search |
| `Esc` | Close modal |
| `J/K` | Navigate issues |
| `N` | Move to next status |
| `P` | Move to previous status |

## Quick Start

```bash
# Start Pulse server
pulse start --addr localhost:3002

# Open browser
open http://localhost:3002
```

## API Reference

### Workspaces

```bash
# List workspaces
curl http://localhost:3002/api/workspaces

# Create workspace
curl -X POST http://localhost:3002/api/workspaces \
  -H "Content-Type: application/json" \
  -d '{"name": "My Project", "description": "Project description"}'

# Update workspace
curl -X PUT http://localhost:3002/api/workspaces/{id} \
  -H "Content-Type: application/json" \
  -d '{"name": "Updated Name"}'

# Delete workspace
curl -X DELETE http://localhost:3002/api/workspaces/{id}
```

### Issues

```bash
# List issues
curl http://localhost:3002/api/issues?workspace_id={id}

# Create issue
curl -X POST http://localhost:3002/api/issues \
  -H "Content-Type: application/json" \
  -d '{
    "workspace_id": "{id}",
    "title": "Fix login bug",
    "description": "Login fails on mobile",
    "status": "backlog",
    "priority": 2,
    "estimate": 3,
    "labels": ["bug"]
  }'

# Update issue
curl -X PUT http://localhost:3002/api/issues/{id} \
  -H "Content-Type: application/json" \
  -d '{"status": "in_progress"}'

# Delete issue
curl -X DELETE http://localhost:3002/api/issues/{id}
```

### Metrics

```bash
# Get velocity metrics
curl http://localhost:3002/api/metrics?workspace_id={id}
```

Response:
```json
{
  "cycle_id": "cycle-123",
  "points_planned": 50,
  "points_completed": 35,
  "cycle_time_hours": 24.5,
  "lead_time_hours": 48.2,
  "bug_count": 3,
  "issues_created": 15,
  "issues_completed": 10,
  "average_estimate": 3.3,
  "completion_rate": 66.7
}
```

### Search

```bash
# Search issues
curl http://localhost:3002/api/search?q=login
```

## Configuration

### Environment Variables
| Variable | Description | Default |
|----------|-------------|---------|
| `PULSE_DATA_DIR` | Data directory | `./.pulse-data` |
| `PULSE_ADDR` | Listen address | `localhost:3002` |

## Development

```bash
# Run tests
go test ./e2e/pulse_test.go -v

# Run with coverage
go test ./e2e/pulse_test.go -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## Architecture

```
pulse/
├── cmd/pulse/
│   └── main.go           # CLI entry point
└── cmd/pulse/internal/
    └── server/
        └── server.go     # Web server & API
```

## Metrics Explained

### Velocity
The number of story points completed per cycle. Used to predict future capacity.

### Cycle Time
Time from when work begins (In Progress) to completion (Done). Lower is better.

### Lead Time
Time from issue creation to completion. Includes time in backlog.

### Completion Rate
Percentage of issues that reach Done status. Higher indicates better planning.

## Integration with Nexus Workspaces

Pulse integrates with Nexus for seamless development:

```bash
# Create workspace for an issue
nexus branch create feature-login

# Start development
nexus branch up feature-login

# Work on issue in isolated environment

# Track progress in Pulse
```

## License

MIT
