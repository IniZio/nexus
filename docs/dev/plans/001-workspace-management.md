# Workspace Management PRD

**Status:** Implemented  
**Created:** 2026-02-20  
**Updated:** 2026-02-23  
**Component:** Workspace  

---

## 1. Overview

### 1.1 Problem Statement

**The Branch Conflict Problem:** Developer needs to fix urgent bug in `main` (5 min), continue feature work on `feature/payments` (2 hour context), and review colleague's PR on `feature/auth`. Current git workflow loses 30-45 minutes per context switch.

**The Environment Drift Problem:** "Works on my machine → Fails in CI" due to different Node.js versions, undocumented global tools, environment variables in `.bashrc`.

**The AI Collaboration Problem:** Claude Code makes changes while human works on same file - no isolation between human and AI workstreams.

### 1.2 Goals (Implemented)

- ✅ **Git Worktree Isolation** - Automatic branch creation per workspace
- ✅ **Docker Backend with SSH Access** - Full Docker Compose support with SSH-based access
- ✅ **Port Auto-Allocation** - Dynamic assignment (32800-34999 range)
- 🚧 **Bidirectional File Sync** - Mutagen integration (partial)
- 📋 **Checkpoint/Resume** - Save/restore workspace state (planned)

### 1.3 Non-Goals

- Kubernetes backend (Docker sufficient)
- Windows container support (Linux only, WSL2 works)
- GUI applications (web-based tools only)
- Production hosting (dev environments only)

---

## 2. Architecture

### 2.1 System Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Nexus Workspace System                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────┐     ┌─────────────────────┐     ┌───────────────┐ │
│  │     CLI (nexus)     │     │    IDE Plugins      │     │    SDK        │ │
│  │  • nexus ws up      │     │  • OpenCode         │     │  • TypeScript │ │
│  │  • nexus ws down    │     │  • Claude Code      │     │  • Go         │ │
│  │  • nexus ws list    │     │  • Cursor           │     │  • Python     │ │
│  │  • nexus ws ssh     │     │                     │     │               │ │
│  └──────────┬──────────┘     └──────────┬──────────┘     └───────┬───────┘ │
│             │                           │                        │         │
│             └───────────────────────────┼────────────────────────┘         │
│                                         │                                  │
│                                         ▼                                  │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    Workspace Manager (Go)                          │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐  │   │
│  │  │   Provider   │  │   Worktree   │  │   Port Allocator         │  │   │
│  │  │   Registry   │  │   Manager    │  │   (SSH + Services)       │  │   │
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
│  │  │  • SSH Server     │  │  │  │  • Billing    │  │  │               │   │
│  │  │  • SSH Keys       │  │  │  │               │  │  │               │   │
│  │  └───────────────────┘  │  │  └───────────────┘  │  │               │   │
│  └─────────────────────────┘  └─────────────────────┘  └───────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 SSH Access Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       SSH Access Architecture                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   User Machine                              Workspace Container             │
│   ┌─────────────────┐                       ┌───────────────────────┐       │
│   │  SSH Client     │◀──── SSH Protocol ───▶│  OpenSSH Server       │       │
│   │  (any client)   │    (port 32801)       │  (sshd on port 22)    │       │
│   └────────┬────────┘                       └───────────┬───────────┘       │
│            │                                            │                    │
│   ┌────────▼────────┐                       ┌───────────▼───────────┐       │
│   │  SSH Agent      │◀─── ForwardAgent ────▶│  ~/.ssh/authorized    │       │
│   │  (keys on host) │                       │  _keys (injected)     │       │
│   └─────────────────┘                       └───────────────────────┘       │
│                                                                             │
│   Access Methods:                                                           │
│   • nexus workspace ssh <workspace>                                         │
│   • ssh -A nexus@localhost -p <port>                                        │
│   • VS Code Remote-SSH                                                      │
│   • Cursor IDE with SSH                                                     │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.3 Configuration Hierarchy

