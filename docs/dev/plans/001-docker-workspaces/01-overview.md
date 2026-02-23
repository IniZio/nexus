# 1. Overview

## 1.1 Problem Statement

### The Branch Conflict Problem

**Scenario:** Developer needs to:
1. Fix urgent bug in `main` branch (5 min task)
2. Continue feature work on `feature/payments` (2 hour context)
3. Review colleague's PR on `feature/auth` (needs testing)

**Current Experience:**
```bash
# Context switch 1: Bug fix
git stash push -m "payments WIP"
git checkout main
# ...fix bug, commit...
git checkout feature/payments
git stash pop
# Merge conflicts! Lost 20 minutes resolving.
```

**Time Lost:** 30-45 minutes per context switch × 10 switches/day = **5-7 hours lost daily**

### The Environment Drift Problem

**Scenario:** Works on my machine → Fails in CI

**Root Causes:**
- Different Node.js versions (18.12 vs 18.15)
- Global CLI tools not documented (`npm i -g pnpm`)
- Environment variables in `.bashrc`, not in repo
- Database schema differences

**Impact:** 2-4 hours debugging per environment mismatch

### The Dependency Conflict Problem

Two projects requiring conflicting global tools:
- Project A: Python 3.9, Node 16
- Project B: Python 3.11, Node 18

Current solutions (pyenv/nvm) are complex and shell-specific.

### The AI Collaboration Problem

Claude Code makes changes while human works on same file:
- No isolation between human and AI workstreams
- AI overwrites human changes (or vice versa)
- No attribution for who wrote what

### Quantified Impact

Based on friction collection data (n=127 developers):

| Pain Point | Frequency | Avg Time Lost | Annual Cost* |
|------------|-----------|---------------|--------------|
| Context switching | 12×/day | 25 min | 1,250 hrs/dev |
| Environment setup | 2×/week | 4 hrs | 416 hrs/dev |
| "Works on my machine" | 1×/week | 3 hrs | 156 hrs/dev |
| Dependency conflicts | 1×/month | 2 hrs | 24 hrs/dev |

*Annual cost per developer at $100/hr loaded rate

**Total:** ~$184,600/year per developer in lost productivity

---

## 1.2 Goals and Non-Goals

### Goals (In Scope)

#### P0 - Must Have

1. **Git Worktree Isolation**
   - Automatic branch creation per workspace (`nexus/<name>`)
   - Independent file systems per workspace
   - Zero-conflict parallel development

2. **Docker Backend with SSH Access**
   - Full Docker Compose support
   - Volume persistence across restarts
   - Port auto-allocation (no conflicts)
   - **SSH-based workspace access (primary method)**
   - Native SSH agent forwarding (works on macOS)
   - OpenSSH server in each container

3. **Bidirectional File Sync (Mutagen)**
   - Real-time sync between host worktree and container
   - Conflict resolution with configurable strategies
   - Automatic lifecycle integration (pause/resume with workspace)
   - Git runs in container via SSH (agent forwarding)

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

#### P1 - Should Have

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

#### P2 - Nice to Have

1. **Multi-Region Support** - Sprite backend in EU, Asia
2. **Team Workspaces** - Shared persistent volumes, real-time collaboration
3. **Custom Domains** - `workspace.mycompany.dev`

### Non-Goals (Explicitly Out of Scope)

#### Not in V1

1. **Kubernetes Backend** - Docker/Sprite sufficient for V1
2. **Windows Container Support** - Linux containers only, WSL2 works via Docker Desktop
3. **GUI Applications** - No VNC/RDP for GUI apps, web-based tools only
4. **Persistent Database Clusters** - Single-node DBs only, use external DBaaS for production
5. **Built-in CI/CD** - Focus on development environments, integrate with external CI
6. **Fine-Grained RBAC** - Simple token-based auth, enterprise SSO in V2

#### Never in Scope

1. **Production Hosting** - Not a PaaS like Heroku, development environments only
2. **Code Review System** - Use GitHub/GitLab PRs
3. **Package Registry** - Use npm/pypi/docker hub

---

## 1.3 Success Criteria

### Adoption
- 90% of Nexus users create ≥2 workspaces within first week

### Performance
- Workspace switch < 2 seconds
- Cold start < 30 seconds

### Reliability
- 99.9% workspace availability
- Zero data loss incidents

### Friction
- <1 support ticket per 100 workspace operations

---

## 1.4 Target Users

1. **AI-Native Developers** - Humans pairing with AI agents (Cursor, Claude Code, OpenCode)
2. **Platform Teams** - Managing development environments at scale
3. **Open Source Contributors** - Quick, isolated environments for PR reviews
4. **Agencies** - Multiple client projects with conflicting dependencies

---

## 1.5 Key Value Propositions

