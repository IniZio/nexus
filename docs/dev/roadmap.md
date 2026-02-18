# Roadmap

## Current Status

**MVP Complete** - All critical features implemented and tested.

## Completed Milestones

### ✅ MVP Features (Complete)

| Feature | Status | Tests |
|---------|--------|-------|
| Container Workspaces | Complete | 26 tests |
| Multi-Service Templates | Complete | 29 tests |
| Task Verification | Complete | 56 tests |
| Ralph Loop | Complete | 22 tests |
| Agent Management | Complete | 20 tests |

### ✅ Statistics

- **Source Code:** ~4,273 lines
- **Test Code:** ~5,598 lines
- **Test Ratio:** 1.3:1
- **Test Functions:** 153

## Upcoming Features

### Phase 1: Git Worktree Integration

**Goal:** Each workspace has an isolated git branch.

**Status:** Implemented

- Auto-create git worktree on `workspace create`
- Mount worktree to container (not project root)
- Branch naming: `nexus/<workspace-name>`
- Sync changes between workspace and main

**Related ADRs:**
- [001 - Worktree Isolation](decisions/001-worktree-isolation.md)

### Phase 2: Essential Multi-Service Templates

**Goal:** One-command full dev environment.

**Status:** Implemented

**Templates Available:**
1. **node-postgres** - React/Vue + Node API + PostgreSQL
2. **python-postgres** - Flask/Django + PostgreSQL
3. **go-postgres** - Go API + PostgreSQL

### Phase 3: Simple Parallel Execution

**Goal:** Run 2-3 agents simultaneously.

**Status:** Implemented (simplified scope)

- Assign independent tasks to multiple agents
- Basic conflict detection
- No complex dependency resolution

### Phase 4: Polish & Documentation

**Goal:** Usable by others.

**Status:** In Progress

- README with quickstart ✓
- Example projects
- Troubleshooting guide
- Performance optimization

## Post-MVP Features

The following are planned for future releases:

### Remote Workspaces
SSH to other Docker hosts for distributed development.

### Web UI
Visual task board and workspace management.

### Advanced Parallel Coordination
5+ agents with complex dependency resolution.

### Additional Templates
- rust-postgres
- java-postgres
- .NET postgresql

### Plugin System
Extend Nexus with custom providers and templates.

## Related Documentation

- [Architecture](explanation/architecture.md)
- [Architecture Decisions](dev/decisions/)
