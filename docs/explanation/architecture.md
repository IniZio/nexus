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

## Workspace Lifecycle

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Workspace Lifecycle                                 │
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
│  git          container       container       cleanup                      │
│  worktree     starts          stops           volumes                       │
│  created      sshd runs       sshd stops      worktree                      │
│                           (state preserved)  removed                        │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## SSH Access Model

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         SSH Access Flow                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   User Command                                                               │
│   ┌─────────────────────┐                                                   │
│   │ nexus workspace ssh │                                                   │
│   │ my-workspace        │                                                   │
│   └──────────┬──────────┘                                                   │
│              │                                                              │
│              ▼                                                              │
│   ┌──────────────────────────────────────────────┐                          │
│   │  Daemon resolves SSH port (e.g., 32801)     │                          │
│   │  - Deterministic: hash(workspace) + 32800   │                          │
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
| **CLI** | Go | Command-line interface for workspace management |
| **Daemon** | Go | Background service handling workspace operations |
| **Plugins** | TypeScript | IDE integration (OpenCode, Claude Code, Cursor) |
| **Docker Backend** | Docker API | Container orchestration with SSH access |
| **Telemetry** | Agent Trace | Usage tracking and analytics |

## Port Allocation

- **Range**: 32800-34999 (Docker backend)
- **Strategy**: Deterministic hash-based allocation
- **Per workspace**: SSH + service ports (web, api, db, etc.)
