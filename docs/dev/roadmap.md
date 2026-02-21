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
| **Workspace SDK** | 🚧 In Development | Remote workspace WebSocket SDK |
| **Workspace Daemon** | 🚧 In Development | Go-based workspace server |
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

## Workspace (In Development)

Inspired by [opencode-devcontainer](https://github.com/athal7/opencode-devcontainer) and [Sprites](https://github.com/peterj/sprites).

### Goals

Provide isolated, reproducible development environments for AI agents:

- **Isolation** - Each task in its own environment
- **Reproducibility** - Same setup every time
- **Remote Execution** - Run agents anywhere
- **Git Integration** - Worktree-based isolation

### Current Progress

| Milestone | Status | Notes |
|-----------|--------|-------|
| Architecture Design | ✅ Complete | See [internal plans](internal/plans/) |
| SDK Protocol | ✅ Complete | WebSocket + JSON-RPC |
| SDK Implementation | 🚧 80% | File ops, exec working |
| Daemon Prototype | 🚧 60% | Go server in development |
| Docker Integration | 📋 Planned | Container environments |
| Remote Workspaces | 📋 Planned | SSH-based remote execution |

### Open Questions

1. **Auth Forwarding** - How to securely forward API keys to remote workspaces?
2. **State Persistence** - How to persist workspace state across restarts?
3. **Multi-User** - How to handle multiple agents on same workspace?

See [internal implementation plans](internal/implementation/) for details.

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

See [ADR-003: Telemetry Design](decisions/003-telemetry-design.md) for technical details.

---

## Future Ideas

### Web Dashboard

A web interface for:
- Monitoring active agents
- Viewing enforcement history
- Managing workspace fleet
- Analytics and insights

### Multi-Agent Coordination

Enable multiple agents to work together:
- Task distribution
- Dependency management
- Conflict resolution
- Shared context

### MCP Server

Model Context Protocol integration:
- External tool integration
- Custom enforcement rules
- Third-party extensions

---

## Related Documentation

- [Architecture Overview](../explanation/architecture.md)
- [Boulder System](../explanation/boulder-system.md)
- [Internal Plans](internal/plans/) - Workspace architecture
- [Internal Implementation](internal/implementation/) - Workspace SDK plans
- [Architecture Decisions](decisions/) - ADRs

---

**Last Updated:** February 2026
