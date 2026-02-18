# Nexus MVP - Completion Roadmap

## Current State (80% Complete)

### ✅ DONE (Core Features)
1. **Container Workspaces** - Docker-based with SSH
2. **Task Coordination** - SQLite with verification workflow
3. **Agent Management** - Registration and assignment
4. **Ralph Loop** - Auto skill improvement
5. **Comprehensive Tests** - 99 tests, production-ready

### 🎯 MVP Definition
**Minimum Viable Product:**
- Developer can create isolated workspace in one command
- Can assign tasks to agents with verification
- System learns and improves automatically
- Can work on multiple features simultaneously

---

## MVP Completion Plan

### Phase 1: Git Worktree Integration (MVP CRITICAL)
**Goal:** Each workspace = isolated git branch
**Why:** Without this, workspaces share code and conflict

**Tasks:**
1. Auto-create git worktree on `workspace create`
2. Mount worktree to container (not project root)
3. Branch naming: `nexus/<workspace-name>`
4. Sync changes between workspace and main

**Effort:** 2-3 days
**Tests:** 15 integration tests

---

### Phase 2: Essential Multi-Service Templates (MVP CRITICAL)
**Goal:** One-command full dev environment
**Why:** Manual service setup is error-prone

**Templates (3 only):**
1. **node-postgres** - React/Vue + Node API + PostgreSQL
2. **python-postgres** - Flask/Django + PostgreSQL
3. **go-postgres** - Go API + PostgreSQL

**Features:**
- Pre-configured docker-compose.yml
- Environment variables set
- Auto-run migrations
- Health checks

**Effort:** 2-3 days
**Tests:** 12 integration tests

---

### Phase 3: Simple Parallel Execution (MVP NICE-TO-HAVE)
**Goal:** Run 2-3 agents simultaneously
**Why:** Speed up development

**Simplified scope:**
- Assign independent tasks to multiple agents
- Basic conflict detection (same file edited)
- No complex dependency resolution
- No real-time coordination

**Effort:** 2 days
**Tests:** 10 integration tests

---

### Phase 4: Polish & Documentation (MVP CRITICAL)
**Goal:** Usable by others

**Tasks:**
1. README with quickstart
2. Example projects
3. Troubleshooting guide
4. Fix remaining test failures
5. Performance optimization

**Effort:** 2 days

---

## MVP Cutoff (What's NOT in MVP)

**Post-MVP (Future):**
- ❌ Remote workspaces (SSH to other hosts)
- ❌ Complex parallel coordination (5+ agents)
- ❌ Web UI
- ❌ Advanced conflict resolution
- ❌ Plugin system
- ❌ Multiple providers (LXC, QEMU)

---

## MVP Success Criteria

✅ **Can demonstrate:**
```bash
# 1. Create workspace
cd my-project
nexus init
nexus workspace create feature-auth --template node-postgres

# 2. Workspace has isolated git branch
git branch -a
# → nexus/feature-auth

# 3. Full dev environment ready
nexus workspace up feature-auth
nexus workspace ports feature-auth
# → web: localhost:3001
# → api: localhost:5001  
# → postgres: localhost:5433

# 4. Assign task
nexus task create "Implement JWT auth"
nexus task assign <task-id> <agent-id>

# 5. Agent completes work
nexus task verify <task-id>
nexus task approve <task-id>

# 6. System improves
nexus feedback analyze
# → "Updated skill with JWT troubleshooting"

# 7. Multiple workspaces
git checkout main
nexus workspace create feature-payment --template node-postgres
# Both workspaces active, isolated, no conflicts
```

---

## Implementation Order

**Week 1:**
- Git worktree integration (Phase 1)

**Week 2:**
- Multi-service templates (Phase 2)

**Week 3:**
- Simple parallel execution (Phase 3)
- Polish & docs (Phase 4)

**Total: 3 weeks to MVP**

---

## Current vs MVP Gap

| Feature | Current | MVP Need | Gap |
|---------|---------|----------|-----|
| Container workspaces | ✅ | ✅ | None |
| Task verification | ✅ | ✅ | None |
| Ralph loop | ✅ | ✅ | None |
| Git worktrees | ❌ | ✅ | **Need** |
| Multi-service templates | ❌ | ✅ | **Need** |
| Parallel execution | ❌ | ⚠️ | Simplified |
| Remote workspaces | ❌ | ❌ | Post-MVP |
| Web UI | ❌ | ❌ | Post-MVP |

**Status: 2 critical features needed for MVP**

---

## Recommendation

**Focus on Phase 1 (Git Worktrees) immediately.**

Without git worktree isolation, multiple workspaces will conflict and corrupt each other's work. This is the critical missing piece.

Then Phase 2 (Templates) for usability.

Phase 3 (Parallel) can be simplified or deferred if needed.
