# Docker Workspaces PRD Update Plan

**Goal:** Update all Docker Workspaces PRD files to use unified `nexus` CLI, add port forwarding design, and add lifecycle management.

## Analysis Summary

### Current State
- CLI uses `boulder` throughout all PRD files
- 8 PRD files need updating
- Missing: Port forwarding design, comprehensive lifecycle management
- hanlun-lms requirements exist but need consolidation

### Changes Required

#### 1. CLI Naming Changes (ALL FILES)
Replace `boulder` → `nexus`:
- `boulder workspace create` → `nexus workspace create`
- `boulder ssh` → `nexus workspace ssh`
- `boulder admin` → `nexus admin`
- All boulder commands become nexus commands

#### 2. Remove Boulder Subcommand References
- No `nexus boulder status|pause|resume`
- No boulder as user-facing CLI
- Keep boulder as internal enforcement (mentioned but not CLI)

#### 3. New Sections to Add

**Port Forwarding Design (New in 02-architecture.md)**
- Automatic port detection from docker-compose.yml
- Service port allocation strategy
- URL generation (localhost or custom domains)
- Port range management per workspace
- hanlun-lms example: 3000, 3001, 5432, 6379, 8080

**Workspace Lifecycle Management (New section)**
- States: pending → stopped → running → paused → stopped
- Auto-start services on workspace up
- Health check system
- Graceful shutdown sequence
- Resource cleanup

**hanlun-lms Requirements (New in 01-overview.md)**
- Document as reference implementation
- Services needed: frontend (3000), backend (3001), postgres (5432), redis (6379), nginx (8080)
- docker-compose service integration

## Files to Update

| File | Changes |
|------|---------|
| README.md | Update CLI commands, add sections to index |
| 01-overview.md | Add hanlun-lms requirements |
| 02-architecture.md | Add port forwarding + lifecycle sections |
| 03-api.md | Replace all CLI commands, add lifecycle API |
| 04-security.md | Replace boulder commands |
| 05-operations.md | Replace all boulder commands |
| 06-testing.md | Replace boulder commands |
| 07-roadmap.md | Replace boulder commands |

## hanlun-lms Requirements (Reference)

From 06-testing.md line 576-591:
- **Project:** hanlun-lms  
- **Stack:** Next.js 14, Node.js, Express, tRPC, PostgreSQL 15, Redis
- **Services:** 6 (web, api, db, redis, worker, nginx)
- **Ports:** 3000 (frontend), 3001 (backend), 5432 (postgres), 6379 (redis), 8080 (nginx)
- **Build:** ~3 min cold, ~30s startup

## Port Forwarding Design Spec

```yaml
# ~/.nexus/config.yaml
workspaces:
  hanlun:
    path: ~/projects/hanlun-lms
    services:
      web:
        port: 3000
        auto_forward: true
        url_format: "{name}.localhost"
      api:
        port: 3001
        auto_forward: true
      postgres:
        port: 5432
        auto_forward: false  # Don't expose DB publicly
        host_port: 15432     # Fixed host port for local tools
```

## Lifecycle States

```
PENDING → STOPPED → RUNNING
            ↑        ↓
            └──── PAUSED
```

**Commands:**
- `nexus workspace up` - Create/start workspace, run pre-start hooks, start services
- `nexus workspace down` - Graceful shutdown, post-stop hooks
- `nexus workspace pause` - Checkpoint state, pause sync, stop container
- `nexus workspace resume` - Restore from checkpoint, resume sync

## Implementation Order

1. Update README.md (index/overview)
2. Update 01-overview.md (add hanlun-lms section)
3. Update 02-architecture.md (add port forwarding + lifecycle)
4. Update 03-api.md (CLI + API changes)
5. Update 04-security.md (commands only)
6. Update 05-operations.md (commands only)
7. Update 06-testing.md (commands only)
8. Update 07-roadmap.md (commands only)

## Verification Checklist

- [ ] All `boulder` replaced with `nexus`
- [ ] No `nexus boulder` subcommand references
- [ ] Port forwarding design documented
- [ ] Lifecycle management documented
- [ ] hanlun-lms requirements documented
- [ ] All CLI examples updated
- [ ] Architecture diagrams updated
- [ ] API spec updated
