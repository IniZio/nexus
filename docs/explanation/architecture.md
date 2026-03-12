# Architecture Overview

## System Overview

Nexus is an AI-native development environment with three main components:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Nexus Architecture                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                           CLI Layer                                   │   │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐ │   │
│  │  │  nexus CLI  │  │   OpenCode  │  │ Claude Code │  │    Cursor   │ │   │
│  │  │   (Go)      │  │  (Plugin)   │  │  (Plugin)   │  │   (Plugin)  │ │   │
│  │  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘ │   │
│  └─────────┼────────────────┼────────────────┼────────────────┼────────┘   │
│            │                │                │                │             │
│            └────────────────┴────────────────┴────────────────┘             │
│                                       │                                      │
│                                       ▼                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                         Daemon (nexusd)                               │   │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐ │   │
│  │  │  Workspace  │  │     SSH     │  │    Port    │  │   Telemetry │ │   │
│  │  │   Manager   │  │   Manager    │  │  Allocator │  │  Collector  │ │   │
│  │  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘ │   │
│  └─────────┼────────────────┼────────────────┼────────────────┼────────┘   │
│            │                │                │                │             │
│            └────────────────┴────────────────┴────────────────┘             │
│                                       │                                      │
│                                       ▼                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                          Backend Layer                                │   │
│  │  ┌─────────────────────────────────┐  ┌─────────────────────────────┐ │   │
│  │  │         Docker                  │  │      Future Backends       │ │   │
│  │  │  ┌───────────────────────────┐  │  │  ┌───────────────────────┐  │ │   │
│  │  │  │  Container + SSH Server  │  │  │  │  Sprite / Kubernetes │  │ │   │
│  │  │  │  • Isolated workspace     │  │  │  │  (planned)           │  │ │   │
│  │  │  │  • Git worktree mounted   │  │  │  └───────────────────────┘  │ │   │
│  │  │  │  • SSH access (port 32800+)│ │  └─────────────────────────────┘ │   │
│  │  │  └───────────────────────────┘  │                                  │   │
│  │  └─────────────────────────────────┘                                  │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Environment Lifecycle

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Environment Lifecycle                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  create          start           stop           destroy                     │
│    │              │               │               │                         │
│    ▼              ▼               ▼               ▼                         │
│ ┌──────┐      ┌──────┐       ┌──────┐       ┌──────┐                       │
│ │NEW   │ ──▶  │RUNNING│ ──▶  │STOPPED│ ──▶  │DESTROYED│                   │
│ └──────┘      └──────┘       └──────┘       └──────┘                       │
│    │              │               │               │                         │
│    │              │               │               │                         │
│    ▼              ▼               ▼               ▼                         │
│  version      container       container       cleanup                      │
│  snapshot     starts          stops           volumes                       │
│  prepared     sshd runs       sshd stops      environment                   │
│                           (state preserved)  removed                        │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Environment Access Model

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         SSH Access Flow                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   User Command                                                               │
│   ┌─────────────────────┐                                                   │
│   │ nexus environment ssh │                                                 │
│   │ my-environment      │                                                   │
│   └──────────┬──────────┘                                                   │
│              │                                                              │
│              ▼                                                              │
│   ┌──────────────────────────────────────────────┐                          │
│   │  Daemon resolves SSH port (e.g., 32801)     │                          │
│   │  - Deterministic: hash(environment) + 32800 │                          │
│   └──────────────────────┬───────────────────────┘                          │
│                          │                                                  │
│                          ▼                                                  │
│   ┌──────────────────────────────────────────────┐                          │
│   │  ssh -A nexus@localhost -p 32801            │                          │
│   │  - Agent forwarding enabled                  │                          │
│   │  - No password required (authorized_keys)   │                          │
│   └──────────────────────┬───────────────────────┘                          │
│                          │                                                  │
│                          ▼                                                  │
│   ┌──────────────────────────────────────────────┐                          │
│   │  Container (172.20.0.2)                      │                          │
│   │  - OpenSSH server on port 22                 │                          │
│   │  - User "nexus" with injected keys          │                          │
│   │  - Working dir: /work                        │                          │
│   └──────────────────────────────────────────────┘                          │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Key Components

| Component | Technology | Purpose |
|-----------|------------|---------|
| **CLI** | Go | Command-line interface for project, branch, version, and environment workflows |
| **Daemon** | Go | Background service handling environment operations |
| **Plugins** | TypeScript | IDE integration (OpenCode, Claude Code, Cursor) |
| **Docker Backend** | Docker API | Container orchestration with SSH access |
| **Telemetry** | Agent Trace | Usage tracking and analytics |

## Current Interface (Implemented)

- The user-facing CLI is organized around `project`, `branch`, `version`, and `environment` command groups.
- `project list` and `branch use` are scaffold stubs right now and return not-implemented errors.
- Environment lifecycle and access are handled through `nexus environment ...` subcommands, including checkpoint operations.
- Internal implementation still uses `workspace` naming in some APIs and storage paths while migration work continues.

## Future Interface Direction (Internal Planning Note)

Nexus has approved a future canonical product model:

`Org -> Project -> Branch -> Version -> Environment`

Current environment internals are expected to keep converging on that model over time, and the project-first command tree is the shipped user-facing interface.

## Port Allocation

- **Range**: 32800-34999 (Docker backend)
- **Strategy**: Deterministic hash-based allocation
- **Per workspace**: SSH + service ports (web, api, db, etc.)