```
1. Node/System    /etc/nexus/config.yaml
2. User           ~/.nexus/config.yaml
3. Project        ~/projects/myapp/.nexus/config.yaml
4. CLI Flags     --backend docker --port 3000
5. Environment   NEXUS_BACKEND=docker
```

### 2.4 Port Allocation

| Range | Purpose |
|-------|---------|
| 32768-32799 | Reserved (system) |
| 32800-34999 | Docker backend workspaces (SSH ports) |
| 35000-39999 | Sprite backend workspaces |
| 40000-65535 | Dynamic allocation (fallback) |

Per-Workspace Assignment:
- Offset 0: SSH access (container:22 → host:32xxx)
- Offset 1: Web/dashboard
- Offset 2: API server
- Offset 3: Database
- Offset 4+: Additional services

---

## 3. API Specification

### 3.1 REST API

#### Workspaces

**List Workspaces**
```http
GET /api/v1/workspaces
```

**Create Workspace**
```http
POST /api/v1/workspaces
Content-Type: application/json

{
  "name": "feature-auth",
  "backend": "docker",
  "ports": [3000, 5173]
}
```

**Start Workspace**
```http
POST /api/v1/workspaces/{id}/start
```

**Stop Workspace**
```http
POST /api/v1/workspaces/{id}/stop
```

**SSH Connection Info**
```http
GET /api/v1/workspaces/{id}/ssh
```

Response:
```json
{
  "workspaceId": "ws-123",
  "enabled": true,
  "host": "localhost",
  "port": 32801,
  "user": "nexus",
  "forwardAgent": true,
  "connectionCommand": "ssh -A nexus@localhost -p 32801"
}
```

### 3.2 CLI Interface

```bash
# Create workspace
nexus workspace create <name> [--backend docker]

# Start/stop workspace
nexus workspace start <name>
nexus workspace stop <name>

# List workspaces
nexus workspace list

# SSH into workspace
nexus workspace ssh <name>

# Execute command
nexus workspace exec <name> -- <command>
```

---

## 4. Configuration

### 4.1 User Configuration

**Location:** `~/.nexus/config.yaml`

```yaml
# User-level configuration
defaults:
  backend: docker
  idle_timeout: 30m

workspaces:
  hanlun:
    path: ~/projects/hanlun-lms
    ports: [3000, 5173]

ssh:
  port_range:
    start: 32800
    end: 34999
  injection:
    enabled: true
    include_agent_keys: true
```

### 4.2 Project Configuration

**Location:** `.nexus/config.yaml`

```yaml
workspace:
  name: hanlun-lms
  display_name: "Hanlun Learning Platform"

ports:
  web:
    container: 3000
    host: 3000
  api:
    container: 5000
    # Auto-allocated if omitted
```

---

## 5. Implementation Status

### 5.1 Implemented Features

| Feature | Status | Notes |
|---------|--------|-------|
| Docker containers | ✅ | Full Docker Compose support |
| SSH access | ✅ | OpenSSH server + key injection |
| SSH agent forwarding | ✅ | Works on macOS |
| Port auto-allocation | ✅ | 32800-34999 range |
| Git worktrees | ✅ | `.worktree/<name>/` |
| Exec via SSH | ✅ | Replaces docker exec |
| nexus workspace ssh | ✅ | Interactive shell |

### 5.2 In Progress

| Feature | Status | Notes |
|---------|--------|-------|
| Mutagen file sync | 🚧 | Partial implementation |
| Checkpoint/restore | 🚧 | Design complete |

### 5.3 Planned

| Feature | Status | Notes |
|---------|--------|-------|
| Lifecycle management | 📋 | Stop/start/pause |
| Remote workspaces | 📋 | Cloud execution |
| Sprite backend | 📋 | Alternative to Docker |

---

## 6. References

- [ADR-001: Worktree Isolation](decisions/001-worktree-isolation.md)
- [ADR-002: Port Allocation](decisions/002-port-allocation.md)
- [Boulder System](../boulder-system.md)

---

**Last Updated:** February 2026
