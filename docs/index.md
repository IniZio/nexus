# Nexus Workspace System

Nexus is a containerized workspace management system that provides isolated development environments for multi-agent software development.

## Features

### Container Workspaces
Each workspace runs in an isolated Docker container with its own git branch, file system, and network. Create multiple workspaces for different features without code conflicts.

**Commands:**
```bash
nexus workspace create <name>
nexus workspace up <name>
nexus workspace down <name>
nexus workspace destroy <name>
```

### Multi-Service Templates
Pre-configured development environments for common stacks:

- **node-postgres** - React/Vue + Node.js + PostgreSQL
- **python-postgres** - Flask/Django + PostgreSQL
- **go-postgres** - Go API + PostgreSQL

### Task Verification System
Mandatory verification workflow ensures quality before completion:

```bash
nexus task create "Title" -d "Description"
nexus task assign <task-id> <agent-id>
nexus task verify <task-id>
nexus task approve <task-id>
```

### Ralph Loop
Auto-improvement system that collects feedback, detects patterns, and updates skills automatically.

### Agent Management
Register agents with capabilities and assign tasks:

```bash
nexus agent register <name> -c <capabilities>
nexus agent list
```

## Quick Start

1. **Initialize Nexus:**
   ```bash
   cd your-project
   nexus init
   ```

2. **Create a Workspace:**
   ```bash
   nexus workspace create feature-x --template node-postgres
   ```

3. **Start Coding:**
   ```bash
   nexus workspace up feature-x
   nexus workspace ports feature-x
   ```

## Documentation

### Tutorials
- [Installation](tutorials/installation.md)
- [Your First Workspace](tutorials/first-workspace.md)

### How-To Guides
- [Debugging Ports](how-to/debug-ports.md)

### Explanation
- [Architecture](explanation/architecture.md)

### Reference
- [CLI Reference](reference/cli.md)

### Development
- [Contributing](dev/contributing.md)
- [Roadmap](dev/roadmap.md)
- [Architecture Decisions](dev/decisions/)

## Statistics

| Metric | Value |
|--------|-------|
| Source Code | ~4,273 lines |
| Test Code | ~5,598 lines |
| Test Functions | 153 |
| Test Files | 10 |

## Resources

- **OpenCode Plugin Docs:** https://opencode.ai/docs/plugins/
- **Config Docs:** https://opencode.ai/docs/config/
- **Nexus Repo:** https://github.com/IniZio/nexus
- **Testing Plan:** `docs/testing/ENFORCER_TESTING.md`

## Next Steps

- Browse [tutorials](tutorials/) to get started
- Read the [architecture](explanation/architecture.md) to understand how it works
- Check the [roadmap](dev/roadmap.md) for upcoming features