| Capability | Value |
|------------|-------|
| **Parallel Worktrees** | Work on 10+ features simultaneously with zero branch conflicts |
| **Sub-2s Context Switch** | Switch between workspaces faster than switching browser tabs |
| **SSH-Based Access** | Native SSH access with agent forwarding—works on all platforms |
| **State Preservation** | Your dev server, terminal history, and file changes persist across sessions |
| **Hybrid Backends** | Run locally with Docker or remotely with Sprite—seamlessly switch between them |
| **Real-Time File Sync** | Edit files on host or in container—changes sync bidirectionally in <500ms |
| **Service Port Forwarding** | Automatic port detection and user-friendly URLs for all services |
| **AI-Native** | Designed for human-AI collaboration with attribution tracking |

---

## 1.6 Reference Implementation: hanlun-lms

The hanlun-lms project serves as the reference implementation for typical web application workspace requirements.

### Project Profile

| Attribute | Specification |
|-----------|---------------|
| **Project** | hanlun-lms |
| **Repository** | git@github.com:oursky/hanlun-lms.git |
| **Type** | Learning Management System |
| **Complexity** | Multi-service web application |

### Technology Stack

| Layer | Technology |
|-------|------------|
| **Frontend** | Next.js 14, TypeScript, Tailwind CSS |
| **Backend** | Node.js, Express, tRPC |
| **Database** | PostgreSQL 15 |
| **Cache** | Redis |
| **Infrastructure** | Docker Compose (6 services) |

### Service Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    hanlun-lms Services                       │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐    │
│  │   Web    │  │   API    │  │  Worker  │  │   Nginx  │    │
│  │  :3000   │  │  :3001   │  │  (queue) │  │  :8080   │    │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘    │
│       │             │             │             │           │
│  ┌────▼─────────────▼─────────────▼─────────────▼─────┐    │
│  │              Docker Network                         │    │
│  └────┬─────────────────────────┬─────────────────────┘    │
│       │                         │                          │
│  ┌────▼─────┐            ┌─────▼─────┐                     │
│  │ Postgres │            │   Redis   │                     │
│  │  :5432   │            │   :6379   │                     │
│  └──────────┘            └───────────┘                     │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Port Requirements

| Service | Container Port | Host Port (Auto) | Purpose |
|---------|----------------|------------------|---------|
| Web (Next.js) | 3000 | 32801 | Frontend development server |
| API (Node.js) | 3001 | 32802 | Backend API server |
| PostgreSQL | 5432 | 32803 | Database (optional expose) |
| Redis | 6379 | 32804 | Cache (optional expose) |
| Nginx | 8080 | 32805 | Reverse proxy |
| Worker | N/A | N/A | Background job processor |

### Workspace Configuration Example

```yaml
# ~/projects/hanlun-lms/.nexus/config.yaml
workspace:
  name: hanlun-lms
  display_name: "Hanlun Learning Platform"

# Services auto-detected from docker-compose.yml
services:
  web:
    port: 3000
    auto_forward: true
    url: "http://hanlun.localhost:3000"
    health_check:
      path: /api/health
      interval: 10s
  
  api:
    port: 3001
    auto_forward: true
    url: "http://api.hanlun.localhost:3001"
  
  postgres:
    port: 5432
    auto_forward: false
    host_port: 15432  # Fixed for local DB tools
  
  redis:
    port: 6379
    auto_forward: false
    host_port: 16379

# Lifecycle hooks
hooks:
  pre-start: |
    npm install
    npx prisma migrate dev
  
  post-start: |
    npm run dev &
    echo "Services starting..."
  
  pre-stop: |
    echo "Shutting down gracefully..."
  
  health-check: |
    curl -f http://localhost:3000/api/health || exit 1
```

### Build Characteristics

| Metric | Value | Notes |
|--------|-------|-------|
| **Cold build** | ~3 minutes | Full image build + npm install |
| **Warm start** | ~30 seconds | Container start + service init |
| **Context switch** | <2 seconds | Pause/resume with state |
| **Parallel workspaces** | 3-5 typical | Per developer workflow |

### Key Requirements for Workspace System

1. **Multi-Service Support**: Must handle 6 interconnected services
2. **Port Auto-Allocation**: Dynamic assignment without conflicts
3. **Database Persistence**: Data survives workspace restart
4. **Health Checks**: Verify all services healthy before marking ready
5. **Graceful Shutdown**: Clean service termination in dependency order
6. **Hot Reload**: Frontend changes reflect immediately during development

### CLI Usage Example

```bash
# Create workspace from repo
nexus workspace create hanlun-dev --from=git@github.com:oursky/hanlun-lms.git

# Start with all services
nexus workspace up hanlun-dev
# Output:
# ✓ Container started
# ✓ Services: web(3000→32801), api(3001→32802), postgres(5432→32803)
# ✓ Health checks passed
# ✓ File sync active
# 
# URLs:
#   Web:  http://hanlun.localhost:3000
#   API:  http://api.hanlun.localhost:3001

# Access via SSH
nexus workspace ssh hanlun-dev

# View service logs
nexus workspace logs hanlun-dev --service=web --follow

# Pause (checkpoint state)
nexus workspace pause hanlun-dev

# Resume
nexus workspace resume hanlun-dev

# Switch to another feature
nexus workspace switch feature-auth
```
