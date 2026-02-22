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
| **State Preservation** | Your dev server, terminal history, and file changes persist across sessions |
| **Hybrid Backends** | Run locally with Docker or remotely with Sprite—seamlessly switch between them |
| **AI-Native** | Designed for human-AI collaboration with attribution tracking |
