# Docker Workspace Management PRD

**Version:** 1.0.0  
**Status:** Production-Ready for Implementation  
**Last Updated:** 2026-02-22  
**Document Owner:** Nexus Architecture Team  

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Problem Statement](#2-problem-statement)
3. [Goals and Non-Goals](#3-goals-and-non-goals)
4. [Reference Research](#4-reference-research)
5. [Architecture](#5-architecture)
6. [Data Models](#6-data-models)
7. [API Specification](#7-api-specification)
8. [Security Model](#8-security-model)
9. [Error Handling](#9-error-handling)
10. [Edge Cases](#10-edge-cases)
11. [Testing Strategy](#11-testing-strategy)
12. [Performance Benchmarks](#12-performance-benchmarks)
13. [Operational Runbook](#13-operational-runbook)
14. [Migration Guide](#14-migration-guide)
15. [Risk Assessment](#15-risk-assessment)
16. [Success Metrics](#16-success-metrics)
17. [Real-World Testing: hanlun-lms.git](#17-real-world-testing-hanlun-lmsgit)
18. [Appendices](#18-appendices)

---

## 1. Executive Summary

### 1.1 Overview

The Docker Workspace Management system provides **frictionless parallel development environments** for AI-native development workflows. It combines git worktree isolation with containerized compute to enable multiple developers (or a single developer with multiple AI agents) to work on the same repository simultaneously without conflicts.

### 1.2 Key Value Propositions

| Capability | Value |
|------------|-------|
| **Parallel Worktrees** | Work on 10+ features simultaneously with zero branch conflicts |
| **Sub-2s Context Switch** | Switch between workspaces faster than switching browser tabs |
| **State Preservation** | Your dev server, terminal history, and file changes persist across sessions |
| **Hybrid Backends** | Run locally with Docker or remotely with Sprite—seamlessly switch between them |
| **AI-Native** | Designed for human-AI collaboration with attribution tracking |

### 1.3 Target Users

1. **AI-Native Developers**: Humans pairing with AI agents (Cursor, Claude Code, OpenCode)
2. **Platform Teams**: Managing development environments at scale
3. **Open Source Contributors**: Quick, isolated environments for PR reviews
4. **Agencies**: Multiple client projects with conflicting dependencies

### 1.4 Success Criteria

- **Adoption**: 90% of Nexus users create ≥2 workspaces within first week
- **Performance**: Workspace switch < 2 seconds, cold start < 30 seconds
- **Reliability**: 99.9% workspace availability, zero data loss incidents
- **Friction**: <1 support ticket per 100 workspace operations

---

## 2. Problem Statement

### 2.1 Current Pain Points

#### 2.1.1 The Branch Conflict Problem

**Scenario**: Developer needs to:
1. Fix urgent bug in `main` branch (5 min task)
2. Continue feature work on `feature/payments` (2 hour context)
3. Review colleague's PR on `feature/auth` (needs testing)

**Current Experience**:
```bash
# Context switch 1: Bug fix
git stash push -m "payments WIP"
git checkout main
# ...fix bug, commit...
git checkout feature/payments
git stash pop
# Merge conflicts! Lost 20 minutes resolving.

# Context switch 2: PR review
git stash push -m "payments WIP again"
git fetch origin feature/auth
git checkout -b review-auth origin/feature/auth
# ...review...
git checkout feature/payments
git stash pop
# More conflicts!
```

**Time Lost**: 30-45 minutes per context switch × 10 switches/day = **5-7 hours lost daily**

#### 2.1.2 The Environment Drift Problem

**Scenario**: Works on my machine → Fails in CI

**Root Causes**:
1. Different Node.js versions (18.12 vs 18.15)
2. Global CLI tools not documented (`npm i -g pnpm`)
3. Environment variables in `.bashrc`, not in repo
4. Database schema differences

**Impact**: 2-4 hours debugging per environment mismatch

#### 2.1.3 The Dependency Conflict Problem

**Scenario**: Two projects requiring conflicting global tools:
- Project A: Python 3.9, Node 16
- Project B: Python 3.11, Node 18

**Current Solutions**:
- pyenv/nvm (complex, shell-specific)
- Virtual machines (slow, resource-heavy)
- "Just use different computers" (not scalable)

#### 2.1.4 The AI Collaboration Problem

**Scenario**: Claude Code makes changes while human works on same file

**Current Risk**:
- No isolation between human and AI workstreams
- AI overwrites human changes (or vice versa)
- No attribution for who wrote what

### 2.2 Quantified Impact

Based on friction collection data (n=127 developers):

| Pain Point | Frequency | Avg Time Lost | Annual Cost* |
|------------|-----------|---------------|--------------|
| Context switching | 12×/day | 25 min | 1,250 hrs/dev |
| Environment setup | 2×/week | 4 hrs | 416 hrs/dev |
| "Works on my machine" | 1×/week | 3 hrs | 156 hrs/dev |
| Dependency conflicts | 1×/month | 2 hrs | 24 hrs/dev |

*Annual cost per developer at $100/hr loaded rate

**Total**: ~$184,600/year per developer in lost productivity

---

## 3. Goals and Non-Goals

### 3.1 Goals (In Scope)

#### 3.1.1 Must Have (P0)

1. **Git Worktree Isolation**
   - Automatic branch creation per workspace (`nexus/<name>`)
   - Independent file systems per workspace
   - Zero-conflict parallel development

2. **Docker Backend**
   - Full Docker Compose support
   - Volume persistence across restarts
   - Port auto-allocation (no conflicts)

3. **Sub-2-Second Workspace Switch**
   - Container warm start < 2s
   - State restoration (terminal, running processes)
   - Hot reload preserved

4. **Sprite Backend Support**
   - CLI flag: `--backend=sprite`
   - Same UX as Docker backend
   - Automatic fallback on Docker unavailable

5. **AI-Native Features**
   - Agent Trace integration (attribution tracking)
   - Friction collection (usage analytics)
   - Conversation-to-workspace mapping

6. **Production-Grade Reliability**
   - 99.9% workspace availability
   - Automatic recovery from crashes
   - Zero data loss guarantees

#### 3.1.2 Should Have (P1)

1. **Prebuilt Images**
   - Cached layers for common stacks
   - 50% faster cold start

2. **Snapshot/Checkpoint**
   - Save workspace state
   - Rollback to previous state
   - Share snapshots with team

3. **Web IDE Integration**
   - VS Code in browser
   - Port forwarding with public URLs

4. **Resource Limits**
   - Per-workspace CPU/memory quotas
   - Auto-shutdown on idle

#### 3.1.3 Nice to Have (P2)

1. **Multi-Region Support**
   - Sprite backend in EU, Asia

2. **Team Workspaces**
   - Shared persistent volumes
   - Real-time collaboration

3. **Custom Domains**
   - `workspace.mycompany.dev`

### 3.2 Non-Goals (Explicitly Out of Scope)

#### 3.2.1 Not in V1

1. **Kubernetes Backend**
   - Complex orchestration out of scope
   - Docker/Sprite sufficient for V1

2. **Windows Container Support**
   - Linux containers only
   - WSL2 works via Docker Desktop

3. **GUI Applications**
   - No VNC/RDP for GUI apps
   - Web-based tools only

4. **Persistent Database Clusters**
   - Single-node DBs only (Postgres, MySQL)
   - Use external DBaaS for production data

5. **Built-in CI/CD**
   - Focus on development environments
   - Integrate with external CI (GitHub Actions, etc.)

6. **Fine-Grained RBAC**
   - Simple token-based auth
   - Enterprise SSO in V2

#### 3.2.2 Never in Scope

1. **Production Hosting**
   - Not a PaaS like Heroku
   - Development environments only

2. **Code Review System**
   - Use GitHub/GitLab PRs
   - Not reinventing version control

3. **Package Registry**
   - Use npm/pypi/docker hub
   - Not a build artifact store

---

## 4. Reference Research

### 4.1 Sprites.dev (fly.io)

#### 4.1.1 Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        Sprite Instance                       │
│  ┌───────────────────────────────────────────────────────┐  │
│  │           Firecracker MicroVM (vCPU, RAM)              │  │
│  │  ┌─────────────────────────────────────────────────┐  │  │
│  │  │              ext4 Root Filesystem                │  │  │
│  │  │  ┌──────────────┐  ┌─────────────────────────┐  │  │  │
│  │  │  │   Runtime    │  │    User Workspace       │  │  │  │
│  │  │  │  (minimal)   │  │  (/home/user/workspace) │  │  │  │
│  │  │  └──────────────┘  └─────────────────────────┘  │  │  │
│  │  └─────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────┘  │
│                            │                                │
│                            ▼                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │           NVMe Hot Storage (ephemeral)                 │  │
│  │     Fast local cache for active working data           │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│              Object Storage (persistent, S3-like)           │
│              Durable storage for checkpoints                 │
└─────────────────────────────────────────────────────────────┘
```

#### 4.1.2 Key Insights

| Feature | Implementation | Nexus Adaptation |
|---------|---------------|------------------|
| **Checkpoints** | Copy-on-write filesystem snapshots | Use Docker commits + volume snapshots |
| **Activation** | HTTP request triggers VM allocation | WebSocket connect triggers container start |
| **Idle Detection** | Cgroup CPU/memory counters | Docker stats + inactivity timeout |
| **Billing** | Per-second CPU/memory/disk | N/A (local resources) |

#### 4.1.3 Lifecycle Management

```
Cold Start Flow:
1. User requests workspace
2. Allocate Firecracker VM (300-500ms)
3. Restore filesystem from checkpoint (1-2s)
4. Start init process
5. Accept connections (<2s total)

Warm Start Flow:
1. User requests workspace
2. VM already running
3. Accept connection (<100ms)
```

### 4.2 GitHub Codespaces

#### 4.2.1 Architecture

```
┌────────────────────────────────────────────────────────────┐
│                    Azure VM (Host)                         │
│  ┌──────────────────────────────────────────────────────┐  │
│  │              Docker Container                        │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌──────────────┐  │  │
│  │  │   VS Code   │  │  Dev Tools  │  │   Project    │  │  │
│  │  │   Server    │  │  (node, py) │  │   Source     │  │  │
│  │  └─────────────┘  └─────────────┘  └──────────────┘  │  │
│  └──────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────┘
```

#### 4.2.2 Key Insights

| Feature | Implementation | Nexus Adaptation |
|---------|---------------|------------------|
| **Dev Container Config** | `.devcontainer/devcontainer.json` | Support standard format |
| **Port Forwarding** | Automatic HTTPS URLs | Localhost + optional ngrok |
| **Prebuilds** | GitHub Actions workflow | Local Docker layer cache |
| **Dotfiles** | `install.sh` from dotfiles repo | Post-create hooks |

#### 4.2.3 Configuration Standard

```json
{
  "name": "Node.js & PostgreSQL",
  "image": "mcr.microsoft.com/devcontainers/javascript-node:18",
  "features": {
    "ghcr.io/devcontainers/features/docker-in-docker:2": {},
    "ghcr.io/devcontainers/features/github-cli:1": {}
  },
  "forwardPorts": [3000, 5432],
  "postCreateCommand": "npm install",
  "customizations": {
    "vscode": {
      "extensions": ["dbaeumer.vscode-eslint"]
    }
  }
}
```

### 4.3 DevPod (loft.sh)

#### 4.3.1 Provider Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        DevPod CLI                          │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │   Docker     │  │   Kubernetes │  │   SSH (Remote)   │  │
│  │   Provider   │  │   Provider   │  │     Provider     │  │
│  └──────────────┘  └──────────────┘  └──────────────────┘  │
│                                                            │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │    AWS       │  │    GCP       │  │   DigitalOcean   │  │
│  │   Provider   │  │   Provider   │  │     Provider     │  │
│  └──────────────┘  └──────────────┘  └──────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

#### 4.3.2 Key Insights

| Feature | Implementation | Nexus Adaptation |
|---------|---------------|------------------|
| **Provider Interface** | Go interface with lifecycle methods | Go interface: Create/Start/Stop/Destroy |
| **IDE Agnostic** | VS Code, JetBrains, SSH | Focus on CLI + WebSocket SDK |
| **Local & Remote** | Same UX regardless of backend | Docker local, Sprite remote |
| **Prebuilds** | Image builds on server push | Multi-stage Docker builds |

### 4.4 Gitpod

#### 4.4.1 Workspace Classes

| Class | CPU | Memory | Use Case |
|-------|-----|--------|----------|
| Standard | 4 cores | 8GB | Default development |
| Large | 8 cores | 16GB | Resource-intensive builds |
| XL | 16 cores | 32GB | Heavy compute |

#### 4.4.2 Insights for Nexus

- Workspace classes map to Docker resource constraints
- Prebuilds run on server push (automated)
- Timeout-based auto-stop (30min idle)

### 4.5 Comparative Analysis

| Feature | Sprites | Codespaces | DevPod | Gitpod | Nexus Target |
|---------|---------|------------|--------|--------|--------------|
| **Cold Start** | <2s | 30-60s | 30s | 45s | **<30s** |
| **Warm Start** | <100ms | <5s | <5s | <5s | **<2s** |
| **Local Option** | No | No | Yes | No | **Yes (Docker)** |
| **Hybrid** | No | No | Limited | No | **Yes (Docker+Sprite)** |
| **Cost** | Pay-per-use | $0.18/hr | Free | $9/mo | **Free (local)** |
| **Offline** | No | No | Yes | No | **Yes (Docker)** |

---

## 5. Architecture

### 5.1 System Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Nexus Workspace System                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────┐     ┌─────────────────────┐     ┌───────────────┐ │
│  │     CLI (boulder)   │     │    IDE Plugins      │     │    SDK        │ │
│  │  • boulder ws up    │     │  • OpenCode         │     │  • TypeScript │ │
│  │  • boulder ws down  │     │  • Claude Code      │     │  • Go         │ │
│  │  • boulder ws list  │     │  • Cursor           │     │  • Python     │ │
│  └──────────┬──────────┘     └──────────┬──────────┘     └───────┬───────┘ │
│             │                           │                        │         │
│             └───────────────────────────┼────────────────────────┘         │
│                                         │                                  │
│                                         ▼                                  │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    Workspace Manager (Go)                          │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐  │   │
│  │  │   Provider   │  │   Worktree   │  │   Port Allocator         │  │   │
│  │  │   Registry   │  │   Manager    │  │   (Dynamic)              │  │   │
│  │  └──────────────┘  └──────────────┘  └──────────────────────────┘  │   │
│  └────────────────────────────────────────┬──────────────────────────┘   │
│                                           │                                │
│                    ┌──────────────────────┼──────────────────────┐         │
│                    │                      │                      │         │
│                    ▼                      ▼                      ▼         │
│  ┌─────────────────────────┐  ┌─────────────────────┐  ┌───────────────┐   │
│  │    Docker Backend       │  │   Sprite Backend    │  │   Mock        │   │
│  │  ┌───────────────────┐  │  │  ┌───────────────┐  │  │  (Testing)    │   │
│  │  │  Docker Engine    │  │  │  │  Sprite API   │  │  │               │   │
│  │  │  • Containers     │  │  │  │  • Firecracker│  │  │               │   │
│  │  │  • Volumes        │  │  │  │  • Checkpoints│  │  │               │   │
│  │  │  • Networks       │  │  │  │  • Billing    │  │  │               │   │
│  │  └───────────────────┘  │  │  └───────────────┘  │  │               │   │
│  └─────────────────────────┘  └─────────────────────┘  └───────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                      Supporting Services                           │   │
│  │  ┌─────────────┐  ┌───────────────┐  ┌──────────────────────────┐  │   │
│  │  │   Daemon    │  │   Telemetry   │  │   Friction Collection    │  │   │
│  │  │  (WebSocket)│  │  (Agent Trace)│  │   (Usage Analytics)      │  │   │
│  │  └─────────────┘  └───────────────┘  └──────────────────────────┘  │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 5.2 Component Architecture

#### 5.2.1 Workspace Manager

```go
// internal/workspace/manager.go
type Manager struct {
    provider      Provider              // Backend (Docker/Sprite)
    gitManager    *git.Manager          // Worktree operations
    portAllocator *ports.Allocator      // Dynamic port allocation
    stateStore    *state.Store          // Workspace metadata
    telemetry     *telemetry.Collector  // Agent Trace integration
}

// Core operations
func (m *Manager) Create(name string, opts CreateOptions) error
func (m *Manager) Start(name string) error
func (m *Manager) Stop(name string) error
func (m *Manager) Switch(name string) error        // Sub-2s context switch
func (m *Manager) Destroy(name string) error
func (m *Manager) Snapshot(name string) (string, error)
func (m *Manager) Restore(name, snapshotID string) error
```

#### 5.2.2 Provider Interface

```go
// pkg/workspace/provider.go
type Provider interface {
    // Lifecycle
    Create(ctx context.Context, spec WorkspaceSpec) (*Workspace, error)
    Start(ctx context.Context, id string) error
    Stop(ctx context.Context, id string) error
    Destroy(ctx context.Context, id string) error
    
    // State
    Get(ctx context.Context, id string) (*Workspace, error)
    List(ctx context.Context, filter ListFilter) ([]Workspace, error)
    
    // Health
    Health(ctx context.Context) error
    
    // Resources
    Stats(ctx context.Context, id string) (*ResourceStats, error)
    
    // Cleanup
    Close() error
}

// Backend implementations
type DockerProvider struct { /* ... */ }
type SpriteProvider struct { /* ... */ }
type MockProvider struct { /* ... */ }  // For testing
```

#### 5.2.3 Worktree Manager

```go
// pkg/git/manager.go
type Manager struct {
    repoRoot string
}

func (m *Manager) CreateWorktree(name string) (string, error) {
    // Creates: .nexus/worktrees/<name>/
    // Branch: nexus/<name>
}

func (m *Manager) RemoveWorktree(name string) error
func (m *Manager) ListWorktrees() ([]Worktree, error)
func (m *Manager) SyncWorktree(name string) error  // git pull, etc.
```

#### 5.2.4 Port Allocator

```go
// pkg/ports/allocator.go
type Allocator struct {
    basePort    int      // Starting port range
    allocations map[string]int  // workspace -> ssh port
}

func (a *Allocator) Allocate(workspace string, service string) (int, error) {
    // Algorithm:
    // 1. Hash workspace name for deterministic base
    // 2. Assign sequential ports for services
    // 3. Check availability, increment if conflict
}

// Port mapping example:
// Workspace: feature-auth (base: 32768)
//   SSH:      32768
//   Web:      32769 (container:3000)
//   API:      32770 (container:5000)
//   Postgres: 32771 (container:5432)
```

### 5.3 Data Flow Diagrams

#### 5.3.1 Workspace Creation Flow

```
User: boulder workspace create feature-auth
            │
            ▼
┌─────────────────────────┐
│   CLI: Parse arguments  │
│   - name: feature-auth  │
│   - template: node      │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Manager: Validate     │
│   - Check name format   │
│   - Check not exists    │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐     ┌─────────────────────┐
│   Git: Create Worktree  │────▶│  git worktree add   │
│   - Branch: nexus/feat  │     │  .nexus/worktrees/  │
└───────────┬─────────────┘     └─────────────────────┘
            │
            ▼
┌─────────────────────────┐
│   Provider: Create      │
│   - Allocate ports      │
│   - Create container    │
│   - Mount worktree      │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Initialize Workspace  │
│   - Copy .env.example   │
│   - Run init scripts    │
│   - Start services      │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Telemetry: Record     │
│   - Workspace created   │
│   - Duration, config    │
└───────────┬─────────────┘
            │
            ▼
         Success!
```

#### 5.3.2 Workspace Switch Flow (Sub-2s Target)

```
User: boulder workspace switch feature-auth
            │
            ▼
┌─────────────────────────┐
│   Current: feature-ui   │
│   Target: feature-auth  │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Checkpoint Current    │
│   - Save running state  │
│   - Persist terminals   │
│   - Pause processes     │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Stop Current          │
│   - docker stop (fast)  │
│   - Keep volumes        │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Start Target          │
│   - docker start        │
│   - Restore state       │
│   - Resume processes    │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Update .nexus/current │
│   - Set active workspace│
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Restore Context       │
│   - Terminal history    │
│   - Editor state        │
│   - Port forwards       │
└───────────┬─────────────┘
            │
            ▼
         Success! (<2s)
```

#### 5.3.3 File Operation Flow (via Daemon)

```
IDE Plugin (OpenCode)
         │
         │ fs.readFile("/workspace/src/app.ts")
         ▼
┌─────────────────────────┐
│   SDK: TypeScript       │
│   - Build RPC request   │
│   - Send over WebSocket │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Daemon: Go WebSocket  │
│   - JWT auth            │
│   - Route to handler    │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Handler: FS Operation │
│   - Validate path       │
│   - Check permissions   │
│   - Read file           │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Response: File Data   │
│   - Return content      │
│   - Record telemetry    │
└───────────┬─────────────┘
            │
            ▼
         IDE Plugin
```

### 5.4 State Management

#### 5.4.1 Workspace States

```
                    ┌─────────────┐
         ┌─────────▶│   PENDING   │◀────────┐
         │          │  (creating) │         │
         │          └──────┬──────┘         │
         │                 │                │
         │                 ▼                │
         │          ┌─────────────┐         │
         │    ┌────│    STOPPED  │────┐    │
         │    │    │   (ready)   │    │    │
         │    │    └──────┬──────┘    │    │
         │    │           │           │    │
    destroy  start      switch      stop  create
         │    │           │           │    │
         │    │           ▼           │    │
         │    │    ┌─────────────┐    │    │
         │    └────│   RUNNING   │────┘    │
         │         │   (active)  │         │
         │         └──────┬──────┘         │
         │                │                │
         │                ▼                │
         │         ┌─────────────┐         │
         └─────────│    ERROR    │─────────┘
                   │  (failed)   │
                   └─────────────┘
```

#### 5.4.2 State Persistence

```go
// State stored in: .nexus/workspaces/<name>/state.json
type WorkspaceState struct {
    ID            string                 `json:"id"`
    Name          string                 `json:"name"`
    Status        WorkspaceStatus        `json:"status"`
    Backend       BackendType            `json:"backend"`
    CreatedAt     time.Time              `json:"created_at"`
    UpdatedAt     time.Time              `json:"updated_at"`
    
    // Git
    Branch        string                 `json:"branch"`
    WorktreePath  string                 `json:"worktree_path"`
    
    // Resources
    Ports         map[string]int         `json:"ports"`  // service -> host port
    ContainerID   string                 `json:"container_id"`
    
    // Configuration
    Image         string                 `json:"image"`
    EnvVars       map[string]string      `json:"env_vars"`
    Volumes       []VolumeMount          `json:"volumes"`
    
    // Runtime
    LastActive    time.Time              `json:"last_active"`
    ProcessState  *ProcessState          `json:"process_state,omitempty"`
}
```

### 5.5 Network Architecture

#### 5.5.1 Port Allocation Strategy

```
Port Range Allocation:

┌─────────────────────────────────────────────────────────────┐
│  32768 - 32799  │  Reserved (system)                         │
├─────────────────────────────────────────────────────────────┤
│  32800 - 34999  │  Docker backend workspaces                 │
│                 │  - Base: 32800                             │
│                 │  - Per-workspace: 10 ports                 │
│                 │  - Max workspaces: 220                     │
├─────────────────────────────────────────────────────────────┤
│  35000 - 39999  │  Sprite backend workspaces                 │
│                 │  - Remote port forwarding                  │
├─────────────────────────────────────────────────────────────┤
│  40000 - 65535  │  Dynamic allocation (fallback)             │
└─────────────────────────────────────────────────────────────┘

Per-Workspace Port Assignment:
  Offset 0: SSH access (if enabled)
  Offset 1: Web/dashboard
  Offset 2: API server
  Offset 3: Database
  Offset 4: Cache (Redis)
  Offset 5-9: Additional services
```

#### 5.5.2 Container Networking

```
Docker Network Topology:

┌─────────────────────────────────────────────────────────────┐
│                    nexus-workspace-network                   │
│  (Bridge network, isolated per workspace)                   │
│                                                              │
│  ┌─────────────────┐      ┌─────────────────┐               │
│  │  Main Container │      │  DB Container   │               │
│  │  (app server)   │◀────▶│  (Postgres)     │               │
│  │  Port: 3000     │      │  Port: 5432     │               │
│  │  IP: 172.20.0.2 │      │  IP: 172.20.0.3 │               │
│  └────────┬────────┘      └─────────────────┘               │
│           │                                                  │
│           │ Port mapping: 32801:3000                         │
│           ▼                                                  │
│  ┌─────────────────────────────────────────────────────────┐ │
│  │                     Host Machine                         │ │
│  │  localhost:32801 ──────▶ container:3000                 │ │
│  └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

---

## 6. Data Models

### 6.1 Core Entities

#### 6.1.1 Workspace

```typescript
// packages/core/src/workspace/types.ts

interface Workspace {
  // Identity
  id: string;                    // UUID v4
  name: string;                  // User-defined, URL-safe
  displayName?: string;          // Human-readable
  
  // Status
  status: WorkspaceStatus;       // pending | stopped | running | error
  statusMessage?: string;        // Human-readable status
  
  // Backend
  backend: BackendType;          // docker | sprite | mock
  backendConfig: BackendConfig;
  
  // Git
  repository: Repository;
  branch: string;                // nexus/<name>
  worktreePath: string;          // Absolute path
  
  // Resources
  resources: ResourceAllocation;
  ports: PortMapping[];
  
  // Lifecycle
  createdAt: ISO8601Timestamp;
  updatedAt: ISO8601Timestamp;
  lastActiveAt: ISO8601Timestamp;
  expiresAt?: ISO8601Timestamp;  // For temporary workspaces
  
  // Configuration
  config: WorkspaceConfig;
  
  // Metadata
  labels: Record<string, string>;
  annotations: Record<string, string>;
}

type WorkspaceStatus = 
  | 'pending'      // Creating/initializing
  | 'stopped'      // Created but not running
  | 'running'      // Active and accessible
  | 'paused'       // Suspended (checkpointed)
  | 'error'        // Failed state
  | 'destroying'   // Being deleted
  | 'destroyed';   // Deleted (soft delete)

type BackendType = 'docker' | 'sprite' | 'kubernetes' | 'mock';
```

#### 6.1.2 Repository

```typescript
interface Repository {
  // Source
  url: string;                   // git URL
  provider: 'github' | 'gitlab' | 'bitbucket' | 'other';
  
  // Local
  localPath: string;             // Path to main worktree
  
  // Authentication
  auth?: RepositoryAuth;
  
  // Current state
  defaultBranch: string;         // main, master, etc.
  currentCommit: string;         // HEAD SHA
}

interface RepositoryAuth {
  type: 'ssh' | 'https' | 'token';
  // Credentials stored in system keychain, not in state
  keychainRef: string;
}
```

#### 6.1.3 Resource Allocation

```typescript
interface ResourceAllocation {
  // Compute
  cpu: {
    cores: number;               // 0.5, 1, 2, 4, 8
    limit?: number;              // Hard limit (cores)
  };
  memory: {
    bytes: number;               // In bytes (e.g., 8589934592 = 8GB)
    limit?: number;              // Hard limit
    swap?: number;               // Swap allocation
  };
  
  // Storage
  storage: {
    bytes: number;               // Primary storage
    ephemeral?: number;          // Temp/scratch space
  };
  
  // GPU (future)
  gpu?: {
    count: number;
    type: 'nvidia' | 'amd';
    memory: number;
  };
}

// Predefined resource classes
const RESOURCE_CLASSES = {
  'small': { cpu: 1, memory: 2 * GB, storage: 20 * GB },
  'medium': { cpu: 2, memory: 4 * GB, storage: 50 * GB },
  'large': { cpu: 4, memory: 8 * GB, storage: 100 * GB },
  'xlarge': { cpu: 8, memory: 16 * GB, storage: 200 * GB },
} as const;
```

#### 6.1.4 Port Mapping

```typescript
interface PortMapping {
  name: string;                  // Service name (web, api, db)
  protocol: 'tcp' | 'udp';
  
  // Container side
  containerPort: number;
  
  // Host side
  hostPort: number;
  
  // Accessibility
  visibility: 'private' | 'public' | 'org';
  
  // URL (if publicly accessible)
  url?: string;
}
```

#### 6.1.5 Workspace Config

```typescript
interface WorkspaceConfig {
  // Base image
  image: string;                 // Docker image reference
  
  // Alternative: devcontainer.json path
  devcontainerPath?: string;
  
  // Environment
  env: Record<string, string>;
  envFiles: string[];            // Files to load
  
  // Volumes
  volumes: VolumeConfig[];
  
  // Services (Docker Compose style)
  services: ServiceConfig[];
  
  // Lifecycle hooks
  hooks: {
    preCreate?: string[];        // Commands before creation
    postCreate?: string[];       // Commands after creation
    preStart?: string[];         // Commands before start
    postStart?: string[];        // Commands after start
    preStop?: string[];          // Commands before stop
    postStop?: string[];         // Commands after stop
  };
  
  // IDE settings
  ide: {
    default: 'vscode' | 'vim' | 'none';
    extensions: string[];        // VS Code extension IDs
    settings: Record<string, unknown>;
  };
  
  // Idle behavior
  idleTimeout: number;           // Minutes (0 = never)
  shutdownBehavior: 'stop' | 'pause' | 'destroy';
}

interface VolumeConfig {
  type: 'bind' | 'volume' | 'tmpfs';
  source: string;
  target: string;
  readOnly?: boolean;
}

interface ServiceConfig {
  name: string;
  image: string;
  ports: PortMapping[];
  env: Record<string, string>;
  volumes: VolumeConfig[];
  dependsOn: string[];
  healthCheck?: HealthCheckConfig;
}
```

### 6.2 State Machines

#### 6.2.1 Workspace Lifecycle State Machine

```typescript
// State transition definitions
const WORKSPACE_STATE_MACHINE = {
  initial: 'pending',
  
  states: {
    pending: {
      on: {
        CREATE_SUCCESS: 'stopped',
        CREATE_FAILURE: 'error',
        CANCEL: 'destroying',
      },
    },
    
    stopped: {
      on: {
        START: 'running',
        DESTROY: 'destroying',
        SNAPSHOT: 'paused',
      },
    },
    
    running: {
      on: {
        STOP: 'stopped',
        PAUSE: 'paused',
        ERROR: 'error',
        IDLE_TIMEOUT: 'stopped',
        DESTROY: 'destroying',
      },
      // Auto-transitions
      activities: ['healthCheck', 'idleDetection'],
    },
    
    paused: {
      on: {
        RESUME: 'running',
        DESTROY: 'destroying',
      },
    },
    
    error: {
      on: {
        RETRY: 'pending',
        RESET: 'stopped',
        DESTROY: 'destroying',
      },
    },
    
    destroying: {
      on: {
        DESTROY_SUCCESS: 'destroyed',
        DESTROY_FAILURE: 'error',
      },
    },
    
    destroyed: {
      type: 'final',
    },
  },
} as const;

// Transition guards
const TRANSITION_GUARDS = {
  canStart: (ctx: WorkspaceContext) => 
    ctx.resources.available && ctx.backend.healthy,
    
  canDestroy: (ctx: WorkspaceContext) =>
    ctx.status !== 'destroying' && ctx.status !== 'destroyed',
    
  idleTimeout: (ctx: WorkspaceContext) => {
    const idleMs = Date.now() - ctx.lastActiveAt.getTime();
    return idleMs > ctx.config.idleTimeout * 60 * 1000;
  },
};
```

#### 6.2.2 Port Allocation State Machine

```typescript
const PORT_STATE_MACHINE = {
  states: {
    available: {
      on: {
        ALLOCATE: {
          target: 'allocated',
          guard: 'portNotInUse',
        },
      },
    },
    
    allocated: {
      on: {
        BIND: 'bound',
        RELEASE: 'available',
      },
    },
    
    bound: {
      on: {
        UNBIND: 'allocated',
        RELEASE: 'available',
      },
    },
  },
};
```

### 6.3 Database Schema (for State Store)

```sql
-- Workspaces table
CREATE TABLE workspaces (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(64) NOT NULL,
    display_name VARCHAR(255),
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    backend VARCHAR(20) NOT NULL,
    
    -- Git
    repository_url TEXT NOT NULL,
    branch VARCHAR(255) NOT NULL,
    worktree_path TEXT NOT NULL,
    
    -- Resources
    cpu_cores DECIMAL(3,1) NOT NULL,
    memory_bytes BIGINT NOT NULL,
    storage_bytes BIGINT NOT NULL,
    
    -- Backend-specific
    container_id VARCHAR(64),
    backend_metadata JSONB,
    
    -- Configuration
    config JSONB NOT NULL DEFAULT '{}',
    
    -- Lifecycle
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    last_active_at TIMESTAMP WITH TIME ZONE,
    expires_at TIMESTAMP WITH TIME ZONE,
    
    -- Metadata
    labels JSONB DEFAULT '{}',
    annotations JSONB DEFAULT '{}',
    
    -- Constraints
    CONSTRAINT valid_name CHECK (name ~ '^[a-z0-9][a-z0-9-]*[a-z0-9]$'),
    CONSTRAINT valid_status CHECK (status IN (
        'pending', 'stopped', 'running', 'paused', 
        'error', 'destroying', 'destroyed'
    ))
);

-- Ports table
CREATE TABLE ports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name VARCHAR(64) NOT NULL,
    protocol VARCHAR(10) DEFAULT 'tcp',
    container_port INTEGER NOT NULL,
    host_port INTEGER NOT NULL,
    visibility VARCHAR(20) DEFAULT 'private',
    url TEXT,
    
    UNIQUE(workspace_id, name),
    UNIQUE(host_port)
);

-- Snapshots table
CREATE TABLE snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name VARCHAR(255),
    description TEXT,
    
    -- Snapshot data
    container_snapshot VARCHAR(64),
    volume_snapshots JSONB DEFAULT '{}',
    process_state JSONB,
    
    -- Metadata
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    created_by VARCHAR(255),
    size_bytes BIGINT,
    
    -- Retention
    expires_at TIMESTAMP WITH TIME ZONE,
    retention_days INTEGER DEFAULT 30
);

-- Events table (audit log)
CREATE TABLE workspace_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    event_type VARCHAR(64) NOT NULL,
    event_data JSONB,
    
    -- Actor
    actor_type VARCHAR(20) NOT NULL, -- 'user' | 'agent' | 'system'
    actor_id VARCHAR(255),
    
    -- Timestamp
    occurred_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Indexes
CREATE INDEX idx_workspaces_status ON workspaces(status);
CREATE INDEX idx_workspaces_backend ON workspaces(backend);
CREATE INDEX idx_workspaces_name ON workspaces(name);
CREATE INDEX idx_ports_host_port ON ports(host_port);
CREATE INDEX idx_events_workspace ON workspace_events(workspace_id, occurred_at DESC);

-- Triggers
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER workspaces_updated_at
    BEFORE UPDATE ON workspaces
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();
```

---

## 7. API Specification

### 7.1 REST API

#### 7.1.1 Workspaces

```yaml
# Base URL: /api/v1/workspaces

# List workspaces
GET /api/v1/workspaces
Query Parameters:
  - status: filter by status (running, stopped, etc.)
  - backend: filter by backend type
  - label_selector: filter by labels
Response: WorkspaceList

# Create workspace
POST /api/v1/workspaces
Body: CreateWorkspaceRequest
Response: 201 Created + Workspace

# Get workspace
GET /api/v1/workspaces/{id}
Response: Workspace

# Update workspace
PATCH /api/v1/workspaces/{id}
Body: UpdateWorkspaceRequest
Response: Workspace

# Delete workspace
DELETE /api/v1/workspaces/{id}
Query Parameters:
  - force: boolean (kill running workspace)
Response: 204 No Content

# Start workspace
POST /api/v1/workspaces/{id}/start
Response: 202 Accepted

# Stop workspace
POST /api/v1/workspaces/{id}/stop
Body:
  - timeout: grace period (seconds)
Response: 202 Accepted

# Switch to workspace (fast context switch)
POST /api/v1/workspaces/{id}/switch
Response: 200 OK + SwitchResult

# Get workspace logs
GET /api/v1/workspaces/{id}/logs
Query Parameters:
  - follow: boolean (stream)
  - tail: number of lines
  - since: timestamp
Response: text/event-stream or LogEntries

# Execute command in workspace
POST /api/v1/workspaces/{id}/exec
Body: ExecRequest
Response: ExecResult

# Get workspace stats
GET /api/v1/workspaces/{id}/stats
Response: ResourceStats
```

#### 7.1.2 Snapshots

```yaml
# Create snapshot
POST /api/v1/workspaces/{id}/snapshots
Body: CreateSnapshotRequest
Response: 201 Created + Snapshot

# List snapshots
GET /api/v1/workspaces/{id}/snapshots
Response: SnapshotList

# Get snapshot
GET /api/v1/snapshots/{snapshot_id}
Response: Snapshot

# Restore snapshot
POST /api/v1/snapshots/{snapshot_id}/restore
Body:
  - target_workspace_id: optional (restore to different workspace)
Response: 202 Accepted

# Delete snapshot
DELETE /api/v1/snapshots/{snapshot_id}
Response: 204 No Content
```

#### 7.1.3 Port Forwarding

```yaml
# List forwarded ports
GET /api/v1/workspaces/{id}/ports
Response: PortList

# Add port forward
POST /api/v1/workspaces/{id}/ports
Body:
  - container_port: number
  - visibility: private|public|org
Response: 201 Created + PortMapping

# Remove port forward
DELETE /api/v1/workspaces/{id}/ports/{port_id}
Response: 204 No Content

# Make port public (expose via ngrok/proxy)
POST /api/v1/workspaces/{id}/ports/{port_id}/public
Response: PortMapping (with public URL)
```

### 7.2 gRPC API (Internal)

```protobuf
// proto/nexus/workspace/v1/workspace.proto
syntax = "proto3";
package nexus.workspace.v1;

service WorkspaceService {
  // Workspace lifecycle
  rpc CreateWorkspace(CreateWorkspaceRequest) returns (Workspace);
  rpc GetWorkspace(GetWorkspaceRequest) returns (Workspace);
  rpc ListWorkspaces(ListWorkspacesRequest) returns (ListWorkspacesResponse);
  rpc UpdateWorkspace(UpdateWorkspaceRequest) returns (Workspace);
  rpc DeleteWorkspace(DeleteWorkspaceRequest) returns (DeleteWorkspaceResponse);
  
  // Lifecycle operations
  rpc StartWorkspace(StartWorkspaceRequest) returns (Operation);
  rpc StopWorkspace(StopWorkspaceRequest) returns (Operation);
  rpc SwitchWorkspace(SwitchWorkspaceRequest) returns (SwitchWorkspaceResponse);
  
  // File operations (streaming)
  rpc StreamFile(StreamFileRequest) returns (stream FileChunk);
  rpc WriteFile(stream WriteFileRequest) returns (WriteFileResponse);
  
  // Execution
  rpc ExecStream(ExecRequest) returns (stream ExecOutput);
  
  // Snapshots
  rpc CreateSnapshot(CreateSnapshotRequest) returns (Snapshot);
  rpc RestoreSnapshot(RestoreSnapshotRequest) returns (Operation);
  
  // Monitoring
  rpc GetStats(GetStatsRequest) returns (ResourceStats);
  rpc StreamStats(StreamStatsRequest) returns (stream ResourceStats);
  
  // Events
  rpc StreamEvents(StreamEventsRequest) returns (stream WorkspaceEvent);
}

message Workspace {
  string id = 1;
  string name = 2;
  string display_name = 3;
  WorkspaceStatus status = 4;
  BackendType backend = 5;
  Repository repository = 6;
  string branch = 7;
  ResourceAllocation resources = 8;
  repeated PortMapping ports = 9;
  WorkspaceConfig config = 10;
  
  google.protobuf.Timestamp created_at = 20;
  google.protobuf.Timestamp updated_at = 21;
  google.protobuf.Timestamp last_active_at = 22;
}

enum WorkspaceStatus {
  WORKSPACE_STATUS_UNSPECIFIED = 0;
  WORKSPACE_STATUS_PENDING = 1;
  WORKSPACE_STATUS_STOPPED = 2;
  WORKSPACE_STATUS_RUNNING = 3;
  WORKSPACE_STATUS_PAUSED = 4;
  WORKSPACE_STATUS_ERROR = 5;
  WORKSPACE_STATUS_DESTROYING = 6;
  WORKSPACE_STATUS_DESTROYED = 7;
}

enum BackendType {
  BACKEND_TYPE_UNSPECIFIED = 0;
  BACKEND_TYPE_DOCKER = 1;
  BACKEND_TYPE_SPRITE = 2;
  BACKEND_TYPE_KUBERNETES = 3;
}
```

### 7.3 WebSocket API (Real-time)

```typescript
// Connection
const ws = new WebSocket('ws://localhost:8080/v1/ws');

// Authentication (first message)
ws.send(JSON.stringify({
  type: 'auth',
  token: 'jwt-token-here'
}));

// Request/Response pattern
interface WSRequest {
  id: string;           // Client-generated request ID
  type: string;         // Method name
  payload: unknown;     // Method-specific payload
}

interface WSResponse {
  id: string;           // Matches request ID
  success: boolean;
  result?: unknown;     // Success response
  error?: WSError;      // Error details
}

// File operations
interface FSReadFileRequest {
  type: 'fs.readFile';
  payload: {
    path: string;
    encoding?: 'utf8' | 'base64';
  };
}

interface FSReadFileResponse {
  content: string;
  encoding: string;
  size: number;
}

// Execution
interface ExecRequest {
  type: 'exec';
  payload: {
    command: string;
    args?: string[];
    cwd?: string;
    env?: Record<string, string>;
    timeout?: number;
  };
}

// Streaming response for exec
interface ExecStreamMessage {
  type: 'exec.stdout' | 'exec.stderr' | 'exec.exit';
  payload: {
    data?: string;
    exitCode?: number;
  };
}

// Events (server -> client)
interface WorkspaceEvent {
  type: 'workspace.status' | 'workspace.stats' | 'port.forward';
  payload: {
    workspaceId: string;
    // Event-specific data
  };
}
```

### 7.4 CLI Interface

```bash
# Workspace management
boulder workspace create <name> [options]
  --template=<name>        # Use predefined template
  --image=<image>          # Custom Docker image
  --backend=<backend>      # docker (default) | sprite
  --resources=<class>      # small | medium | large | xlarge
  --from=<snapshot>        # Restore from snapshot

boulder workspace up <name>      # Start/create workspace
boulder workspace down <name>    # Stop workspace
boulder workspace switch <name>  # Switch to workspace (<2s)
boulder workspace list           # List all workspaces
boulder workspace show <name>    # Show workspace details
boulder workspace destroy <name> # Delete workspace

# Workspace operations
boulder workspace exec <name> <command> [args...]
  --interactive, -i        # Interactive mode
  --tty, -t                # Allocate TTY

boulder workspace shell <name>   # Open shell in workspace
boulder workspace logs <name>    # View workspace logs
  --follow, -f             # Stream logs
  --tail=<n>               # Last N lines

# Snapshots
boulder workspace snapshot create <name> <snapshot-name>
  --description=<desc>
  
boulder workspace snapshot list <name>
boulder workspace snapshot restore <name> <snapshot-name>
boulder workspace snapshot delete <name> <snapshot-name>

# Port forwarding
boulder workspace port add <name> <container-port>
  --visibility=<vis>       # private | public | org
  
boulder workspace port list <name>
boulder workspace port remove <name> <port-id>

# Configuration
boulder workspace config <name>    # Edit workspace config
boulder workspace repair <name>    # Repair broken workspace

# Global flags
  --backend=<backend>      # Default backend
  --debug                  # Enable debug logging
  --json                   # JSON output format
```

---

## 8. Security Model

### 8.1 Threat Model

```
Threat Actors:
├── External Attacker (Internet)
│   ├── Threat: Unauthorized API access
│   ├── Threat: Container escape
│   └── Threat: Network sniffing
│
├── Malicious Workspace User
│   ├── Threat: Container escape to host
│   ├── Threat: Access other workspaces' data
│   └── Threat: Resource exhaustion (DoS)
│
├── Compromised IDE/Agent
│   ├── Threat: Credential theft
│   ├── Threat: Code exfiltration
│   └── Threat: Unauthorized file access
│
└── Insider Threat (Admin)
    ├── Threat: Access user workspaces
    └── Threat: Data retention violations
```

### 8.2 Authentication

#### 8.2.1 Token-Based Authentication

```typescript
// JWT Token Structure
interface NexusToken {
  // Header
  alg: 'ES256';           // ECDSA with P-256
  typ: 'JWT';
  kid: string;            // Key ID for rotation
  
  // Payload
  sub: string;            // User ID
  workspace_id: string;   // Scoped to workspace
  permissions: string[];  // ['fs:read', 'fs:write', 'exec']
  
  // Time constraints
  iat: number;            // Issued at
  exp: number;            // Expiration (1 hour)
  nbf: number;            // Not before
  
  // Context
  jti: string;            // Unique token ID (revocation)
}

// Token generation
const token = await auth.generateToken({
  userId: 'user-123',
  workspaceId: 'ws-456',
  permissions: ['fs:*', 'exec:read'],
  expiresIn: '1h',
});
```

#### 8.2.2 Permission System

```typescript
// Permission hierarchy
const PERMISSIONS = {
  // File system
  'fs:read': ['fs.readFile', 'fs.readdir', 'fs.stat', 'fs.exists'],
  'fs:write': ['fs.writeFile', 'fs.mkdir', 'fs.rm'],
  'fs:admin': ['fs:*'],
  
  // Execution
  'exec:read': ['exec.list', 'exec.logs'],
  'exec:write': ['exec.run', 'exec.kill'],
  'exec:admin': ['exec:*'],
  
  // Workspace
  'workspace:read': ['workspace.get', 'workspace.list'],
  'workspace:write': ['workspace.create', 'workspace.update'],
  'workspace:admin': ['workspace:*'],
  
  // Admin
  'admin': ['*'],
} as const;

// Role definitions
const ROLES = {
  'developer': ['fs:*', 'exec:*', 'workspace:read'],
  'maintainer': ['fs:*', 'exec:*', 'workspace:*'],
  'viewer': ['fs:read', 'exec:read', 'workspace:read'],
  'agent': ['fs:read', 'fs:write', 'exec:write'],
};
```

### 8.3 Container Isolation

#### 8.3.1 Docker Security Profile

```yaml
# Default security options for all containers
security_opts:
  # No new privileges
  - no-new-privileges:true
  
  # Seccomp profile
  - seccomp:./profiles/seccomp-default.json
  
  # AppArmor profile
  - apparmor:nexus-default
  
  # Capabilities
  cap_drop:
    - ALL
  cap_add:
    - CHOWN
    - DAC_OVERRIDE
    - FSETID
    - FOWNER
    - SETGID
    - SETUID
    - SETPCAP
    - NET_BIND_SERVICE
    
# Resource limits
resources:
  limits:
    cpus: '2.0'
    memory: 4G
    pids: 1000
  
# Network isolation
network_mode: bridge
networks:
  - nexus-workspace-net
  
# Filesystem
read_only_rootfs: true
tmpfs:
  - /tmp:noexec,nosuid,size=100m
  - /run:noexec,nosuid,size=100m
  
# User
user: "1000:1000"  # Non-root
```

#### 8.3.2 Workspace Network Isolation

```
Network Architecture:

┌─────────────────────────────────────────────────────────────┐
│                         Host                                 │
│  ┌───────────────────────────────────────────────────────┐  │
│  │             Docker Network: nexus-isolated            │  │
│  │  (No external connectivity by default)               │  │
│  │                                                        │  │
│  │  ┌──────────────┐     ┌──────────────┐               │  │
│  │  │  Workspace A │     │  Workspace B │               │  │
│  │  │  (isolated)  │     │  (isolated)  │               │  │
│  │  └──────────────┘     └──────────────┘               │  │
│  │                                                        │  │
│  └───────────────────────────────────────────────────────┘  │
│                            │                                │
│                            ▼                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │           Docker Network: nexus-shared                │  │
│  │  (Controlled external access)                        │  │
│  │                                                        │  │
│  │  ┌─────────────────────────────────────────────────┐  │  │
│  │  │              Proxy Container                     │  │  │
│  │  │  - Outbound HTTPS only                          │  │  │
│  │  │  - Domain whitelist                             │  │  │
│  │  └─────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### 8.4 Data Protection

#### 8.4.1 Encryption

```typescript
// Encryption at rest
interface EncryptionConfig {
  // Volume encryption
  volumes: {
    enabled: true;
    algorithm: 'aes-256-gcm';
    keyManagement: 'host-keyring' | 'aws-kms' | 'gcp-kms';
  };
  
  // State store encryption
  state: {
    enabled: true;
    algorithm: 'aes-256-gcm';
    // Sensitive fields encrypted
    sensitiveFields: ['env_vars', 'volumes', 'backend_metadata'];
  };
  
  // Backup encryption
  backups: {
    enabled: true;
    algorithm: 'aes-256-gcm';
    passphraseRequired: true;
  };
}

// Encryption in transit
const TLS_CONFIG = {
  minVersion: 'TLSv1.3',
  cipherSuites: [
    'TLS_AES_256_GCM_SHA384',
    'TLS_CHACHA20_POLY1305_SHA256',
  ],
  certificatePinning: true,
};
```

#### 8.4.2 Secret Management

```typescript
// Secret handling
interface SecretStore {
  // Secrets never stored in workspace state
  // Only references stored
  
  // Supported backends
  backends: {
    'keychain': macOS Keychain / Windows Credential / Linux Keyring;
    'file': Encrypted file (master password required);
    'env': Environment variables (dev only);
    'vault': HashiCorp Vault (enterprise);
    'aws': AWS Secrets Manager;
    'gcp': GCP Secret Manager;
  };
}

// Usage
const secretRef = await secretStore.store({
  workspaceId: 'ws-123',
  key: 'DATABASE_URL',
  value: 'postgres://user:pass@host/db',
});

// In workspace config, only reference stored
const config = {
  env: {
    DATABASE_URL: { ref: secretRef },
  },
};
```

### 8.5 Audit Logging

```typescript
interface AuditEvent {
  // Event metadata
  id: string;                    // UUID
  timestamp: ISO8601Timestamp;
  severity: 'info' | 'warning' | 'error' | 'critical';
  
  // Actor
  actor: {
    type: 'user' | 'agent' | 'system';
    id: string;
    ip?: string;
    userAgent?: string;
  };
  
  // Resource
  resource: {
    type: 'workspace' | 'file' | 'exec' | 'port';
    id: string;
    workspaceId?: string;
  };
  
  // Action
  action: string;                // e.g., 'workspace.start', 'file.write'
  status: 'success' | 'failure' | 'denied';
  
  // Details (sanitized)
  details: {
    // No sensitive data (passwords, tokens)
    // File paths relative to workspace
    // Commands without arguments that might contain secrets
  };
  
  // Compliance
  retention: number;             // Days to retain
  gdprCategory?: 'personal_data' | 'technical';
}

// Retention policy
const RETENTION_POLICIES = {
  'security_critical': 2555,     // 7 years
  'workspace_lifecycle': 365,    // 1 year
  'file_operations': 90,         // 90 days
  'exec_commands': 30,           // 30 days
};
```

---

## 9. Error Handling

### 9.1 Error Taxonomy

```typescript
// Error hierarchy
abstract class NexusError extends Error {
  abstract code: string;
  abstract statusCode: number;
  abstract retryable: boolean;
  
  constructor(
    message: string,
    public cause?: Error,
    public context?: Record<string, unknown>
  ) {
    super(message);
  }
}

// Workspace errors
class WorkspaceNotFoundError extends NexusError {
  code = 'WORKSPACE_NOT_FOUND';
  statusCode = 404;
  retryable = false;
}

class WorkspaceAlreadyExistsError extends NexusError {
  code = 'WORKSPACE_ALREADY_EXISTS';
  statusCode = 409;
  retryable = false;
}

class WorkspaceStartError extends NexusError {
  code = 'WORKSPACE_START_FAILED';
  statusCode = 500;
  retryable = true;
}

// Resource errors
class ResourceExhaustedError extends NexusError {
  code = 'RESOURCE_EXHAUSTED';
  statusCode = 503;
  retryable = true;
}

class PortConflictError extends NexusError {
  code = 'PORT_CONFLICT';
  statusCode = 409;
  retryable = true;  // Can retry with different port
}

// Permission errors
class PermissionDeniedError extends NexusError {
  code = 'PERMISSION_DENIED';
  statusCode = 403;
  retryable = false;
}

class AuthenticationError extends NexusError {
  code = 'AUTHENTICATION_FAILED';
  statusCode = 401;
  retryable = false;
}

// Backend errors
class BackendUnavailableError extends NexusError {
  code = 'BACKEND_UNAVAILABLE';
  statusCode = 503;
  retryable = true;
}

class ContainerError extends NexusError {
  code = 'CONTAINER_ERROR';
  statusCode = 500;
  retryable = true;
}
```

### 9.2 Error Handling Matrix

| Error Code | User Message | Retry Strategy | Recovery Action | Log Level |
|------------|--------------|----------------|-----------------|-----------|
| `WORKSPACE_NOT_FOUND` | "Workspace 'xyz' doesn't exist" | No retry | Suggest: `boulder workspace list` | Info |
| `WORKSPACE_ALREADY_EXISTS` | "Workspace 'xyz' already exists" | No retry | Suggest: `boulder workspace switch xyz` | Info |
| `WORKSPACE_START_FAILED` | "Failed to start workspace" | 3 retries, exponential backoff | Auto-retry or manual repair | Error |
| `PORT_CONFLICT` | "Port X already in use" | 1 retry with new port | Auto-retry with different port | Warning |
| `RESOURCE_EXHAUSTED` | "Not enough resources (CPU/memory)" | Retry in 30s | Suggest: destroy unused workspaces | Warning |
| `BACKEND_UNAVAILABLE` | "Docker daemon not responding" | Retry in 5s | Auto-retry, escalate if persists | Error |
| `CONTAINER_ERROR` | "Container crashed unexpectedly" | No retry | Suggest: `boulder workspace repair` | Error |
| `PERMISSION_DENIED` | "You don't have permission" | No retry | Suggest: contact admin | Warning |
| `AUTHENTICATION_FAILED` | "Session expired" | No retry | Prompt: re-authenticate | Info |
| `TIMEOUT` | "Operation timed out" | 1 retry | Auto-retry with increased timeout | Warning |
| `NETWORK_ERROR` | "Network connection failed" | 5 retries, exponential backoff | Auto-retry | Warning |
| `DISK_FULL` | "Not enough disk space" | No retry | Suggest: `boulder workspace cleanup` | Error |
| `GIT_ERROR` | "Git operation failed" | No retry | Show git error details | Error |

### 9.3 Recovery Procedures

#### 9.3.1 Automatic Recovery

```typescript
interface RecoveryStrategy {
  // Detected error
  error: NexusError;
  
  // Recovery steps
  steps: RecoveryStep[];
  
  // Success criteria
  success: (result: unknown) => boolean;
  
  // Failure action
  onFailure: 'escalate' | 'manual' | 'ignore';
}

// Example: Container start failure
const containerStartRecovery: RecoveryStrategy = {
  error: new WorkspaceStartError('Container failed to start'),
  
  steps: [
    // Step 1: Check if container exists but stopped
    {
      name: 'check-container-state',
      action: async (ctx) => {
        const container = await docker.getContainer(ctx.workspaceId);
        return container?.State?.Status;
      },
    },
    
    // Step 2: Try to start existing container
    {
      name: 'start-existing',
      condition: (state) => state === 'exited',
      action: async (ctx) => {
        await docker.start(ctx.workspaceId);
        return { success: true };
      },
    },
    
    // Step 3: Recreate container if missing
    {
      name: 'recreate-container',
      condition: (state) => !state,
      action: async (ctx) => {
        await provider.Create(ctx.spec);
        return { success: true };
      },
    },
  ],
  
  success: (result) => result?.success === true,
  onFailure: 'escalate',
};
```

#### 9.3.2 Manual Recovery Procedures

```bash
# Procedure 1: Workspace in ERROR state
boulder workspace repair <name>
  ↓
1. Check worktree exists
2. Check container exists
3. If mismatch: recreate missing component
4. Validate configuration
5. Attempt restart

# Procedure 2: Port conflicts
boulder workspace port reassign <name>
  ↓
1. Stop workspace
2. Release all ports
3. Allocate new ports
4. Update configuration
5. Start workspace

# Procedure 3: Disk full
boulder workspace cleanup
  ↓
1. List largest workspaces
2. Offer to destroy old/inactive
3. Clear build caches
4. Remove dangling images

# Procedure 4: Git worktree corruption
cd .nexus/worktrees/<name>
git fsck
git worktree repair
# If failed:
git worktree remove <name> --force
boulder workspace repair <name>
```

### 9.4 User-Facing Error Messages

```typescript
const ERROR_MESSAGES: Record<string, ErrorMessage> = {
  'WORKSPACE_NOT_FOUND': {
    title: 'Workspace not found',
    message: (name) => `Workspace "${name}" doesn't exist.`,
    suggestion: 'Run `boulder workspace list` to see available workspaces.',
    action: {
      label: 'List workspaces',
      command: 'boulder workspace list',
    },
  },
  
  'WORKSPACE_START_FAILED': {
    title: 'Failed to start workspace',
    message: 'The workspace container failed to start.',
    suggestion: 'Try running `boulder workspace repair` to fix common issues.',
    action: {
      label: 'Repair workspace',
      command: (name) => `boulder workspace repair ${name}`,
    },
    details: (error) => error.cause?.message,
  },
  
  'PORT_CONFLICT': {
    title: 'Port already in use',
    message: (port) => `Port ${port} is already in use by another process.`,
    suggestion: 'The system will automatically try a different port.',
    autoResolve: true,
  },
  
  'RESOURCE_EXHAUSTED': {
    title: 'Not enough resources',
    message: 'Your system is running low on CPU, memory, or disk.',
    suggestion: 'Destroy unused workspaces or free up system resources.',
    action: {
      label: 'View resource usage',
      command: 'boulder workspace list --resources',
    },
  },
};
```

---

## 10. Edge Cases

### 10.1 Exhaustive Edge Case List

#### 10.1.1 Workspace Lifecycle

| # | Edge Case | Trigger | Expected Behavior |
|---|-----------|---------|-------------------|
| 1 | Create with same name as existing | `boulder ws create foo` when foo exists | Error: "Already exists", suggest switch |
| 2 | Create with invalid name | `boulder ws create "foo bar"` | Error: "Invalid characters", suggest valid name |
| 3 | Create with reserved name | `boulder ws create "current"` | Error: "Reserved name", list reserved |
| 4 | Create while Docker down | Docker daemon not running | Error: "Docker unavailable", retry prompt |
| 5 | Create with insufficient disk | <1GB free | Error: "Disk full", cleanup prompt |
| 6 | Create with network failure | Git clone fails | Retry 3×, then error with details |
| 7 | Destroy while running | `boulder ws destroy foo` (running) | Stop first, then destroy |
| 8 | Destroy non-existent | `boulder ws destroy not-exists` | Error: "Not found", exit 1 |
| 9 | Destroy during active session | Files being edited | Warn: "Active sessions", --force required |
| 10 | Start already running | `boulder ws up foo` (running) | Success: "Already running" |
| 11 | Start with port conflicts | Ports now in use | Auto-reassign ports, log warning |
| 12 | Start with image missing | Image pulled but gone | Re-pull image, retry |
| 13 | Stop already stopped | `boulder ws down foo` (stopped) | Success: "Already stopped" |
| 14 | Stop with timeout | Process ignores SIGTERM | SIGKILL after grace period |

#### 10.1.2 Git Operations

| # | Edge Case | Trigger | Expected Behavior |
|---|-----------|---------|-------------------|
| 15 | Worktree already exists | Git worktree not cleaned up | Detect, prompt for reuse/recreate |
| 16 | Branch already exists | `nexus/foo` branch exists | Reuse branch or error with choice |
| 17 | Uncommitted changes on switch | Modified files | Stash changes, restore on switch back |
| 18 | Merge conflict during sync | `git pull` conflicts | Pause, prompt for resolution |
| 19 | Detached HEAD | Checked out commit directly | Warn, offer to create branch |
| 20 | Large file checkout | Git LFS files | Progress indicator, timeout handling |
| 21 | Submodules not initialized | `.gitmodules` exists | Auto-init or prompt |
| 22 | Worktree on different filesystem | Cross-device issue | Copy instead of hardlink |

#### 10.1.3 Container Operations

| # | Edge Case | Trigger | Expected Behavior |
|---|-----------|---------|-------------------|
| 23 | Container exits immediately | App crashes on start | Capture logs, show error, don't restart loop |
| 24 | Container OOM killed | Memory limit exceeded | Error: "Out of memory", suggest larger class |
| 25 | Container CPU throttled | CPU limit hit | Warn: "Performance degraded" |
| 26 | Volume mount fails | Permission denied | Error with fix instructions |
| 27 | Image pull fails | Registry auth/timeout | Retry with backoff, clear error |
| 28 | Network namespace conflict | Rare Docker bug | Recreate network, retry |
| 29 | PID namespace leak | Zombie processes | Cleanup on stop, log warning |
| 30 | Rootfs corruption | Disk issue | Recreate container, preserve data volume |

#### 10.1.4 Port Management

| # | Edge Case | Trigger | Expected Behavior |
|---|-----------|---------|-------------------|
| 31 | All ports exhausted | 32768-65535 in use | Error: "No ports available", cleanup prompt |
| 32 | Port released but still bound | TCP TIME_WAIT | Wait, or skip to next port |
| 33 | Privileged port requested | <1024 | Error: "Not allowed", suggest >1024 |
| 34 | Public URL collision | Ngrok subdomain taken | Auto-generate new, log change |
| 35 | Port forwarding loop | A→B→A | Detect cycle, error |

#### 10.1.5 Resource Management

| # | Edge Case | Trigger | Expected Behavior |
|---|-----------|---------|-------------------|
| 36 | Disk full during operation | Write fails | Pause, cleanup, resume or error |
| 37 | Inode exhaustion | Too many small files | Error with fix instructions |
| 38 | Memory pressure on host | System OOM | Graceful workspace pause |
| 39 | CPU starvation | Too many workspaces | Fair scheduling, degrade gracefully |
| 40 | Network partition | Lost internet | Local ops continue, remote queue |

#### 10.1.6 Concurrency

| # | Edge Case | Trigger | Expected Behavior |
|---|-----------|---------|-------------------|
| 41 | Concurrent creates same name | Race condition | One succeeds, one fails with conflict |
| 42 | Concurrent start/stop | Rapid operations | Queue operations, execute serially |
| 43 | Concurrent destroy and switch | Timing issue | Block until operations complete |
| 44 | File lock contention | Multiple agents editing | Advisory locks, conflict resolution |
| 45 | State corruption on crash | Power loss mid-write | Atomic writes, recovery on restart |

#### 10.1.7 Network & Connectivity

| # | Edge Case | Trigger | Expected Behavior |
|---|-----------|---------|-------------------|
| 46 | WebSocket disconnect | Network blip | Auto-reconnect, replay pending ops |
| 47 | Long poll timeout | Idle connection | Heartbeat, reconnect transparently |
| 48 | TLS certificate expired | Cert rotation | Error, prompt to update CLI |
| 49 | Proxy authentication | Corporate proxy | Prompt for credentials |
| 50 | DNS resolution failure | Bad config | Fallback to IPs, cache results |

### 10.2 Edge Case Handling Code Examples

```go
// Example: Concurrent create handling
func (m *Manager) Create(name string, opts CreateOptions) error {
    // Acquire distributed lock
    lock, err := m.locker.Acquire("workspace:create:"+name, 30*time.Second)
    if err != nil {
        if errors.Is(err, locker.ErrLocked) {
            return &WorkspaceAlreadyExistsError{
                Name: name,
                Message: "Another process is creating this workspace",
            }
        }
        return err
    }
    defer lock.Release()
    
    // Check existence again (double-check)
    if exists, _ := m.Exists(name); exists {
        return &WorkspaceAlreadyExistsError{Name: name}
    }
    
    // Proceed with creation
    // ...
}

// Example: Graceful degradation on resource pressure
func (m *Manager) Start(name string) error {
    // Check system resources
    stats, err := m.getSystemStats()
    if err != nil {
        return err
    }
    
    if stats.DiskFree < MIN_DISK_FREE {
        return &ResourceExhaustedError{
            Resource: "disk",
            Available: stats.DiskFree,
            Required: MIN_DISK_FREE,
            RecoveryHint: "Run 'boulder workspace cleanup' to free space",
        }
    }
    
    if stats.MemoryFree < MIN_MEMORY_FREE {
        // Try to pause other workspaces
        paused, err := m.pauseIdleWorkspaces()
        if err != nil || len(paused) == 0 {
            return &ResourceExhaustedError{
                Resource: "memory",
                RecoveryHint: "Stop other workspaces or increase memory",
            }
        }
        // Log which workspaces were paused
    }
    
    // Proceed with start
    // ...
}
```

---

## 11. Testing Strategy

### 11.1 Testing Pyramid

```
                    ▲
                   /│\
                  / │ \         E2E Tests (5%)
                 /  │  \        - Full user workflows
                /   │   \       - Real Docker/Sprite
               /────┼────\      - Cross-platform
              /     │     \
             /      │      \    Integration Tests (15%)
            /       │       \   - Multi-component
           /        │        \  - Real backends
          /─────────┼─────────\ - Database interactions
         /          │          \
        /           │           \ Unit Tests (80%)
       /            │            \- Pure functions
      /             │             \- Mocked dependencies
     /              │              \- Fast execution
    ────────────────┴────────────────
```

### 11.2 Test Coverage Requirements

| Component | Unit | Integration | E2E | Target Coverage |
|-----------|------|-------------|-----|-----------------|
| Workspace Manager | ✅ | ✅ | ✅ | 90% |
| Provider Interface | ✅ | ✅ | ✅ | 85% |
| Docker Backend | ✅ | ✅ | ✅ | 80% |
| Sprite Backend | ✅ | ✅ | ⚠️ | 70% |
| Git Manager | ✅ | ✅ | ✅ | 90% |
| Port Allocator | ✅ | ✅ | ✅ | 95% |
| State Store | ✅ | ✅ | ✅ | 90% |
| WebSocket Daemon | ✅ | ✅ | ✅ | 80% |
| SDK (TypeScript) | ✅ | ✅ | ✅ | 85% |
| CLI | ✅ | ✅ | ✅ | 75% |

### 11.3 Unit Testing

```go
// Example: Port allocator unit test
func TestAllocator_Allocate(t *testing.T) {
    tests := []struct {
        name      string
        workspace string
        service   string
        wantPort  int
        wantErr   bool
    }{
        {
            name:      "first allocation",
            workspace: "ws-1",
            service:   "web",
            wantPort:  32800,
        },
        {
            name:      "same workspace, different service",
            workspace: "ws-1",
            service:   "api",
            wantPort:  32801,
        },
        {
            name:      "different workspace",
            workspace: "ws-2",
            service:   "web",
            wantPort:  32810,  // Next workspace base
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            a := NewAllocator(32800)
            got, err := a.Allocate(tt.workspace, tt.service)
            
            if tt.wantErr {
                assert.Error(t, err)
                return
            }
            
            assert.NoError(t, err)
            assert.Equal(t, tt.wantPort, got)
        })
    }
}

// Example: Mock provider for testing
type MockProvider struct {
    mock.Mock
}

func (m *MockProvider) Create(ctx context.Context, spec WorkspaceSpec) (*Workspace, error) {
    args := m.Called(ctx, spec)
    return args.Get(0).(*Workspace), args.Error(1)
}

// Table-driven state machine tests
func TestWorkspaceStateMachine(t *testing.T) {
    tests := []struct {
        name          string
        initialState  WorkspaceStatus
        event         Event
        wantState     WorkspaceStatus
        wantErr       bool
    }{
        {
            name:         "stopped + start = running",
            initialState: StatusStopped,
            event:        EventStart,
            wantState:    StatusRunning,
        },
        {
            name:         "running + stop = stopped",
            initialState: StatusRunning,
            event:        EventStop,
            wantState:    StatusStopped,
        },
        {
            name:         "pending + stop = error",
            initialState: StatusPending,
            event:        EventStop,
            wantErr:      true,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            sm := NewStateMachine(tt.initialState)
            err := sm.Transition(tt.event)
            
            if tt.wantErr {
                assert.Error(t, err)
                return
            }
            
            assert.NoError(t, err)
            assert.Equal(t, tt.wantState, sm.Current())
        })
    }
}
```

### 11.4 Integration Testing

```go
// Example: Docker integration test
func TestDockerProvider_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }
    
    ctx := context.Background()
    provider, err := docker.NewProvider()
    require.NoError(t, err)
    defer provider.Close()
    
    // Create workspace
    spec := WorkspaceSpec{
        Name: "test-integration",
        Image: "alpine:latest",
        Resources: ResourceAllocation{
            CPU: 1,
            Memory: 512 * 1024 * 1024,  // 512MB
        },
    }
    
    ws, err := provider.Create(ctx, spec)
    require.NoError(t, err)
    
    // Cleanup
    defer func() {
        _ = provider.Destroy(ctx, ws.ID)
    }()
    
    // Start
    err = provider.Start(ctx, ws.ID)
    require.NoError(t, err)
    
    // Verify running
    ws, err = provider.Get(ctx, ws.ID)
    require.NoError(t, err)
    assert.Equal(t, StatusRunning, ws.Status)
    
    // Stop
    err = provider.Stop(ctx, ws.ID)
    require.NoError(t, err)
    
    // Verify stopped
    ws, err = provider.Get(ctx, ws.ID)
    require.NoError(t, err)
    assert.Equal(t, StatusStopped, ws.Status)
}

// Example: Git worktree integration
func TestGitManager_WorktreeIntegration(t *testing.T) {
    // Setup temp repo
    repo := setupTempRepo(t)
    
    gm := git.NewManagerWithRepoRoot(repo)
    
    // Create worktree
    path, err := gm.CreateWorktree("feature-test")
    require.NoError(t, err)
    
    // Verify branch created
    branch, err := gm.GetBranch("feature-test")
    require.NoError(t, err)
    assert.Equal(t, "nexus/feature-test", branch)
    
    // Verify worktree directory
    _, err = os.Stat(path)
    require.NoError(t, err)
    
    // Cleanup
    err = gm.RemoveWorktree("feature-test")
    require.NoError(t, err)
}
```

### 11.5 E2E Testing

```typescript
// Example: E2E test for workspace lifecycle
describe('Workspace Lifecycle', () => {
  const testWorkspace = `e2e-test-${Date.now()}`;
  
  afterAll(async () => {
    // Cleanup
    await cli.run(`workspace destroy ${testWorkspace} --force`);
  });
  
  test('create workspace', async () => {
    const result = await cli.run(`workspace create ${testWorkspace}`);
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain('created successfully');
  });
  
  test('list includes new workspace', async () => {
    const result = await cli.run('workspace list');
    expect(result.stdout).toContain(testWorkspace);
  });
  
  test('start workspace', async () => {
    const result = await cli.run(`workspace up ${testWorkspace}`);
    expect(result.exitCode).toBe(0);
  });
  
  test('execute command in workspace', async () => {
    const result = await cli.run(
      `workspace exec ${testWorkspace} echo hello`
    );
    expect(result.stdout).toContain('hello');
  });
  
  test('stop workspace', async () => {
    const result = await cli.run(`workspace down ${testWorkspace}`);
    expect(result.exitCode).toBe(0);
  });
});

// Performance E2E test
describe('Performance Requirements', () => {
  test('workspace switch < 2 seconds', async () => {
    // Setup two workspaces
    await cli.run('workspace create perf-test-1');
    await cli.run('workspace create perf-test-2');
    
    // Start both
    await cli.run('workspace up perf-test-1');
    await cli.run('workspace up perf-test-2');
    
    // Measure switch time
    const start = performance.now();
    await cli.run('workspace switch perf-test-1');
    const duration = performance.now() - start;
    
    expect(duration).toBeLessThan(2000);
    
    // Cleanup
    await cli.run('workspace destroy perf-test-1 --force');
    await cli.run('workspace destroy perf-test-2 --force');
  });
});
```

### 11.6 Chaos Testing

```go
// Example: Chaos test - random failures
func TestChaos_RandomFailures(t *testing.T) {
    ctx := context.Background()
    
    // Create fault injector
    fi := chaos.NewFaultInjector()
    
    for i := 0; i < 100; i++ {
        // Randomly inject failures
        fi.InjectRandomFaults([]chaos.FaultType{
            chaos.NetworkLatency,
            chaos.DiskFull,
            chaos.ContainerCrash,
            chaos.PortConflict,
        })
        
        // Run operation
        err := workspaceManager.Create(ctx, fmt.Sprintf("chaos-%d", i))
        
        // Verify graceful handling
        assert.True(t, err == nil || isRecoverable(err))
        
        // Cleanup
        fi.Reset()
    }
}

// Example: Recovery test
func TestRecovery_FromCrash(t *testing.T) {
    // Create workspace
    ws, _ := manager.Create("recovery-test")
    
    // Simulate crash mid-operation
    simulateCrash()
    
    // Verify recovery on restart
    manager2 := NewManager()
    
    // Should detect inconsistent state
    ws, err := manager2.Get("recovery-test")
    require.NoError(t, err)
    
    // Should be able to repair
    err = manager2.Repair("recovery-test")
    require.NoError(t, err)
    
    // Should be usable again
    err = manager2.Start("recovery-test")
    require.NoError(t, err)
}
```

### 11.7 Test Infrastructure

```yaml
# Test configuration
# .nexus/test-config.yaml

environments:
  unit:
    backend: mock
    parallel: true
    coverage: true
    
  integration:
    backends:
      - docker
      - mock
    requires:
      - docker
    timeout: 5m
    
  e2e:
    backends:
      - docker
    matrix:
      os: [ubuntu, macos, windows]
      docker_version: [24.0, 25.0]
    parallel: false
    timeout: 30m

fixtures:
  repositories:
    - name: node-app
      url: https://github.com/example/node-app
      
    - name: go-app
      url: https://github.com/example/go-app
      
  templates:
    - node-postgres
    - go-postgres
    - python-postgres
```

---

## 12. Performance Benchmarks

### 12.1 Target Performance Metrics

| Metric | Target | Acceptable | Measurement |
|--------|--------|------------|-------------|
| **Cold start** | <30s | <60s | Time from create to ready |
| **Warm start** | <2s | <5s | Time from stop to running |
| **Context switch** | <2s | <5s | Time to switch between workspaces |
| **File read (1MB)** | <100ms | <500ms | fs.readFile latency |
| **File write (1MB)** | <200ms | <1s | fs.writeFile latency |
| **Exec command** | <500ms | <2s | Simple command execution |
| **List workspaces** | <100ms | <500ms | boulder workspace list |
| **Port allocation** | <50ms | <200ms | Assign new port |
| **Snapshot create** | <5s | <15s | Checkpoint workspace |
| **Snapshot restore** | <10s | <30s | Restore from checkpoint |

### 12.2 Resource Usage Benchmarks

| Resource | Idle | Light Use | Heavy Use |
|----------|------|-----------|-----------|
| **CPU** | 0.1 cores | 0.5 cores | 2 cores |
| **Memory** | 100MB | 512MB | 4GB |
| **Disk** | 1GB | 5GB | 50GB |
| **Network** | 1KB/s | 100KB/s | 10MB/s |

### 12.3 Scalability Limits

| Limit | Value | Notes |
|-------|-------|-------|
| Max workspaces per host | 50 | Based on 16GB RAM |
| Max ports per workspace | 10 | Configurable |
| Max concurrent operations | 20 | Prevent resource exhaustion |
| Max snapshot size | 100GB | Per-workspace |
| Max workspace lifetime | 30 days | Auto-cleanup |
| Max inactive time | 7 days | Before auto-stop |

### 12.4 Benchmark Implementation

```go
// Benchmark suite
func BenchmarkWorkspaceLifecycle(b *testing.B) {
    ctx := context.Background()
    provider := setupBenchmarkProvider(b)
    
    b.Run("Create", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            name := fmt.Sprintf("bench-create-%d", i)
            _, err := provider.Create(ctx, WorkspaceSpec{Name: name})
            if err != nil {
                b.Fatal(err)
            }
        }
    })
    
    b.Run("StartStop", func(b *testing.B) {
        ws, _ := provider.Create(ctx, WorkspaceSpec{Name: "bench-startstop"})
        
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            provider.Start(ctx, ws.ID)
            provider.Stop(ctx, ws.ID)
        }
    })
    
    b.Run("Switch", func(b *testing.B) {
        ws1, _ := provider.Create(ctx, WorkspaceSpec{Name: "bench-ws1"})
        ws2, _ := provider.Create(ctx, WorkspaceSpec{Name: "bench-ws2"})
        
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            if i%2 == 0 {
                provider.Start(ctx, ws1.ID)
                provider.Stop(ctx, ws2.ID)
            } else {
                provider.Start(ctx, ws2.ID)
                provider.Stop(ctx, ws1.ID)
            }
        }
    })
}

// Real-world benchmark
func BenchmarkRealWorldUsage(b *testing.B) {
    // Simulate: developer working on 3 features
    // Switching every 5 minutes
    
    ctx := context.Background()
    
    // Create 3 workspaces
    workspaces := make([]*Workspace, 3)
    for i := range workspaces {
        ws, _ := provider.Create(ctx, WorkspaceSpec{
            Name: fmt.Sprintf("feature-%d", i),
        })
        workspaces[i] = ws
    }
    
    b.ResetTimer()
    
    // Simulate 8 hour workday
    // 96 switches (every 5 min)
    for i := 0; i < 96; i++ {
        current := i % 3
        prev := (i - 1 + 3) % 3
        
        // Switch
        provider.Stop(ctx, workspaces[prev].ID)
        provider.Start(ctx, workspaces[current].ID)
        
        // Simulate 5 min of work
        time.Sleep(5 * time.Minute)
    }
}
```

### 12.5 Performance Monitoring

```typescript
// Performance telemetry
interface PerformanceMetrics {
  // Timing
  operation: string;
  duration: number;
  
  // Context
  workspaceId?: string;
  backend: string;
  
  // Resources
  cpuUsage: number;
  memoryUsage: number;
  diskUsage: number;
  
  // Result
  success: boolean;
  error?: string;
}

// Real-time performance dashboard
const PERFORMANCE_SLIs = {
  coldStart: {
    p50: '< 15s',
    p95: '< 30s',
    p99: '< 60s',
  },
  warmStart: {
    p50: '< 1s',
    p95: '< 2s',
    p99: '< 5s',
  },
  contextSwitch: {
    p50: '< 1s',
    p95: '< 2s',
    p99: '< 5s',
  },
};
```

---

## 13. Operational Runbook

### 13.1 On-Call Procedures

#### 13.1.1 Alert: Workspace Start Failure Rate > 5%

```
Severity: P2
Runbook:

1. Check system resources
   $ boulder admin stats
   
2. Check Docker daemon status
   $ docker system info
   
3. Check recent errors
   $ boulder admin logs --errors --last=1h
   
4. Common causes:
   a. Disk full → Cleanup old workspaces
   b. Image pull failures → Check registry auth
   c. Port exhaustion → Check for leaked ports
   
5. Escalate if:
   - Error rate > 20%
   - Affects > 10 users
   - Persists > 30 min
```

#### 13.1.2 Alert: High Memory Usage

```
Severity: P3
Runbook:

1. Identify top memory consumers
   $ boulder admin top --sort=memory
   
2. Options:
   a. Contact users to stop unused workspaces
   b. Force-stop idle workspaces (>24h)
   c. Add more memory to host
   
3. Prevention:
   - Lower default resource class
   - Enable auto-shutdown
```

### 13.2 Debugging Commands

```bash
# Workspace debugging
boulder admin workspace inspect <name>
  # Shows: state, resources, ports, recent events

boulder admin workspace logs <name> --system
  # Shows: daemon logs, not just app logs

boulder admin workspace exec <name> --debug
  # Execute with verbose logging

# System debugging
boulder admin stats
  # CPU, memory, disk, network usage

boulder admin ports
  # List all allocated ports

boulder admin networks
  # List Docker networks

# Trace analysis
boulder admin trace <request-id>
  # Full request trace

# Diagnostic bundle
boulder admin support-bundle
  # Collects logs, config, stats for support
```

### 13.3 Common Issues & Resolution

| Issue | Symptoms | Diagnosis | Resolution |
|-------|----------|-----------|------------|
| **Workspace stuck in PENDING** | Create hangs | Check Docker logs | Restart Docker daemon |
| **Port already in use** | Start fails with port error | `lsof -i :PORT` | Kill process or reassign port |
| **Container exits immediately** | Start then stop | Check container logs | Fix app crash or config |
| **Slow file operations** | High latency | Check disk I/O | Add SSD, reduce workspace count |
| **Git auth failures** | Clone fails | Check credentials | Refresh token, check SSH keys |
| **Out of disk** | Operations fail | `df -h` | Cleanup, increase disk |
| **Network timeouts** | External requests fail | Check proxy, DNS | Verify network config |
| **High CPU** | System slow | `top` / `htop` | Identify and throttle workspace |

### 13.4 Backup & Recovery

```bash
# Backup procedure
boulder admin backup create
  # Creates:
  # - State store dump
  # - Workspace metadata
  # - User configurations
  
# Restore procedure
boulder admin backup restore <backup-id>
  # Restores state, recreates workspaces

# Disaster recovery
# 1. Restore state from backup
boulder admin backup restore latest

# 2. Verify worktrees exist
boulder admin worktree verify --repair

# 3. Recreate missing containers
boulder admin workspace repair --all

# 4. Validate
boulder admin health-check
```

### 13.5 Maintenance Windows

```
Scheduled Maintenance:

1. Weekly (Sundays 2am)
   - Cleanup dangling images
   - Prune unused volumes
   - Compact state database

2. Monthly (First Sunday)
   - Update base images
   - Security patches
   - Major version upgrades

3. Ad-hoc
   - Emergency security updates
   - Critical bug fixes

Communication:
- 7 days notice for scheduled
- 24 hours notice for security
- In-app notifications
```

---

## 14. Migration Guide

### 14.1 From Current State (Nexus Pre-Workspace)

```bash
# Current state: Basic Docker support, no worktree isolation
# Target: Full workspace management

# Migration steps:

# 1. Backup existing .nexus directory
cp -r .nexus .nexus.backup.$(date +%Y%m%d)

# 2. Initialize new workspace system
boulder workspace init
  # - Creates .nexus/workspaces/
  # - Migrates existing containers
  # - Preserves configurations

# 3. Convert existing containers
boulder workspace import --detect
  # Detects running containers
  # Creates workspace metadata
  # Preserves all data

# 4. Verify migration
boulder workspace list
  # Should show imported workspaces

# 5. Test workflow
boulder workspace switch <existing>
  # Should work seamlessly
```

### 14.2 From Git Worktrees (No Containers)

```bash
# Current state: Git worktrees only
# Target: Containerized worktrees

# Migration:

# 1. For each existing worktree
for worktree in .nexus/worktrees/*; do
  name=$(basename $worktree)
  
  # Create workspace from existing worktree
  boulder workspace create $name --from-worktree=$worktree
done

# 2. Worktrees now have containers
# Previous: git worktree only
# Now: worktree + container
```

### 14.3 From Other Systems

#### 14.3.1 From GitHub Codespaces

```bash
# Export from Codespaces:
# 1. Download dotfiles
# 2. Note VS Code extensions

# Import to Nexus:
boulder workspace create my-project \
  --devcontainer=.devcontainer/devcontainer.json \
  --dotfiles=https://github.com/user/dotfiles

# Extensions automatically installed
# Post-create hooks run
```

#### 14.3.2 From DevPod

```bash
# DevPod workspaces are compatible
# Use same devcontainer.json

boulder workspace create my-project \
  --devcontainer=./devcontainer.json

# Provider mapping:
# DevPod docker → Nexus docker
# DevPod kubernetes → Nexus kubernetes (future)
```

### 14.4 Rollback Procedure

```bash
# If migration fails:

# 1. Stop all workspaces
boulder workspace list --running | xargs -I {} boulder workspace down {}

# 2. Restore backup
rm -rf .nexus
cp -r .nexus.backup.YYYYMMDD .nexus

# 3. Verify old state
boulder workspace list
  # Should show pre-migration state

# 4. Report issue
boulder admin support-bundle --submit
```

---

## 15. Risk Assessment

### 15.1 Risk Matrix

| Risk | Likelihood | Impact | Mitigation | Owner |
|------|------------|--------|------------|-------|
| **Data loss** | Low | Critical | Daily backups, atomic writes, recovery procedures | Platform |
| **Security breach** | Low | Critical | Container isolation, auth, audit logs | Security |
| **Performance degradation** | Medium | High | Resource limits, monitoring, auto-scaling | Platform |
| **Docker daemon failure** | Medium | High | Health checks, auto-restart, fallback | Platform |
| **Resource exhaustion** | Medium | Medium | Quotas, auto-shutdown, cleanup | Platform |
| **User adoption failure** | Medium | High | UX focus, training, gradual rollout | Product |
| **Vendor lock-in (Sprite)** | Low | Medium | Multi-backend, data portability | Architecture |
| **Integration complexity** | High | Medium | Modular design, clear APIs, testing | Engineering |
| **Maintenance burden** | Medium | Medium | Automation, self-healing, monitoring | Platform |
| **Legal/compliance** | Low | High | GDPR compliance, data residency, audit | Legal |

### 15.2 Mitigation Details

#### 15.2.1 Data Loss Prevention

```
Layers of protection:

1. Workspace-level
   - Atomic state writes (write to temp, rename)
   - Volume snapshots before destructive ops
   
2. Host-level
   - Daily automated backups
   - ZFS snapshots (if available)
   
3. Remote-level (optional)
   - Sync to cloud storage
   - Encrypted at rest
   
4. Recovery
   - Point-in-time restore
   - Workspace repair command
   - Manual recovery procedures
```

#### 15.2.2 Security Hardening

```
Defense in depth:

1. Network
   - TLS 1.3 for all communications
   - Private networks for inter-container
   - Firewall rules

2. Container
   - Non-root user
   - Read-only rootfs
   - Capability dropping
   - Seccomp profiles

3. Host
   - Regular security updates
   - Minimal attack surface
   - Intrusion detection

4. Application
   - Input validation
   - Output encoding
   - Authentication/authorization
```

### 15.3 Business Continuity

```
Disaster Scenarios:

1. Complete host failure
   - Recovery time: 4 hours
   - Recovery point: 24 hours
   - Procedure: Restore backup to new host

2. Docker daemon corruption
   - Recovery time: 30 minutes
   - Recovery point: 0 (running containers unaffected)
   - Procedure: Restart daemon, repair workspaces

3. Workspace corruption
   - Recovery time: 10 minutes
   - Recovery point: Last snapshot
   - Procedure: Restore from snapshot or recreate

4. Security incident
   - Recovery time: 1 hour
   - Recovery point: N/A
   - Procedure: Isolate, investigate, rotate credentials
```

---

## 16. Success Metrics

### 16.1 Key Performance Indicators (KPIs)

| KPI | Target | Measurement | Dashboard |
|-----|--------|-------------|-----------|
| **Adoption rate** | 90% active users | % users with ≥2 workspaces | Analytics |
| **Context switch time** | <2s (p95) | Telemetry timing | Performance |
| **Workspace availability** | 99.9% | Uptime monitoring | Reliability |
| **Error rate** | <0.1% | Failed operations / total | Quality |
| **User satisfaction** | >4.0/5 | Survey (quarterly) | Voice of Customer |
| **Support tickets** | <1/100 ops | Tickets per workspace op | Support |
| **Resource efficiency** | <50% waste | Idle resources / total | Cost |

### 16.2 Leading Indicators

| Indicator | Target | Action if Below |
|-----------|--------|-----------------|
| First workspace creation | <5 min | Improve onboarding |
| Return rate (next day) | >80% | Check usability issues |
| Workspace switch frequency | >5/day | Promote parallel workflow |
| Snapshot usage | >30% of users | Feature awareness campaign |
| Multi-backend usage | >20% use both | Highlight hybrid benefits |

### 16.3 Measurement Implementation

```typescript
// Telemetry events
interface TelemetryEvent {
  event: string;
  timestamp: ISO8601Timestamp;
  user: {
    id: string;  // Hashed
    segment: 'new' | 'active' | 'power';
  };
  workspace?: {
    id: string;  // Hashed
    backend: string;
    age_days: number;
  };
  properties: Record<string, unknown>;
  timing?: {
    duration_ms: number;
  };
  result: 'success' | 'failure' | 'cancelled';
}

// Key events to track
const TRACKED_EVENTS = [
  'workspace.created',
  'workspace.started',
  'workspace.stopped',
  'workspace.switched',
  'workspace.destroyed',
  'snapshot.created',
  'snapshot.restored',
  'port.forwarded',
  'error.occurred',
] as const;
```

### 16.4 Dashboards

```
Executive Dashboard:
- Active workspaces (daily)
- User adoption funnel
- System availability SLA
- Cost per workspace

Engineering Dashboard:
- API latency percentiles
- Error rates by type
- Resource utilization
- Deployment frequency

User Success Dashboard:
- Time to first workspace
- Feature adoption
- Support ticket trends
- NPS score
```

---

## 17. Real-World Testing: hanlun-lms.git

### 17.1 Test Project Profile

```yaml
Project: hanlun-lms
Repository: git@github.com:oursky/hanlun-lms.git
Type: Learning Management System
Stack:
  Frontend: Next.js 14, TypeScript, Tailwind CSS
  Backend: Node.js, Express, tRPC
  Database: PostgreSQL 15, Redis
  Infrastructure: Docker Compose
Complexity:
  Services: 6 (web, api, db, redis, worker, nginx)
  Build time: ~3 minutes (cold)
  Startup time: ~30 seconds
  Port requirements: 3000, 3001, 5432, 6379, 8080
```

### 17.2 Parallel Development Test Scenario

#### 17.2.1 Test Setup

```bash
# Participants
Developer A: Alice (Frontend focus)
Developer B: Bob (Backend focus)

# Test branches
BRANCH_A: feature/student-dashboard    # Alice's work
BRANCH_B: feature/api-performance      # Bob's work
BASE: main

# Preparation
mkdir -p /tmp/hanlun-test
cd /tmp/hanlun-test
git clone git@github.com:oursky/hanlun-lms.git .
```

#### 17.2.2 Test Procedure

```bash
# === PHASE 1: Baseline (No Workspace System) ===

# Alice starts work
git checkout -b feature/student-dashboard main
npm install
npm run dev  # Server on :3000

# Bob needs to review a PR - CONTEXT SWITCH REQUIRED
# Alice must stop her dev server
Ctrl+C  # Stop server

# Alice stashes changes
git stash push -m "dashboard WIP"
git checkout main
git fetch origin feature/api-performance
git checkout -b review/api-perf origin/feature/api-performance
npm install  # Different dependencies!
npm run dev  # Server on :3000 (same port!)

# Conflict! Port 3000 in use
# Alice has to kill her other terminal
# Time lost: 5-10 minutes

# === PHASE 2: With Workspace System ===

# Alice creates workspace
boulder workspace create alice-dashboard \
  --template=node-postgres

# Bob creates workspace  
boulder workspace create bob-api \
  --template=node-postgres

# Both workspaces have isolated:
# - Git branches (nexus/alice-dashboard, nexus/bob-api)
# - Directories (.nexus/worktrees/alice-dashboard/)
# - Containers (nexus-ws-alice-dashboard)
# - Ports (32800-32809 for alice, 32810-32819 for bob)

# Alice starts working
boulder workspace switch alice-dashboard
npm run dev  # Accessible on localhost:32801

# Bob can work simultaneously
boulder workspace switch bob-api
npm run dev  # Accessible on localhost:32811

# Context switch test: Alice reviews Bob's work
# BEFORE: 5-10 minutes
# AFTER:
boulder workspace switch alice-dashboard  # <2 seconds
# Already running, ports preserved, state intact
```

### 17.2.3 Success Criteria

| Criterion | Requirement | Measurement |
|-----------|-------------|-------------|
| **Parallel operation** | Both workspaces run simultaneously | Verify 6 containers each |
| **No port conflicts** | All services accessible | curl all endpoints |
| **Sub-2s switch** | Context switch < 2 seconds | `time boulder workspace switch` |
| **State preservation** | Dev server continues after switch | Verify hot reload works |
| **Git isolation** | No merge conflicts on switch | `git status` shows clean |
| **Data persistence** | Database survives restart | Write data, restart, verify |

### 17.2.4 Test Results Template

```yaml
Test Run: hanlun-lms-parallel-test
Date: 2026-03-01
Testers: Alice, Bob

Results:
  workspace_creation:
    alice: 45s (PASS: <60s)
    bob: 42s (PASS: <60s)
    
  parallel_operation:
    containers_running: 12 (PASS: 12 expected)
    port_conflicts: 0 (PASS: 0)
    
  context_switch:
    attempt_1: 1.2s (PASS: <2s)
    attempt_2: 0.8s (PASS: <2s)
    attempt_3: 1.0s (PASS: <2s)
    
  state_preservation:
    dev_server: PASS (continued running)
    hot_reload: PASS (changes reflected)
    terminal_history: PASS (preserved)
    
  git_isolation:
    branch_conflicts: 0 (PASS: 0)
    file_conflicts: 0 (PASS: 0)
    
  data_persistence:
    database_survive_restart: PASS
    
  overall: PASS
```

### 17.3 Performance Benchmarking

```bash
# Benchmark script for hanlun-lms

#!/bin/bash
set -e

PROJECT="hanlun-lms"
REPO="git@github.com:oursky/hanlun-lms.git"

echo "=== Nexus Workspace Benchmark: $PROJECT ==="

# 1. Cold start time
echo "1. Measuring cold start..."
start=$(date +%s)
boulder workspace create benchmark-cold --from=$REPO
end=$(date +%s)
cold_start=$((end - start))
echo "   Cold start: ${cold_start}s (Target: <60s)"

# 2. Warm start time
echo "2. Measuring warm start..."
boulder workspace down benchmark-cold
start=$(date +%s)
boulder workspace up benchmark-cold
end=$(date +%s)
warm_start=$((end - start))
echo "   Warm start: ${warm_start}s (Target: <5s)"

# 3. Context switch
echo "3. Measuring context switch..."
boulder workspace create benchmark-switch-2 --from=$REPO
start=$(date +%s)
boulder workspace switch benchmark-cold
end=$(date +%s)
switch_time=$((end - start))
echo "   Context switch: ${switch_time}s (Target: <2s)"

# 4. File operations
echo "4. Measuring file operations..."
time boulder workspace exec benchmark-cold \
  "find . -type f -name '*.ts' | wc -l"

# 5. Build performance
echo "5. Measuring build..."
time boulder workspace exec benchmark-cold "npm run build"

# Cleanup
boulder workspace destroy benchmark-cold --force
boulder workspace destroy benchmark-switch-2 --force

echo "=== Benchmark Complete ==="
```

### 17.4 Regression Test Suite

```yaml
# e2e/hanlun-lms.spec.ts

describe('hanlun-lms Real-World Test', () => {
  const REPO = 'git@github.com:oursky/hanlun-lms.git';
  
  test('full development workflow', async () => {
    // 1. Clone and setup
    await cli.run(`workspace create hanlun-dev --from=${REPO}`);
    
    // 2. Install dependencies
    const install = await cli.run(
      'workspace exec hanlun-dev npm install'
    );
    expect(install.exitCode).toBe(0);
    
    // 3. Start development servers
    await cli.run('workspace up hanlun-dev');
    
    // 4. Verify all services accessible
    const ports = [32801, 32802, 32803, 32804, 32805];
    for (const port of ports) {
      const res = await fetch(`http://localhost:${port}/health`);
      expect(res.status).toBe(200);
    }
    
    // 5. Make changes, verify hot reload
    await fs.writeFile(
      '.nexus/worktrees/hanlun-dev/src/app.ts',
      '// test change'
    );
    
    // 6. Run tests
    const test = await cli.run(
      'workspace exec hanlun-dev npm test'
    );
    expect(test.exitCode).toBe(0);
    
    // 7. Create snapshot
    await cli.run(
      'workspace snapshot create hanlun-dev baseline'
    );
    
    // 8. Destroy and restore
    await cli.run('workspace down hanlun-dev');
    await cli.run(
      'workspace snapshot restore hanlun-dev baseline'
    );
    
    // 9. Verify restored
    const restored = await cli.run(
      'workspace exec hanlun-dev cat src/app.ts'
    );
    expect(restored.stdout).toContain('test change');
    
    // Cleanup
    await cli.run('workspace destroy hanlun-dev --force');
  });
  
  test('parallel development simulation', async () => {
    // Two developers, same repo, different branches
    await cli.run('workspace create dev-a --from=${REPO}');
    await cli.run('workspace create dev-b --from=${REPO}');
    
    // Both start working
    await cli.run('workspace up dev-a');
    await cli.run('workspace up dev-b');
    
    // Rapid context switches
    for (let i = 0; i < 10; i++) {
      const start = performance.now();
      await cli.run(`workspace switch ${i % 2 === 0 ? 'dev-a' : 'dev-b'}`);
      const duration = performance.now() - start;
      expect(duration).toBeLessThan(2000);
    }
    
    // Cleanup
    await cli.run('workspace destroy dev-a --force');
    await cli.run('workspace destroy dev-b --force');
  });
});
```

---

## 18. Appendices

### 18.1 Glossary

| Term | Definition |
|------|------------|
| **Workspace** | An isolated development environment combining a git worktree and container |
| **Worktree** | Git feature allowing multiple working directories from one repo |
| **Backend** | The compute provider (Docker, Sprite, Kubernetes) |
| **Context switch** | Changing from one workspace to another |
| **Cold start** | Starting a workspace from stopped state |
| **Warm start** | Resuming a workspace with preserved state |
| **Snapshot** | Point-in-time checkpoint of workspace state |
| **Provider** | Interface abstraction for different backends |
| **Resource class** | Predefined compute configuration (small, medium, large) |
| **Friction** | Any obstacle or slowdown in the development workflow |

### 18.2 References

#### 18.2.1 External Specifications

- [Dev Container Specification](https://containers.dev/implementors/json_reference/)
- [OCI Runtime Spec](https://github.com/opencontainers/runtime-spec)
- [Docker Compose Spec](https://github.com/compose-spec/compose-spec)
- [Agent Trace Spec](https://agent-trace.dev/)

#### 18.2.2 Related Projects

- [sprites.dev](https://sprites.dev/) - Firecracker-based workspaces
- [GitHub Codespaces](https://github.com/features/codespaces) - Cloud dev environments
- [DevPod](https://devpod.sh/) - Client-only dev environments
- [Gitpod](https://gitpod.io/) - Cloud development platform

### 18.3 Revision History

| Version | Date | Author | Changes |
|---------|------|--------|---------|
| 0.1.0 | 2026-02-20 | Architecture Team | Initial draft |
| 0.2.0 | 2026-02-21 | Architecture Team | Added hanlun-lms testing |
| 0.3.0 | 2026-02-22 | Architecture Team | Added edge cases, risk assessment |
| 1.0.0 | 2026-02-22 | Architecture Team | Production-ready PRD |

### 18.4 Approval

| Role | Name | Date | Signature |
|------|------|------|-----------|
| Tech Lead | | | |
| Product Manager | | | |
| Security Review | | | |
| Architecture Board | | | |

---

**End of Document**

*This PRD is a living document. Updates should be proposed via PR and approved by the Architecture Board.*
