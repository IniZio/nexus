# Roadmap

## Current Status

**Active Development:** Nexus is a multi-component project with varying levels of completion.

## Component Overview

| Component | Status | Description |
|-----------|--------|-------------|
| **Enforcer** | ✅ Implemented | Task enforcement with idle detection and mini-workflows |
| **OpenCode Plugin** | ✅ Implemented | OpenCode IDE integration |
| **Claude Integration** | ✅ Implemented | Claude Code plugin |
| **Cursor Extension** | 🚧 In Progress | Cursor IDE extension |
| **Workspace (nexusd)** | ✅ Implemented | Go-based workspace server with Docker, SSH, port forwarding, DinD, checkpoints |
| **Workspace CLI** | ✅ Implemented | `nexus workspace` commands for managing Docker-based workspaces |
| **Telemetry** | 📋 Planned | Agent Trace specification implementation |
| **Web Dashboard** | 📋 Planned | Web UI for monitoring and management |

Legend:
- ✅ Implemented - Production ready
- 🚧 In Progress - Under active development  
- 📋 Planned - Designed but not yet started

---

## Enforcer (Implemented)

The Enforcer component is production-ready and actively used.

### Core Features

| Feature | Status | Description |
|---------|--------|-------------|
| Idle Detection | ✅ | Prevents agents from stopping prematurely |
| Mini-Workflows | ✅ | Enforces documentation, git, CI standards |
| Boulder System | ✅ | Never-stop iteration enforcement |
| Multi-Agent Support | ✅ | OpenCode, Claude (Cursor in progress) |

### Mini-Workflows

Implemented enforcement workflows:

1. **Documentation Validation**
   - Checks docs follow project structure
   - Ensures new features are documented
   - Validates AGENTS.md compliance

2. **Git Commit Standards**
   - Enforces conventional commits
   - Validates commit organization
   - Prevents messy git history

3. **CI Verification**
   - Blocks completion until CI passes
   - Runs `task ci` before allowing completion
   - Ensures no type/lint errors

### Statistics

- **Source:** ~4,273 lines
- **Tests:** ~5,598 lines  
- **Coverage:** 1.3:1 ratio
- **Packages:** enforcer, opencode, claude, cursor

---

## Workspace (Implemented)

Inspired by [opencode-devcontainer](https://github.com/athal7/opencode-devcontainer) and [Sprites](https://github.com/peterj/sprites).

### Goals

Provide isolated, reproducible development environments for AI agents:

- **Isolation** - Each task in its own environment
- **Reproducibility** - Same setup every time
- **Remote Execution** - Run agents anywhere
- **Git Integration** - Worktree-based isolation

### Current Status

| Milestone | Status | Notes |
|-----------|--------|-------|
| Architecture Design | ✅ Complete | See [internal plans](./internal/plans/) |
| SDK Protocol | ✅ Complete | WebSocket + JSON-RPC |
| SDK Implementation | 🚧 80% | File ops, exec working |
| Daemon | ✅ Implemented | Go server with Docker backend |
| Docker Integration | ✅ Implemented | Container environments with SSH access |
| SSH Workspaces | ✅ Implemented | SSH-based access with agent forwarding |
| Port Forwarding | ✅ Implemented | Auto-allocated ports (32800-34999) |
| File Sync | 🚧 In Progress | Mutagen integration (partial) |
| Remote Workspaces | 📋 Planned | Cloud-based workspace execution |

### Implemented Features

**Docker Backend:**
- Full Docker Compose support
- Volume persistence across restarts
- Port auto-allocation (32800-34999 range)
- OpenSSH server in each container
- User SSH key injection
- SSH agent forwarding (macOS compatible)

**Workspace Management:**
- Git worktree isolation (`.worktree/<name>/`)
- Automatic branch creation (`nexus/<name>`)
- `nexus workspace ssh` command for interactive access
- SSH-based exec (replaces docker exec)

**Port Allocation:**
- SSH ports: 32800-34999
- Service ports auto-assigned sequentially
- Conflict detection and resolution

### Open Questions

1. **File Sync Completion** - Finish Mutagen bidirectional sync implementation
2. **State Persistence** - Complete checkpoint/restore functionality
3. **Multi-User** - How to handle multiple agents on same workspace?

See [internal plans](./internal/plans/001-workspace-management.md) for technical details.

---

## Telemetry (Planned)

Following the [Agent Trace](https://agent-trace.dev/) specification.

### Vision

Track AI contributions with full provenance:

```typescript
// Example trace record
{
  "version": "0.1.0",
  "vcs": {
    "type": "git",
    "revision": "abc123..."
  },
  "files": [{
    "path": "src/app.ts",
    "conversations": [{
      "url": "https://nexus.dev/conv/123",
      "contributor": {
        "type": "ai",
        "model_id": "anthropic/claude-opus-4-5-20251101"
      },
      "ranges": [
        { "start_line": 10, "end_line": 25 }
      ]
    }]
  }]
}
```

### Use Cases

- **Attribution** - Know what code came from AI vs humans
- **Auditing** - Track which model/conversation produced changes
- **Analytics** - Measure agent effectiveness
- **Compliance** - Document AI involvement for legal/regulatory

### Implementation Plan

| Phase | Feature | Status |
|-------|---------|--------|
| 1 | Trace record storage | 📋 Not started |
| 2 | Git integration | 📋 Not started |
| 3 | IDE plugin hooks | 📋 Not started |
| 4 | Query interface | 📋 Not started |
| 5 | Dashboard | 📋 Not started |

See [internal plans](./internal/plans/002-telemetry.md) for PRD details.

---

## Future Ideas

### Web Dashboard

A web interface for:
- Monitoring active agents
- Viewing enforcement history
- Managing workspace fleet
- Analytics and insights

**Status:** Not planned for Q1 2026

### Multi-Agent Coordination

Enable multiple agents to work together:
- Task distribution
- Dependency management
- Conflict resolution
- Shared context

**Status:** Research phase

### Nexus CLI

Unified CLI to replace scattered entry points:
- `nexus workspace` commands
- `nexus boulder` commands  
- `nexus trace` commands
- Single configuration file

See [internal plans](./internal/plans/003-nexus-cli.md) for PRD details.

**Status:** Planned - not yet implemented

### MCP Server

Model Context Protocol integration:
- External tool integration
- Custom enforcement rules
- Third-party extensions

**Status:** Under consideration

---

## Related Documentation

- [Boulder System](../explanation/boulder-system.md) - Enforcement system details
- [Architecture Decisions](./decisions/) - ADRs
- [Internal Plans](./internal/plans/) - PRDs for upcoming features

---

## Changelog

### February 2026
- Updated Workspace Daemon status to "Implemented"
- Added SSH workspace access documentation
- Updated port allocation (32800-34999 range)
- Removed "Docker NOT implemented" references

### January 2026
- Added Cursor extension to roadmap
- Updated component statuses

---

**Last Updated:** February 2026
