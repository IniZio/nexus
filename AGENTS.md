# Agent Guidelines

## Project Overview

This is the **Nexus** project - an AI-native development environment with multiple components:

### Components

| Component | Status | Description |
|-----------|--------|-------------|
| **Enforcer** | ✅ Implemented | Task enforcement with idle detection and mini-workflows |
| **Workspace** | 🚧 In Development | Isolated dev environments (inspired by opencode-devcontainer, sprite) |
| **Telemetry** | 📋 Planned | Agent Trace specification implementation for attribution tracking |

### Packages

| Package | Component | Status |
|---------|-----------|--------|
| `packages/enforcer` | Enforcer | ✅ Core enforcement library |
| `packages/opencode-plugin` | Enforcer | ✅ OpenCode integration |
| `packages/opencode` | Enforcer | ✅ OpenCode CLI tool |
| `packages/claude` | Enforcer | ✅ Claude Code integration |
| `packages/cursor` | Enforcer | 🚧 Cursor IDE extension |
| `packages/workspace-sdk` | Workspace | 🚧 WebSocket SDK |
| `packages/workspace-daemon` | Workspace | 🚧 Go server |

### What IS Implemented

- **Boulder/Enforcer System**: Idle detection, mini-workflows (docs, git, CI enforcement)
- **IDE Plugins**: OpenCode, Claude Code (Cursor in progress)
- **Docker Workspaces**: SSH-based workspace containers with:
  - OpenSSH server in each container
  - Auto-allocated SSH ports (32800-34999)
  - User SSH key injection for passwordless auth
  - `nexus workspace ssh` command for interactive access
  - SSH-based exec (replaces docker exec)
  - SSH agent forwarding support
- **Workspace SDK**: Partial implementation for remote file/exec operations

### What Is NOT Yet Implemented

- Workspace lifecycle management (stop/start/pause)
- Complete remote workspace lifecycle
- Telemetry/Agent Trace tracking
- `nexus` CLI (use `boulder` CLI instead)

---

## Enforcement Rules

- An agent MUST complete tasks fully before claiming completion.
- An agent MUST verify all requirements are explicitly addressed.
- An agent MUST ensure code works, builds, runs, and tests pass.
- An agent MUST provide evidence of success, not just claims.
- An agent SHOULD test changes in real environments via dogfooding.
- An agent MUST verify builds succeed before claiming completion.
- An agent MUST verify there are zero type errors.
- An agent MUST verify there are zero lint errors.
- An agent SHOULD log friction points encountered during development.
- An agent MUST use isolated workspaces for feature development.
- An agent MUST NOT work directly in the main worktree for features.
- An agent MUST list what remains undone if stopping early.
- An agent MUST explain why it cannot complete a task if stopping early.
- An agent MUST specify what the user needs to do next if stopping early.

---

## Documentation Guidelines

### User-Facing Docs (docs/)

User-facing documentation goes in `docs/` and its subdirectories:

- `docs/tutorials/` - Step-by-step guides for users
- `docs/reference/` - API references, CLI commands, configuration
- `docs/explanation/` - Conceptual explanations and architecture
- `docs/dev/` - Developer documentation (contributing, roadmap, ADRs)

**Only document ACTUALLY IMPLEMENTED features.**

### Internal Docs (docs/dev/internal/)

Historical, planning, and research documents go in `docs/dev/internal/`:

- `docs/dev/internal/implementation/` - Implementation plans (historical)
- `docs/dev/internal/plans/` - Feature plans (some may not be implemented)
- `docs/dev/internal/design/` - Design documents (historical)
- `docs/dev/internal/research/` - Research findings
- `docs/dev/internal/testing/` - Testing reports (some reference unimplemented features)
- `docs/dev/internal/ARCHIVE/` - Archived documents

### Architecture Decision Records

ADRs go in `docs/dev/decisions/`:

- `docs/dev/decisions/001-worktree-isolation.md`
- `docs/dev/decisions/002-port-allocation.md`
- `docs/dev/decisions/003-telemetry-design.md`

### What NOT to Reference

Never reference in documentation:

- `nexus` CLI commands (they don't exist)
- Docker container workspaces (not implemented)
- Workspace lifecycle management (not implemented)
- Remote workspaces via SSH (not implemented)

If a feature doesn't exist, don't document it as if it does. Instead, note it as planned/future.

### Documentation Structure

```
docs/
├── index.md                 # Home
├── tutorials/              # User tutorials
│   ├── installation.md
│   └── plugin-setup.md
├── reference/              # API/CLI reference
│   ├── boulder-cli.md
│   ├── enforcer-config.md
│   └── workspace-sdk.md   # Note: workspace SDK not fully implemented
├── explanation/           # Concepts
│   ├── architecture.md
│   └── boulder-system.md
└── dev/                    # Developer docs
    ├── contributing.md
    ├── roadmap.md
    ├── decisions/         # ADRs
    └── internal/          # Historical docs (not user-facing)
        ├── implementation/
        ├── plans/
        ├── design/
        ├── research/
        ├── testing/
        └── ARCHIVE/
```
