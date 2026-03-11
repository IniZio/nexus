# Roadmap

## Current Status

**Active Development:** Nexus is a multi-component project with varying levels of completion.

## Product Interface Migration

The approved future product model is:

`Org -> Project -> Branch -> Version -> Environment`

This is internal direction for staged migration planning. The currently implemented user CLI remains workspace-first (`nexus workspace ...`) as defined in `packages/nexusd/internal/cli/root.go` and `packages/nexusd/internal/cli/workspace.go`.

Do not treat project-first command groups as implemented until they are added to the shipped CLI.

## Component Overview

| Component | Status | Description |
|-----------|--------|-------------|
| **Enforcer** | ✅ Implemented | Task enforcement with idle detection and mini-workflows |
| **OpenCode Plugin** | ✅ Implemented | OpenCode IDE integration |
| **Claude Integration** | ✅ Implemented | Claude Code plugin |
| **Cursor Extension** | ✅ Implemented | Cursor IDE extension |
| **Workspace (nexusd)** | ✅ Implemented | Go-based workspace server with Docker, SSH, port forwarding, DinD, checkpoints |
| **Workspace CLI** | ✅ Implemented | `nexus workspace` commands for managing Docker-based workspaces |
| **Telemetry** | ✅ Implemented | Agent Trace specification implementation for attribution tracking |
| **Multi-User Support** | 📋 Planned | Organization and team management with workspace sharing |
| **Web Dashboard** | 📋 Planned | Web UI for monitoring and management |
| **Auto-Update** | 📋 Planned | Self-updating CLI with secure distribution |

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
| Multi-Agent Support | ✅ | OpenCode, Claude, Cursor |

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
| Architecture Design | ✅ Complete | Go-based workspace server |
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

---

## Phase 2: Next Features (Q2 2026)

### Multi-User Support

Enable team collaboration with workspace sharing and resource governance.

**Status:** 📋 Planned - PRD Ready  
**Effort:** Medium (6-8 weeks)  
**Dependencies:** None  

**Features:**
- Organization management (multi-tenant)
- Team collaboration with workspace sharing
- Resource quotas (per org/user)
- Permission levels (owner/editor/viewer)
- Audit logging

**CLI Commands:**
```bash
nexus auth login
nexus org create "Acme Corp"
nexus org invite teammate@acme.com
nexus workspace share feature-auth teammate@acme.com --permission editor
```

See: [004-multi-user.md](./plans/004-multi-user.md)

---

### Web Dashboard

Visual interface for workspace management with real-time monitoring.

**Status:** 📋 Planned - PRD Ready  
**Effort:** Medium (8-10 weeks)  
**Dependencies:** None (can be built in parallel)

**Features:**
- Workspace list with status overview
- Real-time resource charts (CPU, memory, disk)
- In-browser terminal (xterm.js)
- One-click workspace actions
- Responsive design (mobile support)

**Tech Stack:**
- React 18 + TypeScript + Tailwind CSS
- WebSocket for real-time updates
- Vite for build
- Recharts for metrics

See: [005-web-dashboard.md](./plans/005-web-dashboard.md)

---

### Auto-Update System

Self-updating CLI with secure distribution.

**Status:** 📋 Planned - PRD Ready  
**Effort:** Short (4-5 weeks)  
**Dependencies:** None  

**Features:**
- Automatic update checks on startup
- One-command update installation
- Signed binary verification (Minisign)
- Atomic replacement with rollback
- Multiple channels (stable/beta/nightly)

**CLI Commands:**
```bash
nexus update check
nexus update install
nexus update status
```

See: [006-auto-update.md](./plans/006-auto-update.md)

---

## Telemetry (Implemented)

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

### Implementation Status

| Phase | Feature | Status |
|-------|---------|--------|
| 1 | Core telemetry collector | ✅ Complete |
| 2 | Git integration | ✅ Complete |
| 3 | CLI trace commands | ✅ Complete |
| 4 | Query interface | ✅ Complete |
| 5 | Dashboard integration | 📋 Planned (Phase 3) |

---

## Phase 3: Future Ideas (2026+)

### Advanced Workspace Backends

Expand beyond Docker:

| Backend | Use Case | Status |
|---------|----------|--------|
| **Firecracker** | MicroVMs for stronger isolation | Research |
| **Kubernetes** | Enterprise orchestration | Research |
| **LXD** | System containers | Research |
| **Remote SSH** | Existing servers as workspaces | Planned |

### Workspace Templates Marketplace

- Pre-configured environments for popular stacks
- Team template sharing
- Versioned templates
- Template builder UI

### CI/CD Integration

- GitHub Actions integration
- Pre-commit hooks
- Workspace snapshots for CI
- Test parallelization

### Advanced Networking

- VPN access to workspaces
- Service mesh integration
- Custom domains
- TLS certificate management

---

## Implementation Timeline

```
2026 Q1:
├── ✅ Complete Telemetry implementation
├── ✅ Cursor extension polish
└── 🚧 File sync (Mutagen) improvements

2026 Q2 (Phase 2):
├── 📋 Multi-User Support (6-8 weeks)
├── 📋 Web Dashboard (8-10 weeks)
└── 📋 Auto-Update System (4-5 weeks)

2026 Q3+ (Phase 3):
├── 📋 Additional backends (Firecracker, k8s)
├── 📋 Template marketplace
└── 📋 Advanced networking
```

---

## Research Summary

See [Phase 2 Feature Research](./internal/research/phase2-features.md) for detailed analysis of:
- Multi-tenancy patterns
- Dashboard design patterns
- Auto-update security models
- Resource quota strategies

---

## Related Documentation

- [Boulder System](./boulder-system.md) - Enforcement system details
- [Architecture Decisions](./decisions/) - ADRs
- [Implementation Plans](./plans/) - PRDs for all features
- [Research Findings](./internal/research/) - Technical research

---

## Changelog

### February 2026
- Added Phase 2 features (Multi-User, Web Dashboard, Auto-Update)
- Updated component statuses
- Created detailed PRDs for next features
- Added implementation timeline

### January 2026
- Added Cursor extension to roadmap
- Updated component statuses

---

**Last Updated:** February 2026
